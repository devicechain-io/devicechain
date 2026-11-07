// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package physpr is the experimental "physical pull request" toolkit: git's verbs applied to the
// outcomes of a deployed fleet's behaviour rather than to its text.
//
// A change to how the physical system behaves (a rule edit, a threshold, a profile bump) is judged
// by REPLAYING recorded traffic under the base and the head and comparing what each would have
// done. That comparison is only meaningful because detection is deterministic and replay-correct:
// the same events through the same rule yield the same edges, so any difference between two
// replays is caused by the change and by nothing else. Three pieces:
//
//   - DiffFirings compares two replays' edge timelines (this file);
//   - Bisect finds, over an ordered history of versions, an adjacent pair whose replayed outcome
//     differs, in O(log n) replays (bisect.go);
//   - Render* turn both into the report a reviewer reads (render.go).
//
// Everything here is a pure function over values the preview harness already produces. It reads no
// stream, writes nothing and holds no state, so it inherits the preview's isolation contract
// unchanged: a physical PR cannot perturb the live engine.
package physpr

import (
	"sort"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
)

// DefaultShiftWindow is how far apart a base edge and a head edge on the same series may be and
// still count as the SAME alarm, moved. Beyond it they are a removal and an addition. It is a
// reviewer-facing reading of the diff, not a property of the engine, so the caller may widen or
// narrow it.
const DefaultShiftWindow = time.Hour

// Kind classifies one difference between the base and head timelines.
type Kind string

const (
	// Added is an edge the head produces and the base does not.
	Added Kind = "added"
	// Removed is an edge the base produces and the head does not (it is suppressed).
	Removed Kind = "removed"
	// Shifted is an edge both produce, at different instants.
	Shifted Kind = "shifted"
)

// Edge names which side of an alarm's lifecycle a change concerns.
type Edge string

const (
	// Raise is the rising edge: the alarm fires.
	Raise Edge = "raise"
	// Resolve is the falling edge: the alarm clears.
	Resolve Edge = "resolve"
)

// Change is one difference. Base is zero for an Added change and Head is zero for a Removed one.
// Shift is Head minus Base for a Shifted change, so a negative shift means the head fires EARLIER.
type Change struct {
	Series string
	Edge   Edge
	Kind   Kind
	Base   time.Time
	Head   time.Time
	Shift  time.Duration
}

// Counts tallies one edge kind across every series.
type Counts struct {
	Unchanged int
	Added     int
	Removed   int
	Shifted   int
}

// Changed is the number of edges that differ.
func (c Counts) Changed() int { return c.Added + c.Removed + c.Shifted }

// SeriesDelta is one series' net effect, for the "which devices does this touch" line.
type SeriesDelta struct {
	Series  string
	Added   int // raises added
	Removed int // raises removed
	Shifted int // raises moved
	// Earliest is the most negative raise shift on the series (zero when none moved earlier).
	Earliest time.Duration
	// Latest is the most positive raise shift on the series (zero when none moved later).
	Latest time.Duration
}

// Diff is the outcome comparison of two replays.
type Diff struct {
	Raises   Counts
	Resolves Counts
	// Changes lists every difference, ordered by the instant it concerns, then series.
	Changes []Change
	// Series lists the series with at least one raise difference, most-affected first.
	Series []SeriesDelta
	// Base and Head are the two replays' coverage counters, so a diff that is quiet because one
	// side could not evaluate (calculated skips, predicate errors) says so.
	Base, Head preview.Stats
	// BaseDegraded and HeadDegraded carry each replay's partial-result reason. A diff of two
	// partial replays is evidence, not proof, and the report says so.
	BaseDegraded, HeadDegraded string
}

// Empty reports that the head behaves exactly like the base over the window: no edge was added,
// removed or moved. It is the property a no-op edit must have, and the negative control the
// demo leans on.
func (d Diff) Empty() bool { return d.Raises.Changed() == 0 && d.Resolves.Changed() == 0 }

// DiffFirings compares the base and head edge timelines. shiftWindow <= 0 means DefaultShiftWindow.
//
// Edges are grouped by (series, edge) and each group's two time-sorted lists are walked together:
// when the next base and head edges lie within shiftWindow of each other they are the same edge
// (Unchanged when the instants are equal, Shifted otherwise); otherwise the earlier of the two is
// unmatched on the other side and is reported Removed or Added. The walk is a pure function of its
// inputs, so the same pair of replays always yields the same diff.
func DiffFirings(base, head preview.Result, shiftWindow time.Duration) Diff {
	if shiftWindow <= 0 {
		shiftWindow = DefaultShiftWindow
	}
	type key struct {
		series string
		edge   Edge
	}
	group := func(fs []preview.Firing) map[key][]time.Time {
		m := map[key][]time.Time{}
		for _, f := range fs {
			e := Resolve
			if f.Raise {
				e = Raise
			}
			k := key{f.Series, e}
			m[k] = append(m[k], f.OccurredAt)
		}
		for _, ts := range m {
			sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		}
		return m
	}
	b, h := group(base.Firings), group(head.Firings)
	keys := map[key]struct{}{}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range h {
		keys[k] = struct{}{}
	}

	d := Diff{Base: base.Stats, Head: head.Stats, BaseDegraded: base.Degraded, HeadDegraded: head.Degraded}
	per := map[string]*SeriesDelta{}
	delta := func(series string) *SeriesDelta {
		if per[series] == nil {
			per[series] = &SeriesDelta{Series: series}
		}
		return per[series]
	}
	for k := range keys {
		counts := &d.Raises
		if k.edge == Resolve {
			counts = &d.Resolves
		}
		bs, hs := b[k], h[k]
		i, j := 0, 0
		for i < len(bs) || j < len(hs) {
			switch {
			case i < len(bs) && j < len(hs) && absDur(hs[j].Sub(bs[i])) <= shiftWindow:
				if bs[i].Equal(hs[j]) {
					counts.Unchanged++
				} else {
					counts.Shifted++
					shift := hs[j].Sub(bs[i])
					d.Changes = append(d.Changes, Change{Series: k.series, Edge: k.edge, Kind: Shifted, Base: bs[i], Head: hs[j], Shift: shift})
					if k.edge == Raise {
						sd := delta(k.series)
						sd.Shifted++
						if shift < sd.Earliest {
							sd.Earliest = shift
						}
						if shift > sd.Latest {
							sd.Latest = shift
						}
					}
				}
				i++
				j++
			case j >= len(hs) || (i < len(bs) && bs[i].Before(hs[j])):
				counts.Removed++
				d.Changes = append(d.Changes, Change{Series: k.series, Edge: k.edge, Kind: Removed, Base: bs[i]})
				if k.edge == Raise {
					delta(k.series).Removed++
				}
				i++
			default:
				counts.Added++
				d.Changes = append(d.Changes, Change{Series: k.series, Edge: k.edge, Kind: Added, Head: hs[j]})
				if k.edge == Raise {
					delta(k.series).Added++
				}
				j++
			}
		}
	}

	at := func(c Change) time.Time {
		if !c.Head.IsZero() {
			return c.Head
		}
		return c.Base
	}
	sort.Slice(d.Changes, func(i, j int) bool {
		a, c := d.Changes[i], d.Changes[j]
		if !at(a).Equal(at(c)) {
			return at(a).Before(at(c))
		}
		if a.Series != c.Series {
			return a.Series < c.Series
		}
		if a.Edge != c.Edge {
			return a.Edge < c.Edge
		}
		return a.Kind < c.Kind
	})
	for _, sd := range per {
		d.Series = append(d.Series, *sd)
	}
	sort.Slice(d.Series, func(i, j int) bool {
		a, c := d.Series[i], d.Series[j]
		an, cn := a.Added+a.Removed+a.Shifted, c.Added+c.Removed+c.Shifted
		if an != cn {
			return an > cn
		}
		return a.Series < c.Series
	})
	return d
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
