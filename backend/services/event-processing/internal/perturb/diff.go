// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package perturb

import (
	"sort"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
)

// Divergence kinds.
const (
	// Added is a firing the fork has and reality does not.
	Added = "added"
	// Removed is a firing reality has and the fork does not.
	Removed = "removed"
	// Shifted is a firing both have, at different instants.
	Shifted = "shifted"
)

// DefaultShiftTolerance is how far apart the same edge may sit in the two timelines and still be
// read as one firing that MOVED rather than one that vanished and another that appeared.
const DefaultShiftTolerance = 5 * time.Minute

// Divergence is one place the fork's firing timeline differs from reality's.
type Divergence struct {
	Kind   string
	Series string
	Raise  bool
	// At is the instant on the fork's timeline (Added, Shifted) or reality's (Removed).
	At time.Time
	// Baseline is reality's instant (Removed, Shifted); zero for Added.
	Baseline time.Time
	// Delta is At - Baseline for Shifted; zero otherwise.
	Delta time.Duration
}

// Diff compares reality's firings with the fork's. Edges are matched per (series, raise|resolve)
// in time order: identical instants cancel first, then the remainder pairs up when within tol
// (the pair is Shifted), and what is left is Removed (reality only) or Added (fork only). The
// result is sorted by instant, so it reads as the ribbon of a timeline.
//
// It is a pure function of its two inputs: no clock, no map-order dependence.
func Diff(baseline, fork []preview.Firing, tol time.Duration) []Divergence {
	if tol <= 0 {
		tol = DefaultShiftTolerance
	}
	type key struct {
		series string
		raise  bool
	}
	group := func(fs []preview.Firing) map[key][]time.Time {
		m := map[key][]time.Time{}
		for _, f := range fs {
			k := key{f.Series, f.Raise}
			m[k] = append(m[k], f.OccurredAt)
		}
		for k := range m {
			ts := m[k]
			sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		}
		return m
	}
	b, f := group(baseline), group(fork)
	keys := make([]key, 0, len(b)+len(f))
	seen := map[key]bool{}
	for k := range b {
		keys, seen[k] = append(keys, k), true
	}
	for k := range f {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].series != keys[j].series {
			return keys[i].series < keys[j].series
		}
		return !keys[i].raise && keys[j].raise
	})

	var out []Divergence
	for _, k := range keys {
		bs, fs := cancelEqual(b[k], f[k])
		i, j := 0, 0
		for i < len(bs) && j < len(fs) {
			d := fs[j].Sub(bs[i])
			switch {
			case absDur(d) <= tol:
				out = append(out, Divergence{Kind: Shifted, Series: k.series, Raise: k.raise, At: fs[j], Baseline: bs[i], Delta: d})
				i++
				j++
			case fs[j].Before(bs[i]):
				out = append(out, Divergence{Kind: Added, Series: k.series, Raise: k.raise, At: fs[j]})
				j++
			default:
				out = append(out, Divergence{Kind: Removed, Series: k.series, Raise: k.raise, At: bs[i], Baseline: bs[i]})
				i++
			}
		}
		for ; i < len(bs); i++ {
			out = append(out, Divergence{Kind: Removed, Series: k.series, Raise: k.raise, At: bs[i], Baseline: bs[i]})
		}
		for ; j < len(fs); j++ {
			out = append(out, Divergence{Kind: Added, Series: k.series, Raise: k.raise, At: fs[j]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		if out[i].Series != out[j].Series {
			return out[i].Series < out[j].Series
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return !out[i].Raise && out[j].Raise
	})
	return out
}

// cancelEqual removes instants present in both sorted lists (multiset difference).
func cancelEqual(a, b []time.Time) (ra, rb []time.Time) {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i].Equal(b[j]):
			i++
			j++
		case a[i].Before(b[j]):
			ra = append(ra, a[i])
			i++
		default:
			rb = append(rb, b[j])
			j++
		}
	}
	ra = append(ra, a[i:]...)
	rb = append(rb, b[j:]...)
	return ra, rb
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
