// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"container/heap"
	"sort"
	"time"
)

// AggOp is a windowed aggregate function.
type AggOp int

const (
	AggCount AggOp = iota // number of matching events in the window
	AggSum
	AggAvg
	AggMin
	AggMax
)

// CmpOp compares an aggregate value against a rule threshold.
type CmpOp int

const (
	GT CmpOp = iota
	GE
	LT
	LE
)

func cmp(op CmpOp, a, b float64) bool {
	switch op {
	case GT:
		return a > b
	case GE:
		return a >= b
	case LT:
		return a < b
	case LE:
		return a <= b
	}
	return false
}

// paneAgg is the partial aggregate accumulated for one tumbling window pane.
type paneAgg struct {
	count    int
	sum      float64
	min, max float64
}

func (p *paneAgg) add(v float64) {
	if p.count == 0 {
		p.min, p.max = v, v
	} else {
		if v < p.min {
			p.min = v
		}
		if v > p.max {
			p.max = v
		}
	}
	p.count++
	p.sum += v
}

func (p *paneAgg) value(op AggOp) float64 {
	switch op {
	case AggCount:
		return float64(p.count)
	case AggSum:
		return p.sum
	case AggAvg:
		if p.count == 0 {
			return 0
		}
		return p.sum / float64(p.count)
	case AggMin:
		return p.min
	case AggMax:
		return p.max
	}
	return 0
}

// paneKey identifies one tumbling window pane: a (rule, series) plus the window start
// (unix-nanos, so it is a comparable map key and survives serialization exactly).
type paneKey struct {
	Rule   string
	Series string
	Start  int64
}

// closeItem is a pending window close, min-heap-ordered by end then key so that panes
// closing at the same watermark fire in a deterministic order across replays.
type closeItem struct {
	end   time.Time
	pk    paneKey
	index int
}

type closeHeap []*closeItem

func (h closeHeap) Len() int { return len(h) }
func (h closeHeap) Less(i, j int) bool {
	if !h[i].end.Equal(h[j].end) {
		return h[i].end.Before(h[j].end)
	}
	if h[i].pk.Rule != h[j].pk.Rule {
		return h[i].pk.Rule < h[j].pk.Rule
	}
	if h[i].pk.Series != h[j].pk.Series {
		return h[i].pk.Series < h[j].pk.Series
	}
	return h[i].pk.Start < h[j].pk.Start
}
func (h closeHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *closeHeap) Push(x any) {
	c := x.(*closeItem)
	c.index = len(*h)
	*h = append(*h, c)
}
func (h *closeHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return c
}

// purgeRule drops every pending pane close belonging to rule id and re-establishes the
// heap invariant (ADR-051 slice 4b-3 rule removal). closePanes already tolerates a
// missing pane, so a leaked close item is only wasted memory rather than a wrong firing —
// but a removed rule must leave nothing behind. Indices are reset before heap.Init so the
// heap bookkeeping stays consistent.
func (h *closeHeap) purgeRule(id string) {
	old := *h
	kept := old[:0]
	for _, c := range old {
		if c.pk.Rule != id {
			c.index = len(kept)
			kept = append(kept, c)
		}
	}
	for i := len(kept); i < len(old); i++ {
		old[i] = nil // release the dropped *closeItem for GC
	}
	*h = kept
	heap.Init(h)
}

// applyRepeating handles a Repeating rule: keep a sliding buffer of matching-event times
// within Window, and fire on the rising edge where the trailing count reaches Count.
//
// Eviction advances on EVERY delivered event (matching or not, ADR-057 review D2/D5); only a
// matching event is appended to the buffer. A rule with a filtering `when` leaf therefore
// observes its falling edge while the device keeps reporting NON-matching gate-metric values —
// the burst ages out of the window and the count drops below N — rather than staying raised until
// some future matching event. For a match-every rule (the common case) every event matches, so
// this is byte-identical to always-append. A fully silent series still stays raised until it
// reports again (the window kinds are event-driven; see the package silence note).
func (e *Engine) applyRepeating(ev Event, r Rule) {
	cutoff := ev.Time.Add(-r.Window)
	buf := e.sliding[ev.Key]
	kept := make([]time.Time, 0, len(buf)+1)
	for _, ts := range buf {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	prev := len(kept)
	if ev.Match {
		kept = append(kept, ev.Time)
	}
	if len(kept) == 0 {
		delete(e.sliding, ev.Key) // an emptied window leaks no state entry against the budget
	} else {
		e.sliding[ev.Key] = kept
	}
	switch {
	case prev < r.Count && len(kept) >= r.Count:
		e.emitSample(r, ev) // rising edge: the sample that completed the N-in-window run (none for a metric-less leaf)
	case len(kept) < r.Count:
		// Falling edge (ADR-057): enough prior matches aged out of the window that the count is back
		// below N — the burst has passed, so resolve a raised alarm. A no-op when never raised. The
		// rising case can only fire on a matching event (a non-match never grows kept), so a
		// non-matching event only ever ages the window down toward this falling edge.
		e.resolve(r, ev.Key, ev.Time)
	}
}

// applyAggregate handles an Aggregate rule: fold each matching value into its tumbling
// window pane; the pane fires (or not) when it closes on the watermark (closePanes).
//
// A matching event always opens (or finds) its window's pane and folds its value. A NON-matching
// event opens the pane too — but ONLY while an alarm is currently raised for this series, and folds
// no value (ADR-057 review D2/D5). Without this, an all-non-matching window never opens a pane, so it
// never closes, so a raised alarm stays raised forever under active non-matching traffic — the exact
// stuck-raise the sliding kinds close. Gating the empty-pane open on `raised` bounds the extra state
// to series that actually need the falling edge (a raise implies a prior match); an empty pane
// never satisfies (closePanes), so it resolves. A non-matching event for a NON-raised series opens
// nothing (no alarm to resolve, no value to fold).
func (e *Engine) applyAggregate(ev Event, r Rule) {
	_, raised := e.raised[ev.Key]
	if !ev.Match && !raised {
		return
	}
	start := ev.Time.Truncate(r.Window)
	end := start.Add(r.Window)
	if !end.After(e.wm.now) {
		return // window already closed past the lateness bound: drop the late event
	}
	pk := paneKey{Rule: r.ID, Series: ev.Key.Series, Start: start.UnixNano()}
	pa, ok := e.panes[pk]
	if !ok {
		pa = &paneAgg{}
		e.panes[pk] = pa
		heap.Push(&e.closes, &closeItem{end: end, pk: pk})
	}
	if ev.Match {
		pa.add(ev.Value)
	}
}

// applyCountWindow handles a CountWindow rule: fold each matching value into an event-count
// accumulator; when the Count-th event lands, evaluate Agg vs Thresh, emit on satisfaction,
// and reset. It is a tumbling window measured in events rather than time — no watermark, no
// timer — so replay just re-counts from the snapshotted partial accumulator.
//
// KNOWN RESIDUAL (ADR-057 review D2/D5, inherent — not the sliding-kind gap): the window is counted
// in MATCHING events, and there is no time axis, so a filtering rule that stops matching never
// completes another window — a raised alarm then stays raised until Count more matching events arrive
// (which may be never). Unlike the sliding/tumbling-TIME kinds, there is no eviction or watermark that
// could observe the falling edge from non-matching traffic: "closed" is definitionally Count matches.
// Operators pair such a rule with an Absence rule when "stopped producing matches" must also clear the
// alarm — the same mitigation the package silence note gives for a fully-silent series.
func (e *Engine) applyCountWindow(ev Event, r Rule) {
	if !ev.Match || r.Count <= 0 {
		return
	}
	pa := e.counts[ev.Key]
	if pa == nil {
		pa = &paneAgg{}
		e.counts[ev.Key] = pa
	}
	pa.add(ev.Value)
	if pa.count >= r.Count {
		// The count-window closed: a satisfied close raises (latched across successive windows), an
		// unsatisfied close resolves a prior raise (ADR-057 — the latest N-event window no longer
		// breaches). Reset either way.
		if cmp(r.Op, pa.value(r.Agg), r.Thresh) {
			e.emitValue(r, ev.Key, ev.Time, pa.value(r.Agg))
		} else {
			e.resolve(r, ev.Key, ev.Time)
		}
		delete(e.counts, ev.Key)
	}
}

// closePanes fires every tumbling window whose end has been crossed by the watermark.
// closePanes closes every tumbling pane whose end is at or before the watermark, emitting a
// detection for each that satisfies its comparison. It reports whether it consumed any pane —
// including a pane that closed WITHOUT emitting (its aggregate failed the comparison): that is
// still a serializable-state change the caller must persist, so a wall-clock idle advance that
// silently closes a pane is checkpointed and cannot diverge on replay (ADR-051 slice 4c).
func (e *Engine) closePanes(wm time.Time) bool {
	consumed := false
	for e.closes.Len() > 0 {
		top := e.closes[0]
		if top.end.After(wm) {
			break
		}
		heap.Pop(&e.closes)
		consumed = true
		pa, ok := e.panes[top.pk]
		if !ok {
			continue
		}
		// A satisfied close raises (latched across successive tumbling windows for this series), an
		// unsatisfied close resolves a prior raise (ADR-057 falling edge). An EMPTY pane (count 0) never
		// satisfies — mirroring slidingState.satisfies, so an all-non-matching window opened only to
		// carry the falling edge (applyAggregate, while raised) resolves rather than reading the zero-
		// value aggregate as a real breach (e.g. an LT rule would otherwise fire on 0 < thresh).
		if r, ok := e.rules[top.pk.Rule]; ok {
			key := SeriesKey{Rule: r.ID, Series: top.pk.Series}
			if pa.count > 0 && cmp(r.Op, pa.value(r.Agg), r.Thresh) {
				e.emitValue(r, key, top.end, pa.value(r.Agg))
			} else {
				e.resolve(r, key, top.end)
			}
		}
		delete(e.panes, top.pk)
	}
	return consumed
}

// --- snapshot / restore for the window state ---

type snapSliding struct {
	Rule   string      `json:"rule"`
	Series string      `json:"series"`
	Times  []time.Time `json:"times"`
}

type snapPane struct {
	Rule   string  `json:"rule"`
	Series string  `json:"series"`
	Start  int64   `json:"start"`
	Count  int     `json:"count"`
	Sum    float64 `json:"sum"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

func (e *Engine) snapshotWindows() ([]snapSliding, []snapPane) {
	sliding := make([]snapSliding, 0, len(e.sliding))
	for k, times := range e.sliding {
		sliding = append(sliding, snapSliding{Rule: k.Rule, Series: k.Series, Times: times})
	}
	sort.Slice(sliding, func(i, j int) bool {
		if sliding[i].Rule != sliding[j].Rule {
			return sliding[i].Rule < sliding[j].Rule
		}
		return sliding[i].Series < sliding[j].Series
	})
	panes := make([]snapPane, 0, len(e.panes))
	for k, pa := range e.panes {
		panes = append(panes, snapPane{Rule: k.Rule, Series: k.Series, Start: k.Start, Count: pa.count, Sum: pa.sum, Min: pa.min, Max: pa.max})
	}
	sort.Slice(panes, func(i, j int) bool {
		if panes[i].Rule != panes[j].Rule {
			return panes[i].Rule < panes[j].Rule
		}
		if panes[i].Series != panes[j].Series {
			return panes[i].Series < panes[j].Series
		}
		return panes[i].Start < panes[j].Start
	})
	return sliding, panes
}

type snapCount struct {
	Rule   string  `json:"rule"`
	Series string  `json:"series"`
	Count  int     `json:"count"`
	Sum    float64 `json:"sum"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

func (e *Engine) snapshotCounts() []snapCount {
	out := make([]snapCount, 0, len(e.counts))
	for k, pa := range e.counts {
		out = append(out, snapCount{Rule: k.Rule, Series: k.Series, Count: pa.count, Sum: pa.sum, Min: pa.min, Max: pa.max})
	}
	sortByRuleSeries(out, func(i int) (string, string) { return out[i].Rule, out[i].Series })
	return out
}

func (e *Engine) restoreCounts(in []snapCount) {
	for _, c := range in {
		e.counts[SeriesKey{Rule: c.Rule, Series: c.Series}] = &paneAgg{count: c.Count, sum: c.Sum, min: c.Min, max: c.Max}
	}
}

// restoreWindows rebuilds the sliding buffers and tumbling panes (plus the close heap,
// recomputing each pane end from its rule's Window) into the engine.
func (e *Engine) restoreWindows(sliding []snapSliding, panes []snapPane) {
	for _, s := range sliding {
		e.sliding[SeriesKey{Rule: s.Rule, Series: s.Series}] = s.Times
	}
	for _, p := range panes {
		pk := paneKey{Rule: p.Rule, Series: p.Series, Start: p.Start}
		e.panes[pk] = &paneAgg{count: p.Count, sum: p.Sum, min: p.Min, max: p.Max}
		if r, ok := e.rules[p.Rule]; ok {
			end := time.Unix(0, p.Start).UTC().Add(r.Window)
			heap.Push(&e.closes, &closeItem{end: end, pk: pk})
		}
	}
}
