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

// timer is one scheduled deadline for a SeriesKey. gen is a generation stamp: a timer
// is stale (and silently discarded when popped) if its gen != the wheel's live gen for
// that key. This gives O(1) logical reset/cancel — the survey's acid-test primitive:
// reset-per-event, fire-off-watermark, survive-restart-via-snapshot.
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

// timerWheel is a heap-ordered timer index with lazy, generation-stamped cancellation.
// Generations are strictly MONOTONIC per key and never recycled (a fired timer bumps the
// gen rather than deleting it): if the counter ever restarted at 1, a stale timer still in
// the heap could match a recycled live gen and fire falsely — and because snapshot() keeps
// only live timers, that revival would also make a restored run diverge from the clean one.
// The gens map is thus bounded by the number of distinct keys with timer history (the same
// cardinality class as every other per-key structure, governed by the state budget), not by
// event volume. live records each key's current deadline so a reschedule can refuse to move
// it EARLIER (see scheduleForward).
type timerWheel struct {
	pq   timerPQ
	gens map[SeriesKey]uint64    // live generation per key (monotonic; never recycled)
	live map[SeriesKey]time.Time // current live deadline per key
}

func newTimerWheel() *timerWheel {
	return &timerWheel{gens: map[SeriesKey]uint64{}, live: map[SeriesKey]time.Time{}}
}

// schedule sets (or resets) the deadline for key unconditionally. Any previously-scheduled
// timer for the same key is invalidated by the generation bump. Used by Duration, where a
// fresh matched run may legitimately set an earlier deadline than a stale, already-cancelled
// timer.
func (w *timerWheel) schedule(key SeriesKey, deadline time.Time) {
	w.gens[key]++
	w.live[key] = deadline
	heap.Push(&w.pq, &timer{deadline: deadline, key: key, gen: w.gens[key]})
}

// scheduleForward resets the deadline for key but NEVER moves it earlier. Absence and Session
// track the latest activity, so a bounded-late out-of-order event (an earlier event time)
// must not shrink a deadline a later event time already extended — doing so fires the
// dead-man prematurely (a false detection) or splits one session in two.
func (w *timerWheel) scheduleForward(key SeriesKey, deadline time.Time) {
	if cur, ok := w.live[key]; ok && !deadline.After(cur) {
		return // a later (or equal) deadline already stands
	}
	w.schedule(key, deadline)
}

// cancel invalidates any pending timer for key without scheduling a new one.
func (w *timerWheel) cancel(key SeriesKey) {
	if _, ok := w.gens[key]; ok {
		w.gens[key]++
		delete(w.live, key)
	}
}

// purgeRule sweeps rule id out of the wheel entirely — its generation bookkeeping
// (gens/live) and any of its timers still in the pq heap (ADR-051 slice 4b-3 rule
// removal). fire/apply already early-return on a missing rule, so a leftover timer would
// only fire into a no-op; this is GC, not correctness, but a removed rule must leave the
// wheel clean. Indices are reset before heap.Init to keep the heap consistent.
func (w *timerWheel) purgeRule(id string) {
	deleteSeriesKeys(w.gens, id)
	deleteSeriesKeys(w.live, id)
	old := w.pq
	kept := old[:0]
	for _, t := range old {
		if t.key.Rule != id {
			t.index = len(kept)
			kept = append(kept, t)
		}
	}
	for i := len(kept); i < len(old); i++ {
		old[i] = nil // release the dropped *timer for GC
	}
	w.pq = kept
	heap.Init(&w.pq)
}

// firedTimer is a due timer: the key plus the deadline it was scheduled for (so the
// detection is stamped at the moment the condition elapsed, not the watermark overshoot).
type firedTimer struct {
	key      SeriesKey
	deadline time.Time
}

// popDue returns the live timers whose deadline is <= wm, in deterministic deadline
// order, removing them from the wheel. Stale timers (superseded by reset/cancel) are
// discarded silently.
func (w *timerWheel) popDue(wm time.Time) []firedTimer {
	var fired []firedTimer
	for w.pq.Len() > 0 {
		top := w.pq[0]
		if top.deadline.After(wm) {
			break
		}
		heap.Pop(&w.pq)
		if top.gen != w.gens[top.key] {
			continue // stale: superseded by a later reset/cancel
		}
		fired = append(fired, firedTimer{key: top.key, deadline: top.deadline})
		// One-shot: firing consumes the live timer. BUMP (never delete) the gen so the value
		// is never recycled — a later schedule for this key gets a strictly higher gen, so no
		// stale heap entry can ever masquerade as live.
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

// snapshot captures only LIVE timers (gen == live gen) plus the generation table, in a
// stable order so the serialized bytes are deterministic.
func (w *timerWheel) snapshot() ([]snapTimer, []snapGen) {
	timers := make([]snapTimer, 0, w.pq.Len())
	for _, t := range w.pq {
		if t.gen == w.gens[t.key] {
			timers = append(timers, snapTimer{Deadline: t.deadline, Rule: t.key.Rule, Series: t.key.Series, Gen: t.gen})
		}
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
		heap.Push(&w.pq, &timer{deadline: t.Deadline, key: key, gen: t.Gen})
		w.live[key] = t.Deadline
	}
	return w
}
