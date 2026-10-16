// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"container/heap"
	"sort"
	"time"
)

// The sample-pairing kinds — DeltaRate (each sample against the one before it) and CountWindow
// (every N samples as one group) — are defined over a series' samples in EVENT-TIME order. They
// used to evaluate on arrival, which made the pair and the group a function of the order the
// samples reached the engine: a reordered message, a redelivery, N inbound readers, a batched
// upload — each of these presented the same samples differently and got different detections.
// The windowed kinds converge regardless (a window is a set); these two do not, because a pair
// computed against the wrong neighbour is a different VALUE and a group cut at the wrong sample
// is a different aggregate.
//
// So they are now evaluated the way Aggregate already is: a sample is HELD until the frontier
// says no earlier sample can still arrive, and only then applied, in time order. The run below
// is that holding area. It is keyed state like every other map on the engine — snapshotted,
// restored, sharded by key, swept on rule removal and descope — so a restart or a lease
// handover resumes it, and replay re-derives the same flushes from the same frontier sequence.
//
// FINALITY is "the frontier is strictly past the sample" (t < wm.now). A sample AT the frontier is
// still open, which is the convention every watermark-driven kind already has: a pane [s, e)
// stays open while wm < e, and a sample at exactly wm has e > wm. With lateness 0 this means a
// sample is applied on the next advance — the next message, or an idle advance — rather than on
// its own message; it is stamped at its own time either way.
//
// LATENESS. A sample older than the series' newest FINAL sample speaks to pairs and groups that
// are already decided (hold). The disposition is the one applyDuration and foldsIn already make,
// not a new one: a MATCH is refused and counted in lateSamples; a DeltaRate NON-MATCH is a falling
// edge and goes to resolve, which refuses one older than the raise (the D3/F2 stale guard) and
// otherwise clears the alarm — the same reasoning as Duration's late break, that refusing it
// would make an alarm immortal for a device whose readings always arrive that late.
//
// ORDER WITHIN A RUN is total over the sample's own contents, (time, match, value), so two
// samples at one instant finalize in the same order whatever order they arrived in. That is what
// makes the output a function of the sample SET. A rate-mode DeltaRate still skips an
// equal-time sample (dt = 0 has no rate); a raw delta still accepts it.
//
// MEMORY. A run holds the samples inside the lateness budget of its own series — at the 5 s
// default, a 1 Hz series holds about five, plus whatever one batched message carries. The
// processor clamps far-future event times, so a device clock cannot stretch the frontier. The
// samples are counted in RetainedSampleCounts and the run in LiveKeyCounts, so the per-tenant
// state budget sees them; there is no separate hard cap, as there is none for the sliding kinds.

// pendingSample is one admitted-but-not-final sample of a sample-pairing kind.
type pendingSample struct {
	t     time.Time
	v     float64
	match bool
}

// before is the total order a run is kept in: time, then non-match before match, then value.
// The second key puts a falling edge ahead of a rising edge at the same instant — the order a
// level kind would observe them in if the device reported the non-match first — and the third
// exists only so the order is total; nothing reads a meaning into it.
func (a pendingSample) before(b pendingSample) bool {
	if !a.t.Equal(b.t) {
		return a.t.Before(b.t)
	}
	if a.match != b.match {
		return !a.match
	}
	return a.v < b.v
}

// pendingRun is one series' held samples, sorted by pendingSample.before, plus its slot in the
// engine's flush heap (one entry per run, keyed by the run's earliest sample).
type pendingRun struct {
	key     SeriesKey
	samples []pendingSample
	index   int
}

// pendHeap orders runs by their earliest held sample, then by key, so a flush at one frontier
// applies runs in a deterministic order across replays — the closeHeap discipline.
type pendHeap []*pendingRun

func (h pendHeap) Len() int { return len(h) }
func (h pendHeap) Less(i, j int) bool {
	a, b := h[i].samples[0].t, h[j].samples[0].t
	if !a.Equal(b) {
		return a.Before(b)
	}
	if h[i].key.Rule != h[j].key.Rule {
		return h[i].key.Rule < h[j].key.Rule
	}
	return h[i].key.Series < h[j].key.Series
}
func (h pendHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *pendHeap) Push(x any) {
	r := x.(*pendingRun)
	r.index = len(*h)
	*h = append(*h, r)
}
func (h *pendHeap) Pop() any {
	old := *h
	n := len(old)
	r := old[n-1]
	old[n-1] = nil
	r.index = -1
	*h = old[:n-1]
	return r
}

// removeRun takes one run out of the heap, if it is in it.
func (h *pendHeap) removeRun(r *pendingRun) {
	if r.index >= 0 && r.index < len(*h) && (*h)[r.index] == r {
		heap.Remove(h, r.index)
	}
}

// purgeMatching drops every run whose rule the predicate selects, reporting how many. Like
// closeHeap.purgeMatching, it rebuilds the heap only when something was dropped.
func (h *pendHeap) purgeMatching(match func(ruleID string) bool) int {
	old := *h
	kept := old[:0]
	for _, r := range old {
		if !match(r.key.Rule) {
			r.index = len(kept)
			kept = append(kept, r)
		}
	}
	dropped := len(old) - len(kept)
	if dropped == 0 {
		return 0
	}
	for i := len(kept); i < len(old); i++ {
		old[i] = nil
	}
	*h = kept
	heap.Init(h)
	return dropped
}

// hold admits one sample of a sample-pairing kind into its series' run, or refuses it as late.
//
// The late test is PER SERIES, not the frontier: a sample is late when it is older than the
// series' newest FINAL sample — the pairs and groups up to that sample are decided, so there is
// no position left for it. That is the rule-sized budget every other kind uses (a pane's window,
// a session's gap, a duration's hold) rather than the raw lateness tolerance, and for the reason
// applyDuration gives: a strict "behind the frontier is late" would refuse every reading of a
// device whose clock runs slower than the fleet by more than the tolerance, and most of every
// batched upload. Between the series' final sample and the frontier a sample is still placed by
// its own time — exactly, if no later sample of the series has been finalized since, which is
// what the lateness budget guarantees and what beyond it is best effort.
//
// A refused match is counted in lateSamples. A refused DeltaRate non-match is a falling edge and
// goes to resolve, which refuses one older than the raise and otherwise clears the alarm. Only a
// matching sample is held for CountWindow: the kind ignores a non-match entirely.
func (e *Engine) hold(ev Event, r Rule) bool {
	if !ev.Match && r.Kind == CountWindow {
		return false
	}
	if final, ok := e.finalThrough(ev.Key, r); ok && ev.Time.Before(final) {
		if ev.Match {
			e.lateSamples++ // older than the series' final sample: its pair or group is already decided
		} else {
			e.resolve(r, ev.Key, ev.Time) // a late falling edge still clears an alarm it postdates
		}
		return false
	}
	x := pendingSample{t: ev.Time, v: ev.Value, match: ev.Match}
	run := e.pending[ev.Key]
	if run == nil {
		run = &pendingRun{key: ev.Key, samples: []pendingSample{x}, index: -1}
		e.pending[ev.Key] = run
		heap.Push(&e.pendHeap, run)
		return true
	}
	n := len(run.samples)
	if !x.before(run.samples[n-1]) {
		run.samples = append(run.samples, x) // in order: the common path, no search and no move
		return true
	}
	j := sort.Search(n, func(i int) bool { return x.before(run.samples[i]) })
	run.samples = append(run.samples, pendingSample{})
	copy(run.samples[j+1:], run.samples[j:])
	run.samples[j] = x
	if j == 0 {
		heap.Fix(&e.pendHeap, run.index) // the run's earliest sample moved
	}
	return true
}

// finalThrough is the time of the series' newest FINAL sample under r, if it has one: DeltaRate's
// base, CountWindow's last folded sample. Everything at or before it is decided.
func (e *Engine) finalThrough(key SeriesKey, r Rule) (time.Time, bool) {
	switch r.Kind {
	case DeltaRate:
		if d, ok := e.lastVal[key]; ok {
			return d.time, true
		}
	case CountWindow:
		if c, ok := e.counts[key]; ok {
			return c.last, true
		}
	}
	return time.Time{}, false
}

// flushPending applies, in order, every held sample the frontier is now strictly past, run by
// run in heap order. A run with samples still open goes back into the heap under its new
// earliest; an emptied run is released so it holds no entry against the budget.
func (e *Engine) flushPending() {
	for e.pendHeap.Len() > 0 {
		top := e.pendHeap[0]
		if !top.samples[0].t.Before(e.wm.now) {
			break
		}
		heap.Pop(&e.pendHeap)
		r, known := e.rules[top.key.Rule]
		i := 0
		for i < len(top.samples) && top.samples[i].t.Before(e.wm.now) {
			if known {
				e.finalize(top.key, r, top.samples[i])
			}
			i++
		}
		if i == len(top.samples) {
			delete(e.pending, top.key)
			continue
		}
		// Reslice rather than copy: hold pairs every release with an append, which reclaims the
		// dead prefix on reallocation — the same memory argument as slidingState.evict.
		top.samples = top.samples[i:]
		heap.Push(&e.pendHeap, top)
	}
}

// finalize applies one now-final sample to its kind's running state — the evaluation the kind
// used to make on arrival, now made in event-time order.
func (e *Engine) finalize(key SeriesKey, r Rule, s pendingSample) {
	switch r.Kind {
	case DeltaRate:
		e.finalizeDeltaRate(key, r, s)
	case CountWindow:
		e.finalizeCountWindow(key, r, s)
	}
}

// dropPendingRun releases one series' run, if any, reporting whether there was one.
func (e *Engine) dropPendingRun(key SeriesKey) bool {
	run, ok := e.pending[key]
	if !ok {
		return false
	}
	e.pendHeap.removeRun(run)
	delete(e.pending, key)
	return true
}

// --- snapshot / restore ---

// snapPending persists one series' held samples in run order. Times, Values and Matches are
// parallel, the shape snapSlide already uses.
type snapPending struct {
	Rule    string      `json:"rule"`
	Series  string      `json:"series"`
	Times   []time.Time `json:"times"`
	Values  []float64   `json:"values"`
	Matches []bool      `json:"matches"`
}

func (e *Engine) snapshotPending() []snapPending {
	out := make([]snapPending, 0, len(e.pending))
	for k, run := range e.pending {
		n := len(run.samples)
		s := snapPending{Rule: k.Rule, Series: k.Series, Times: make([]time.Time, n), Values: make([]float64, n), Matches: make([]bool, n)}
		for i, x := range run.samples {
			s.Times[i], s.Values[i], s.Matches[i] = x.t, x.v, x.match
		}
		out = append(out, s)
	}
	sortByRuleSeries(out, func(i int) (string, string) { return out[i].Rule, out[i].Series })
	return out
}

// restorePending rebuilds the runs and the flush heap. A run is re-sorted on the way in rather
// than trusted: the order is a function of the samples, so this costs nothing when the snapshot
// is canonical and corrects one that is not.
func (e *Engine) restorePending(in []snapPending) {
	for _, s := range in {
		if len(s.Times) == 0 {
			continue
		}
		run := &pendingRun{key: SeriesKey{Rule: s.Rule, Series: s.Series}, index: -1}
		for i := range s.Times {
			x := pendingSample{t: s.Times[i]}
			if i < len(s.Values) {
				x.v = s.Values[i]
			}
			if i < len(s.Matches) {
				x.match = s.Matches[i]
			}
			run.samples = append(run.samples, x)
		}
		sort.SliceStable(run.samples, func(i, j int) bool { return run.samples[i].before(run.samples[j]) })
		e.pending[run.key] = run
		heap.Push(&e.pendHeap, run)
	}
}
