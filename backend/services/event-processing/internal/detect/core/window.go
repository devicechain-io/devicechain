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

// applyRepeating handles a Repeating rule: keep a sliding buffer of matching-event times
// within Window, and fire on the rising edge where the trailing count reaches Count.
func (e *Engine) applyRepeating(ev Event, r Rule) {
	if !ev.Match {
		return
	}
	cutoff := ev.Time.Add(-r.Window)
	buf := e.sliding[ev.Key]
	kept := make([]time.Time, 0, len(buf)+1)
	for _, ts := range buf {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	prev := len(kept)
	kept = append(kept, ev.Time)
	e.sliding[ev.Key] = kept
	if prev < r.Count && len(kept) >= r.Count {
		e.emit(r, ev.Key, ev.Time)
	}
}

// applyAggregate handles an Aggregate rule: fold each matching value into its tumbling
// window pane; the pane fires (or not) when it closes on the watermark (closePanes).
func (e *Engine) applyAggregate(ev Event, r Rule) {
	if !ev.Match {
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
	pa.add(ev.Value)
}

// applyCountWindow handles a CountWindow rule: fold each matching value into an event-count
// accumulator; when the Count-th event lands, evaluate Agg vs Thresh, emit on satisfaction,
// and reset. It is a tumbling window measured in events rather than time — no watermark, no
// timer — so replay just re-counts from the snapshotted partial accumulator.
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
		if cmp(r.Op, pa.value(r.Agg), r.Thresh) {
			e.emit(r, ev.Key, ev.Time)
		}
		delete(e.counts, ev.Key)
	}
}

// closePanes fires every tumbling window whose end has been crossed by the watermark.
func (e *Engine) closePanes(wm time.Time) {
	for e.closes.Len() > 0 {
		top := e.closes[0]
		if top.end.After(wm) {
			break
		}
		heap.Pop(&e.closes)
		pa, ok := e.panes[top.pk]
		if !ok {
			continue
		}
		r, ok := e.rules[top.pk.Rule]
		if ok && cmp(r.Op, pa.value(r.Agg), r.Thresh) {
			e.out = append(e.out, Detection{RuleID: r.ID, Series: top.pk.Series, Kind: Aggregate, At: top.end})
		}
		delete(e.panes, top.pk)
	}
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
