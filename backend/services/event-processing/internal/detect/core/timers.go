// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"container/heap"
	"sort"
	"time"
)

// SeriesKey identifies a single (rule, series) stream of state. "series" is the device
// or anchor token the rule is keyed on. Tenant is carried on the rule id (tenant-token
// prefixed, ADR-051 §7) so this stays compact.
type SeriesKey struct {
	Rule   string
	Series string
}

// timer is the one scheduled deadline for a SeriesKey. The wheel holds AT MOST ONE timer per
// key: a reschedule moves this entry with heap.Fix and a cancel removes it, so the heap's size is
// the number of armed keys, never the number of events. gen is the key's generation stamp at the
// time of scheduling; it is what the snapshot serializes beside the deadline.
type timer struct {
	deadline time.Time
	key      SeriesKey
	gen      uint64
	index    int
}

type timerPQ []*timer

func (p timerPQ) Len() int { return len(p) }
func (p timerPQ) Less(i, j int) bool {
	// Deterministic total order: earliest deadline first, then key — so firings are
	// reproducible across a replay regardless of insertion order.
	if !p[i].deadline.Equal(p[j].deadline) {
		return p[i].deadline.Before(p[j].deadline)
	}
	if p[i].key.Rule != p[j].key.Rule {
		return p[i].key.Rule < p[j].key.Rule
	}
	return p[i].key.Series < p[j].key.Series
}
func (p timerPQ) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
	p[i].index = i
	p[j].index = j
}
func (p *timerPQ) Push(x any) {
	t := x.(*timer)
	t.index = len(*p)
	*p = append(*p, t)
}
func (p *timerPQ) Pop() any {
	old := *p
	n := len(old)
	t := old[n-1]
	old[n-1] = nil
	*p = old[:n-1]
	return t
}

// timerWheel is a heap-ordered timer index holding one entry per armed key.
//
// The heap is ordered by the total order (deadline, rule, series), so with one entry per key the
// firing order is a function of the SET of armed deadlines alone — not of the insertion history
// that produced it. That is what makes moving an entry (heap.Fix) indistinguishable, in output,
// from the earlier "push a new entry and leave the superseded one to be discarded when popped":
// the superseded entries never fired and never ordered anything.
//
// Generations are strictly MONOTONIC per key and never recycled (a fired timer bumps the gen
// rather than deleting it). They no longer guard a stale heap entry — there are none — but they
// are part of the serialized snapshot (snapGen, snapTimer.Gen), and a rule purge restarts them by
// design; keeping the same bump discipline keeps the snapshot bytes identical to what the
// push-per-event wheel wrote. The gens map is bounded by the number of distinct keys with timer
// history, not by event volume. live is each armed key's heap entry; it carries the current
// deadline so a reschedule can refuse to move it EARLIER (see scheduleForward).
type timerWheel struct {
	pq   timerPQ
	gens map[SeriesKey]uint64 // generation per key (monotonic; never recycled)
	live map[SeriesKey]*timer // the key's one heap entry, while armed
}

func newTimerWheel() *timerWheel {
	return &timerWheel{gens: map[SeriesKey]uint64{}, live: map[SeriesKey]*timer{}}
}

// hasTimers reports whether any timer is armed. It is exact. The push-per-event wheel also
// answered true while only superseded or cancelled entries remained, so after a cancel the
// idle-advance gate (HasPendingWork) kept admitting bare watermark moves, and a checkpoint for
// each, one per CheckpointInterval, until the stale entry's deadline passed (up to
// MaxRuleDuration). Those moves are driven by the wall clock and are never replayed from the log,
// and they were already absent after any restore (a snapshot keeps live timers only), so nothing
// replay-correct depended on them.
func (w *timerWheel) hasTimers() bool { return w.pq.Len() > 0 }

// schedule sets (or resets) the deadline for key unconditionally: the key's existing entry is
// moved in place (heap.Fix), or one is pushed when the key was not armed. Used by Duration, where
// a fresh matched run may legitimately set an earlier deadline than the one it replaces.
func (w *timerWheel) schedule(key SeriesKey, deadline time.Time) {
	w.gens[key]++
	if t, ok := w.live[key]; ok {
		t.deadline = deadline
		t.gen = w.gens[key]
		heap.Fix(&w.pq, t.index)
		return
	}
	t := &timer{deadline: deadline, key: key, gen: w.gens[key]}
	w.live[key] = t
	heap.Push(&w.pq, t)
}

// scheduleForward resets the deadline for key but NEVER moves it earlier. Absence and Session
// track the latest activity, so a bounded-late out-of-order event (an earlier event time)
// must not shrink a deadline a later event time already extended — doing so fires the
// dead-man prematurely (a false detection) or splits one session in two.
func (w *timerWheel) scheduleForward(key SeriesKey, deadline time.Time) {
	if cur, ok := w.live[key]; ok && !deadline.After(cur.deadline) {
		return // a later (or equal) deadline already stands
	}
	w.schedule(key, deadline)
}

// cancel removes any pending timer for key without scheduling a new one.
func (w *timerWheel) cancel(key SeriesKey) {
	if _, ok := w.gens[key]; ok {
		w.gens[key]++
		if t, armed := w.live[key]; armed {
			heap.Remove(&w.pq, t.index)
			delete(w.live, key)
		}
	}
}

// purgeMatching sweeps every rule the predicate selects out of the wheel entirely — its
// generation bookkeeping (gens/live) and any of its timers still in the pq heap. It serves
// both a single rule's removal (ADR-051 slice 4b-3) and the ADR-077 tenant eviction, which
// selects by the rule id's owning tenant. fire/apply already early-return on a missing
// rule, so a leftover timer would only fire into a no-op; this is GC, not correctness, but
// a removed rule must leave the wheel clean. Indices are reset before heap.Init to keep the
// heap consistent.
//
// The gens map is swept along with the rest, which reintroduces the generation-recycling
// hazard this type's doc describes — a later timer for the same key would restart at
// generation 1 and could match a stale heap entry. That is deliberate and is the same
// trade the single-rule removal already made: the heap is swept in the same call, so there is no stale
// entry left to match, and the alternative — retaining a map keyed on the purged tenant's
// rule ids and device tokens — would leave the tenant's identifiers in memory and in the
// snapshot (gens is serialized) after an erasure claimed they were gone.
func (w *timerWheel) purgeMatching(match func(ruleID string) bool) int {
	n := deleteSeriesKeysMatching(w.gens, match)
	n += deleteSeriesKeysMatching(w.live, match)
	old := w.pq
	kept := old[:0]
	for _, t := range old {
		if !match(t.key.Rule) {
			t.index = len(kept)
			kept = append(kept, t)
		}
	}
	if len(kept) == len(old) {
		// Nothing matched, so the heap is untouched and its invariant already holds. Worth the
		// branch because heap.Init is O(n) over every tenant's live timers and the steady state
		// of a tenant purge is a pass that matches nothing — once a minute, for as long as the
		// deletion takes. See closeHeap.purgeMatching, which skips the same rebuild.
		return n
	}
	for i := len(kept); i < len(old); i++ {
		old[i] = nil // release the dropped *timer for GC
	}
	w.pq = kept
	heap.Init(&w.pq)
	return n
}

// firedTimer is a due timer: the key plus the deadline it was scheduled for (so the
// detection is stamped at the moment the condition elapsed, not the watermark overshoot).
type firedTimer struct {
	key      SeriesKey
	deadline time.Time
}

// popDue returns the armed timers whose deadline is <= wm, in deterministic (deadline, key)
// order, removing them from the wheel.
func (w *timerWheel) popDue(wm time.Time) []firedTimer {
	var fired []firedTimer
	for w.pq.Len() > 0 {
		top := w.pq[0]
		if top.deadline.After(wm) {
			break
		}
		heap.Pop(&w.pq)
		fired = append(fired, firedTimer{key: top.key, deadline: top.deadline})
		// One-shot: firing consumes the live timer. BUMP (never delete) the gen so the value
		// is never recycled — a later schedule for this key gets a strictly higher gen.
		w.gens[top.key]++
		delete(w.live, top.key)
	}
	return fired
}

// --- snapshot support (serialized as part of the Engine snapshot) ---

type snapTimer struct {
	Deadline time.Time `json:"deadline"`
	Rule     string    `json:"rule"`
	Series   string    `json:"series"`
	Gen      uint64    `json:"gen"`
}

type snapGen struct {
	Rule   string `json:"rule"`
	Series string `json:"series"`
	Gen    uint64 `json:"gen"`
}

// snapshot captures the armed timers (the heap holds nothing else) plus the generation table, in
// a stable order so the serialized bytes are deterministic.
func (w *timerWheel) snapshot() ([]snapTimer, []snapGen) {
	timers := make([]snapTimer, 0, w.pq.Len())
	for _, t := range w.pq {
		timers = append(timers, snapTimer{Deadline: t.deadline, Rule: t.key.Rule, Series: t.key.Series, Gen: t.gen})
	}
	sort.Slice(timers, func(i, j int) bool {
		if !timers[i].Deadline.Equal(timers[j].Deadline) {
			return timers[i].Deadline.Before(timers[j].Deadline)
		}
		if timers[i].Rule != timers[j].Rule {
			return timers[i].Rule < timers[j].Rule
		}
		return timers[i].Series < timers[j].Series
	})
	gens := make([]snapGen, 0, len(w.gens))
	for k, g := range w.gens {
		gens = append(gens, snapGen{Rule: k.Rule, Series: k.Series, Gen: g})
	}
	sort.Slice(gens, func(i, j int) bool {
		if gens[i].Rule != gens[j].Rule {
			return gens[i].Rule < gens[j].Rule
		}
		return gens[i].Series < gens[j].Series
	})
	return timers, gens
}

func restoreTimerWheel(timers []snapTimer, gens []snapGen) *timerWheel {
	w := newTimerWheel()
	for _, g := range gens {
		w.gens[SeriesKey{Rule: g.Rule, Series: g.Series}] = g.Gen
	}
	// snapshot() keeps only LIVE timers (one per key), so each restored timer is that key's
	// current live deadline — enough to rebuild the forward-only `live` map exactly.
	for _, t := range timers {
		key := SeriesKey{Rule: t.Rule, Series: t.Series}
		if t.Gen != w.gens[key] {
			continue // not the key's live timer (only a hand-edited snapshot says so): the old wheel discarded it when popped
		}
		if cur, dup := w.live[key]; dup {
			// Two timers for one key (a hand-edited snapshot; snapshot() never writes it). The old
			// wheel restored both and the EARLIER fired, its generation bump voiding the other.
			if t.Deadline.Before(cur.deadline) {
				cur.deadline = t.Deadline
				heap.Fix(&w.pq, cur.index)
			}
			continue
		}
		nt := &timer{deadline: t.Deadline, key: key, gen: t.Gen}
		w.live[key] = nt
		heap.Push(&w.pq, nt)
	}
	return w
}
