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

// applyDeltaRate evaluates a DeltaRate rule against one sample: the change since the previous
// MATCHING sample (Rate ⇒ divided by the elapsed seconds) compared to the threshold. Edge-triggered
// (ADR-057): a qualifying delta/rate raises (latched), a subsequent qualifying sample back within
// threshold resolves, and a NON-matching sample (a filtering `when` leaf gone false) also resolves a
// raised alarm — so a filtering rule doesn't stay raised under active non-matching traffic. The first
// matching sample for a series only primes the state; a non-advancing time gap suppresses a rate
// (division by ≤0) but still updates the last-sample so the series recovers on the next in-order event.
func (e *Engine) applyDeltaRate(ev Event, r Rule) {
	if !ev.Match {
		// Falling edge (ADR-057 review D2/D5): a rule with a filtering `when` leaf that stops matching
		// resolves a raised alarm, instead of staying raised while the device actively reports
		// non-qualifying samples. Do NOT touch lastVal — the delta chain pairs consecutive MATCHING
		// samples, so a non-matching reading is not a base for the next delta. A no-op when never raised.
		e.resolve(r, ev.Key, ev.Time)
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
	// Level over time (ADR-057): the rising edge (a qualifying delta/rate) raises, a subsequent
	// qualifying sample whose delta/rate is back within threshold resolves. Observed per sample —
	// a series that stops reporting stays raised (Absence covers the went-dark case).
	if cmp(r.Op, q, r.Thresh) {
		e.emitValue(r, ev.Key, ev.Time, q) // q = the delta (raw) or rate the detection is about
	} else {
		e.resolve(r, ev.Key, ev.Time)
	}
}

// sample is one (event-time, value) point retained inside a sliding window.
type sample struct {
	t time.Time
	v float64
}

// slidingState is a time-bounded sliding window of samples supporting O(1)-amortized
// running min/max via monotonic deques. buf is the AUTHORITATIVE window — kept sorted by
// event time and used for eviction, sum/avg/count, and the snapshot. minDq/maxDq are derived
// acceleration (front = window min/max) and hold nothing buf does not. The edge-trigger latch
// is NOT held here: SlidingAgg raises and resolves through the engine's shared raised latch
// (ADR-057), the same one every alarm-bearing kind uses, so its rising edge is one detection
// and its falling edge emits a balancing Resolved.
//
// 🔴 `dirty` says the deques do not describe buf. The invariant that makes it safe is narrow
// and worth stating exactly: exactly ONE function TRUSTS a deque — value, for AggMin/AggMax —
// and it calls ensureDeques first. Everything else (evict, pushDeques, spliceDeque) only
// MAINTAINS them, and maintenance of a dirty deque is thrown away wholesale by the rebuild
// that ensureDeques owes. Anything added here that reads a deque to ANSWER something, rather
// than to update it, must go through ensureDeques.
type slidingState struct {
	// 🔴 buf IS NOT THE WINDOW. The window is buf AND late — TWO independently time-sorted
	// runs — and where they overlap in time, buf comes first. Anything that needs the window
	// as one ordered run calls mergeLate; there are exactly two such places and both are
	// stated at mergeLate. Everything else on the per-event path (eviction, the count, the
	// sum, both deque updates) works on the two runs as they stand, which is the whole
	// reason the overlay exists.
	buf  []sample
	late []sample
	sum  float64

	minDq []sample
	maxDq []sample
	// dirty is set only by restoreSlides: a restore hands over a whole buffer at once, so
	// there is no incremental splice to make, and rebuilding every restored series up front
	// is work a sum/avg/count rule never reads.
	dirty bool
}

// count is the window's size — the thing len(buf) used to be, and the reason every
// len(s.buf) in this file became a call. A reader that asks buf alone silently answers
// for part of the window.
func (s *slidingState) count() int { return len(s.buf) + len(s.late) }

// minOverlayMerge floors the overlay so a tiny window is not merged every other sample.
// Below it the merge is cheaper than the bookkeeping to avoid it.
const minOverlayMerge = 32

// insert adds a sample. Out-of-order is ORDINARY, not rare, and the comment here used to say
// the opposite: per-sample timestamps are legal on the JSON transports, so a store-and-forward
// upload presents a batch in whatever order the device buffered it. This runs on the single
// DETECT goroutine every tenant shares, so anything O(window) per sample is quadratic over one
// message and parks the loop for every tenant on the instance.
//
// 🔴 THE TWO SIZES ARE INDEPENDENT AND ONLY ONE OF THEM IS BOUNDED. The entries in a message
// are capped at ingest; the window DEPTH is not — a 24h rule on a 1 Hz series holds ~86 400
// samples. So "cheap per batch" is not the property to aim for; "independent of window depth"
// is. Two things here were O(depth) per sample and each needed its own answer:
//
//   - the monotonic deques, which were REBUILT from the whole window. They are now SPLICED —
//     same deque, O(log n) plus one memmove (spliceDeque).
//   - keeping one sorted slice, which costs a memmove of the whole tail. That one cannot be
//     fixed per-insert, because a sorted slice IS a memmove per insert. It is fixed per BATCH
//     instead: an out-of-order sample lands in the small `late` overlay and the runs are
//     folded together once, later (mergeLate). The same "owe the work" shape as dirty.
//
// 🔑 THE OVERLAY INVARIANT, which this function both relies on and maintains: every sample in
// `late` is strictly OLDER than buf's last, and buf is empty only when late is too. It holds
// because insertLate is reached only for a sample behind buf's last; because buf's last only
// moves forward; and because eviction cannot empty buf while sparing late — a cutoff that
// clears buf's last clears everything older, which is all of late. TestOverlayInvariant pins
// it. The consequence used here: a sample that may be APPENDED to buf is thereby at or after
// EVERYTHING held, so the deques can simply be extended. Nothing else needs checking.
func (s *slidingState) insert(x sample) {
	s.sum += x.v
	if n := len(s.buf); n == 0 || !x.t.Before(s.buf[n-1].t) {
		s.buf = append(s.buf, x)
		s.pushDeques(x)
		return
	}
	s.insertLate(x)
	s.minDq = spliceDeque(s.minDq, x, func(a, b float64) bool { return a <= b })
	s.maxDq = spliceDeque(s.maxDq, x, func(a, b float64) bool { return a >= b })
}

// insertLate puts an out-of-order sample into the overlay at its sorted position, then folds
// the overlay in once it has grown enough to be worth folding.
//
// The threshold is the break-even between the two costs it trades. Carrying an overlay of L
// costs a memmove of ~L per late sample; folding it in costs a pass of len(buf). Those are
// equal around L = sqrt(len(buf)), which is what the multiply says without a sqrt — so k late
// samples into a window of W cost O(k·sqrt(W)) rather than O(k·W). For a 1000-entry message
// into an 86 400-sample window that is the difference between ~0.4M and ~43M sample moves.
func (s *slidingState) insertLate(x sample) {
	j := sort.Search(len(s.late), func(i int) bool { return s.late[i].t.After(x.t) })
	s.late = append(s.late, sample{})
	copy(s.late[j+1:], s.late[j:])
	s.late[j] = x
	if len(s.late) >= minOverlayMerge && len(s.late)*len(s.late) >= len(s.buf) {
		s.mergeLate()
	}
}

// mergeLate folds the overlay into buf, leaving one sorted run.
//
// 🔴 ONLY TWO CALLERS NEED IT, and naming them is the whole invariant: rebuildDeques and
// snapshotSlides, the only two places that consume the window AS ONE ORDERED SEQUENCE. Both
// are off the per-event path (a restore, a checkpoint). Eviction, the count, the sum and both
// deque updates deliberately do NOT merge — making eviction merge would put the O(depth) step
// back on every single event, which is exactly the cost the overlay removes.
//
// 🔴 buf WINS A TIE, and that is not arbitrary. It is the order every incremental deque update
// already assumed: spliceDeque places a sample after every entry at or before its own time, so
// a late sample sharing an instant with one already held sorts AFTER it. Reverse this and the
// deques stop matching a rebuild — silently, and only for equal timestamps.
//
// It merges backwards in place rather than into a fresh slice: a fresh one would allocate the
// whole window on every merge, which on a deep window is the GC pressure this was meant to avoid.
func (s *slidingState) mergeLate() {
	m := len(s.late)
	if m == 0 {
		return
	}
	n := len(s.buf)
	s.buf = append(s.buf, s.late...) // grow first; the tail is overwritten below
	i, j, w := n-1, m-1, n+m-1
	for j >= 0 {
		// Scanning backwards, the tie goes to LATE — which is buf-first read forwards.
		if i >= 0 && s.late[j].t.Before(s.buf[i].t) {
			s.buf[w] = s.buf[i]
			i--
		} else {
			s.buf[w] = s.late[j]
			j--
		}
		w--
	}
	s.late = s.late[:0]
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

// spliceDeque folds a sample that landed MID-BUFFER into one monotonic deque, producing exactly
// the deque a full rebuild from buf would have produced.
//
// `dominates(a, b)` reports whether a later entry valued a makes an earlier entry valued b
// redundant: a <= b for the min deque, a >= b for the max deque. An entry is in the deque iff no
// later entry dominates it, which is why the min deque's values strictly INCREASE with index and
// the max deque's strictly DECREASE — and that monotonicity is what makes both searches binary.
//
// Inserting x splits the deque at p, its own position in time, into three regions:
//
//   - after p — their suffix in buf is unchanged, so their membership is unchanged. Untouched.
//   - x itself — in iff nothing after it dominates it. d[p] is the front-most survivor of that
//     suffix and therefore its extreme, so testing d[p] alone tests the whole suffix.
//   - before p — out iff x dominates them. Values are monotone in index, so those form one
//     contiguous run [q, p) that x replaces.
//
// If x does not survive, [q, p) is necessarily empty: an earlier entry x dominated would be
// dominated by d[p] as well and could not have been in the deque to begin with. Returning
// early is that fact, not a shortcut.
func spliceDeque(d []sample, x sample, dominates func(a, b float64) bool) []sample {
	p := sort.Search(len(d), func(i int) bool { return d[i].t.After(x.t) })
	if p < len(d) && dominates(d[p].v, x.v) {
		return d
	}
	q := sort.Search(p, func(i int) bool { return dominates(x.v, d[i].v) })
	if q == p { // x displaces nothing — open a slot for it
		d = append(d, sample{})
		copy(d[q+1:], d[q:])
	} else { // x replaces the run [q, p) — one entry in place, more than one closes a gap
		copy(d[q+1:], d[p:])
		d = d[:len(d)-(p-q-1)]
	}
	d[q] = x
	return d
}

// rebuildDeques recomputes the monotonic deques from the window, clearing dirty. It consumes
// the window as one ordered sequence, so it folds the overlay in first — one of the only two
// places that has to.
func (s *slidingState) rebuildDeques() {
	s.mergeLate()
	s.minDq = s.minDq[:0]
	s.maxDq = s.maxDq[:0]
	for _, x := range s.buf {
		s.pushDeques(x)
	}
	s.dirty = false
}

// ensureDeques makes minDq/maxDq describe buf. Every read of a deque goes through it.
func (s *slidingState) ensureDeques() {
	if s.dirty {
		s.rebuildDeques()
	}
}

// evict drops every sample at or before cutoff. Because buf is time-sorted the deque fronts
// are the oldest by time too, so they evict by the same cutoff. Reslicing the front (rather
// than copying) is memory-bounded: every evict is paired with an insert, so append reclaims
// the dead prefix by reallocating once the backing array's tail is exhausted — peak memory
// stays ~2× the live window instead of leaking the whole history.
//
// It does NOT call ensureDeques: it only maintains the deques, it never answers from them, and
// a dirty deque's trimming is discarded by the rebuild it already owes. Nor does it merge the
// overlay — the two runs are each time-sorted, so each has its own evictable PREFIX and both
// are trimmed independently. That is what keeps eviction O(dropped) instead of O(depth), and
// it is the reason the overlay can exist at all: eviction runs on every single event, so a
// merge here would have put the whole cost straight back.
func (s *slidingState) evict(cutoff time.Time) {
	s.buf = s.evictRun(s.buf, cutoff)
	s.late = s.evictRun(s.late, cutoff)
	for len(s.minDq) > 0 && !s.minDq[0].t.After(cutoff) {
		s.minDq = s.minDq[1:]
	}
	for len(s.maxDq) > 0 && !s.maxDq[0].t.After(cutoff) {
		s.maxDq = s.maxDq[1:]
	}
}

// evictRun drops the expired prefix of one time-sorted run, subtracting what it drops from
// the running sum. Reslicing rather than copying is what the memory note above describes.
func (s *slidingState) evictRun(run []sample, cutoff time.Time) []sample {
	i := 0
	for i < len(run) && !run[i].t.After(cutoff) {
		s.sum -= run[i].v
		i++
	}
	return run[i:]
}

// satisfies reports whether the current (possibly empty) window meets the rule's Op vs
// Thresh. An empty window never satisfies — its min/max/sum are vacuously 0, which must not
// be read as a real breach (or, for LT rules, a spurious one).
func (s *slidingState) satisfies(r Rule) bool {
	return s.count() > 0 && cmp(r.Op, s.value(r.Agg), r.Thresh)
}

// value returns the running aggregate over the current window.
func (s *slidingState) value(op AggOp) float64 {
	switch op {
	case AggCount:
		return float64(s.count())
	case AggSum:
		return s.sum
	case AggAvg:
		if s.count() == 0 {
			return 0
		}
		return s.sum / float64(s.count())
	case AggMin:
		s.ensureDeques()
		if len(s.minDq) == 0 {
			return 0
		}
		return s.minDq[0].v
	case AggMax:
		s.ensureDeques()
		if len(s.maxDq) == 0 {
			return 0
		}
		return s.maxDq[0].v
	}
	return 0
}

// applySlidingAgg evaluates a SlidingAgg rule: fold the sample into the trailing window,
// then edge-trigger on the running aggregate crossing Op vs Thresh.
//
// Eviction advances on EVERY delivered event (ADR-057 review D2/D5); only a matching sample is
// folded in. A rule with a filtering `when` leaf therefore observes its falling edge while the
// device keeps reporting NON-matching samples — the qualifying samples age out and the aggregate
// stops satisfying — rather than staying raised until the next match. For a match-every rule (the
// common case) every sample folds in, so this is byte-identical to always-insert.
func (e *Engine) applySlidingAgg(ev Event, r Rule) {
	cutoff := e.windowCutoff(ev.Time, r.Window)
	folds := e.foldsIn(ev, cutoff)
	st := e.slides[ev.Key]
	if st == nil {
		if !folds {
			// No window yet, and nothing this sample may open one with: a non-matching sample opens
			// none (nothing to evict or resolve), and neither does a late one — its own window has
			// already passed the frontier, so an entry made from it would be evicted unread.
			return
		}
		st = &slidingState{}
		e.slides[ev.Key] = st
	}
	st.evict(cutoff)
	if folds {
		st.insert(sample{t: ev.Time, v: ev.Value})
	}
	// Evaluate the level ONCE, on the fully-updated trailing window (evicted + this sample folded
	// in when matching). The RISING edge is gated on ev.Match — symmetric with Repeating/Correlation,
	// whose rising edge is structurally match-only: eviction can push a SlidingAgg aggregate EITHER
	// direction (e.g. an AggMax/GT or AggAvg/LT window can cross INTO satisfaction as an old sample
	// expires), so without this gate a non-matching sample could RAISE purely by aging the window —
	// which is both surprising and ragged (unreachable once the window empties and the entry is
	// deleted, review by both 6d-pre-2a lenses). A non-match therefore only ever ages the window
	// toward its FALLING edge; a rise that eviction alone would produce is deferred to the next
	// matching sample. Evaluating only the post-insert window (not a pre-insert check) is the review-D1
	// fix: a pre-insert dip at the instant the left-edge sample expires as the new one arrives exists
	// at no real instant and would flap the alarm on every regular-cadence sample. A breach that ends
	// purely because the window emptied during a silent GAP is not observed until the next event, like
	// every other event-driven kind (see the package silence note) — raised across the gap, not flapping.
	switch {
	case st.satisfies(r):
		if ev.Match {
			e.emitValue(r, ev.Key, ev.Time, st.value(r.Agg))
		}
	default:
		e.resolve(r, ev.Key, ev.Time)
	}
	if st.count() == 0 {
		delete(e.slides, ev.Key) // a fully-aged-out window leaks no state entry against the budget
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
		// The snapshot is the window as one ordered sequence — the second and last place
		// that needs the overlay folded in (see mergeLate). Restoring an unmerged pair as
		// one array would silently reorder the window.
		//
		// 🔴 IT MERGES THE LIVE STATE, AND THAT WRITE IS LOAD-BEARING. Folding into a copy
		// instead looks like the tidier thing — a snapshot has no business mutating what it
		// reads — and it would leave the live engine holding TWO runs where the restored one
		// holds ONE. evictRun subtracts per run, so the two would then evict in different
		// ORDERS, and float subtraction is not associative: the same window answers a sum
		// differing in the last ulp, which is enough to flip a threshold sitting exactly on
		// an AggSum or AggAvg. That is the divergence restoreSlides' own comment below
		// exists to prevent; it is prevented here, by snapshot == live.
		st.mergeLate()
		times := make([]time.Time, len(st.buf))
		values := make([]float64, len(st.buf))
		for i, s := range st.buf {
			times[i], values[i] = s.t, s.v
		}
		out = append(out, snapSlide{Rule: k.Rule, Series: k.Series, Times: times, Values: values, Sum: st.sum})
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
		// Deques deferred, not skipped. A restore hands over a whole buffer at once, so there is
		// no incremental splice to make and rebuilding is the only option — but it is O(len(buf))
		// per series across every restored series, and a sum/avg/count rule never reads a deque,
		// so the answer is to owe the work rather than to do it. dirty is the same flag insert
		// honours, and ensureDeques pays the debt on the first min/max read.
		st := &slidingState{sum: s.Sum, dirty: true}
		for i := range s.Times {
			st.buf = append(st.buf, sample{t: s.Times[i], v: s.Values[i]})
		}
		e.slides[SeriesKey{Rule: s.Rule, Series: s.Series}] = st
	}
}
