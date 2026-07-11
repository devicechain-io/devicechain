// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"sort"
	"time"
)

// deltaState is the minimal running state a DeltaRate rule needs: the previous matching
// sample for a series. Two consecutive samples give a change and an elapsed time.
type deltaState struct {
	value float64
	time  time.Time
}

// applyDeltaRate evaluates a DeltaRate rule against one matching sample: the change since
// the previous sample (Rate ⇒ divided by the elapsed seconds) compared to the threshold.
// Level-triggered — it fires on every qualifying sample. The first sample for a series only
// primes the state; a non-advancing time gap suppresses a rate (division by ≤0) but still
// updates the last-sample so the series recovers on the next in-order event.
func (e *Engine) applyDeltaRate(ev Event, r Rule) {
	if !ev.Match {
		return
	}
	prev, ok := e.lastVal[ev.Key]
	if ok {
		// A strictly-earlier sample is stale — it must not become the comparison base and
		// rewind the running last-value. Rate mode additionally rejects an equal timestamp
		// (dt=0 has no defined rate); raw-delta mode accepts same-time readings.
		if r.Rate && !ev.Time.After(prev.time) {
			return
		}
		if !r.Rate && ev.Time.Before(prev.time) {
			return
		}
	}
	e.lastVal[ev.Key] = deltaState{value: ev.Value, time: ev.Time}
	if !ok {
		return // need two samples for a delta
	}
	q := ev.Value - prev.value
	if r.Rate {
		q /= ev.Time.Sub(prev.time).Seconds() // dt > 0, guaranteed by the advance guard above
	}
	if cmp(r.Op, q, r.Thresh) {
		e.emitValue(r, ev.Key, ev.Time, q) // q = the delta (raw) or rate the detection is about
	}
}

// sample is one (event-time, value) point retained inside a sliding window.
type sample struct {
	t time.Time
	v float64
}

// slidingState is a time-bounded sliding window of samples supporting O(1)-amortized
// running min/max via monotonic deques. buf is the authoritative window — kept sorted by
// event time (input is near-sorted under bounded lateness, so inserts are cheap) and used
// for eviction, sum/avg/count, and the snapshot. minDq/maxDq are derived acceleration
// (front = window min/max); they are rebuilt from buf whenever an out-of-order sample lands
// mid-buffer, so correctness never depends on the input being perfectly ordered. armed
// carries the edge-trigger: the rule fires once when the aggregate crosses into satisfaction
// and re-arms only after it falls back, so a sustained breach is one detection, not a flood.
type slidingState struct {
	buf   []sample
	sum   float64
	minDq []sample
	maxDq []sample
	armed bool
}

// insert adds a sample, keeping buf time-sorted. The common in-order case appends and
// extends the monotonic deques in O(1) amortized; a rare out-of-order sample is inserted
// at its sorted position and the deques are rebuilt from buf.
func (s *slidingState) insert(x sample) {
	s.sum += x.v
	n := len(s.buf)
	if n == 0 || !x.t.Before(s.buf[n-1].t) {
		s.buf = append(s.buf, x)
		s.pushDeques(x)
		return
	}
	i := sort.Search(n, func(i int) bool { return s.buf[i].t.After(x.t) })
	s.buf = append(s.buf, sample{})
	copy(s.buf[i+1:], s.buf[i:])
	s.buf[i] = x
	s.rebuildDeques()
}

// pushDeques extends the monotonic deques with x (already the latest by time).
func (s *slidingState) pushDeques(x sample) {
	for len(s.minDq) > 0 && s.minDq[len(s.minDq)-1].v >= x.v {
		s.minDq = s.minDq[:len(s.minDq)-1]
	}
	s.minDq = append(s.minDq, x)
	for len(s.maxDq) > 0 && s.maxDq[len(s.maxDq)-1].v <= x.v {
		s.maxDq = s.maxDq[:len(s.maxDq)-1]
	}
	s.maxDq = append(s.maxDq, x)
}

// rebuildDeques recomputes the monotonic deques from the (sorted) buffer.
func (s *slidingState) rebuildDeques() {
	s.minDq = s.minDq[:0]
	s.maxDq = s.maxDq[:0]
	for _, x := range s.buf {
		s.pushDeques(x)
	}
}

// evict drops every sample at or before cutoff. Because buf is time-sorted the deque fronts
// are the oldest by time too, so they evict by the same cutoff. Reslicing the front (rather
// than copying) is memory-bounded: every evict is paired with an insert, so append reclaims
// the dead prefix by reallocating once the backing array's tail is exhausted — peak memory
// stays ~2× the live window instead of leaking the whole history.
func (s *slidingState) evict(cutoff time.Time) {
	i := 0
	for i < len(s.buf) && !s.buf[i].t.After(cutoff) {
		s.sum -= s.buf[i].v
		i++
	}
	if i > 0 {
		s.buf = s.buf[i:]
	}
	for len(s.minDq) > 0 && !s.minDq[0].t.After(cutoff) {
		s.minDq = s.minDq[1:]
	}
	for len(s.maxDq) > 0 && !s.maxDq[0].t.After(cutoff) {
		s.maxDq = s.maxDq[1:]
	}
}

// satisfies reports whether the current (possibly empty) window meets the rule's Op vs
// Thresh. An empty window never satisfies — its min/max/sum are vacuously 0, which must not
// be read as a real breach (or, for LT rules, a spurious one).
func (s *slidingState) satisfies(r Rule) bool {
	return len(s.buf) > 0 && cmp(r.Op, s.value(r.Agg), r.Thresh)
}

// value returns the running aggregate over the current window.
func (s *slidingState) value(op AggOp) float64 {
	switch op {
	case AggCount:
		return float64(len(s.buf))
	case AggSum:
		return s.sum
	case AggAvg:
		if len(s.buf) == 0 {
			return 0
		}
		return s.sum / float64(len(s.buf))
	case AggMin:
		if len(s.minDq) == 0 {
			return 0
		}
		return s.minDq[0].v
	case AggMax:
		if len(s.maxDq) == 0 {
			return 0
		}
		return s.maxDq[0].v
	}
	return 0
}

// applySlidingAgg evaluates a SlidingAgg rule: fold the sample into the trailing window,
// then edge-trigger on the running aggregate crossing Op vs Thresh.
func (e *Engine) applySlidingAgg(ev Event, r Rule) {
	if !ev.Match {
		return
	}
	st := e.slides[ev.Key]
	if st == nil {
		st = &slidingState{}
		e.slides[ev.Key] = st
	}
	st.evict(ev.Time.Add(-r.Window))
	// Re-arm off the post-eviction window BEFORE folding in the new sample: the aggregate can
	// fall back to un-satisfied purely by samples aging out during a quiet gap (no low sample
	// ever arrives to clear the latch), so this is what makes the next breach a fresh crossing
	// instead of a swallowed one. Mirrors applyRepeating computing its count after eviction.
	if !st.satisfies(r) {
		st.armed = false
	}
	st.insert(sample{t: ev.Time, v: ev.Value})
	switch {
	case st.satisfies(r) && !st.armed:
		st.armed = true
		e.emitValue(r, ev.Key, ev.Time, st.value(r.Agg))
	case !st.satisfies(r):
		st.armed = false
	}
}

// --- snapshot / restore ---

type snapDelta struct {
	Rule   string    `json:"rule"`
	Series string    `json:"series"`
	Value  float64   `json:"value"`
	Time   time.Time `json:"time"`
}

type snapSlide struct {
	Rule   string      `json:"rule"`
	Series string      `json:"series"`
	Times  []time.Time `json:"times"`
	Values []float64   `json:"values"`
	Sum    float64     `json:"sum"`
	Armed  bool        `json:"armed"`
}

// sortByRuleSeries orders a snapshot slice by (rule, series) so the serialized bytes are
// deterministic — every keyed-state snapshot in this package iterates a Go map, whose order
// is randomized, so a stable sort is what makes the round-trip reproducible across replays.
func sortByRuleSeries[T any](s []T, key func(i int) (string, string)) {
	sort.Slice(s, func(i, j int) bool {
		ri, si := key(i)
		rj, sj := key(j)
		if ri != rj {
			return ri < rj
		}
		return si < sj
	})
}

func (e *Engine) snapshotDeltas() []snapDelta {
	out := make([]snapDelta, 0, len(e.lastVal))
	for k, d := range e.lastVal {
		out = append(out, snapDelta{Rule: k.Rule, Series: k.Series, Value: d.value, Time: d.time})
	}
	sortByRuleSeries(out, func(i int) (string, string) { return out[i].Rule, out[i].Series })
	return out
}

func (e *Engine) restoreDeltas(in []snapDelta) {
	for _, d := range in {
		e.lastVal[SeriesKey{Rule: d.Rule, Series: d.Series}] = deltaState{value: d.Value, time: d.Time}
	}
}

func (e *Engine) snapshotSlides() []snapSlide {
	out := make([]snapSlide, 0, len(e.slides))
	for k, st := range e.slides {
		times := make([]time.Time, len(st.buf))
		values := make([]float64, len(st.buf))
		for i, s := range st.buf {
			times[i], values[i] = s.t, s.v
		}
		out = append(out, snapSlide{Rule: k.Rule, Series: k.Series, Times: times, Values: values, Sum: st.sum, Armed: st.armed})
	}
	sortByRuleSeries(out, func(i int) (string, string) { return out[i].Rule, out[i].Series })
	return out
}

func (e *Engine) restoreSlides(in []snapSlide) {
	for _, s := range in {
		// Restore sum VERBATIM, not by re-summing the buffer: the live sum is an incremental
		// accumulation in arrival order (with subtract-on-evict residue), and float addition is
		// non-associative — re-deriving it in time order could differ in the last ulp and flip a
		// threshold sitting exactly on the aggregate, diverging the restored run from the clean one.
		st := &slidingState{armed: s.Armed, sum: s.Sum}
		for i := range s.Times {
			st.buf = append(st.buf, sample{t: s.Times[i], v: s.Values[i]})
		}
		st.rebuildDeques()
		e.slides[SeriesKey{Rule: s.Rule, Series: s.Series}] = st
	}
}
