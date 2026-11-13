// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"errors"
	"fmt"
	"iter"
	"math"
	"sort"
	"time"
)

// DefaultMaxCandidates bounds how many distinct candidates one query will resolve. Exceeding it
// returns ErrLimitExceeded, never a partial set: a page cut from a truncated set would be a wrong
// answer that looks like a page.
const DefaultMaxCandidates = 50_000

// ErrLimitExceeded is returned when a query's candidate set exceeds its bound.
var ErrLimitExceeded = errors.New("the candidate set exceeds the query bound")

// ErrConflictingCandidate is returned when two candidates carry the same token but different
// positions. A token that resolves two ways has no right answer, and choosing one silently is how
// an entity ends up in the wrong place.
var ErrConflictingCandidate = errors.New("two candidates share a token but disagree about the entity")

// Candidate is one entity a range scan produced, carrying every position a mode may read. The
// caller fills in only what it is AUTHORISED to read: an installed-only caller passes a nil
// Reported, and Effective in INSTALLED mode would not read it anyway.
type Candidate struct {
	Token     string
	Reported  *Fix
	Freshness time.Duration
	Installed *Position
}

// Result is one entity that passed the exact check.
type Result struct {
	Token    string
	Position Position
	Source   Source
	// DistanceM is set for a near query and zero for a within query.
	DistanceM float64
}

// Region is what a query asks about. Construct it with Within or Near.
type Region struct {
	polygons []*Polygon
	center   Position
	radiusM  float64
	near     bool
}

// Within is a polygon-set region: a position matches if any polygon contains it.
func Within(set []*Polygon) (Region, error) {
	if len(set) == 0 {
		return Region{}, fmt.Errorf("%w: the polygon set is empty", ErrInvalidArgument)
	}
	for _, p := range set {
		if p == nil {
			return Region{}, fmt.Errorf("%w: nil polygon", ErrInvalidArgument)
		}
	}
	return Region{polygons: set}, nil
}

// Near is a radius region: a position matches if it is at most radiusM metres from center
// (boundary inclusive).
func Near(center Position, radiusM float64) (Region, error) {
	if err := center.Validate(); err != nil {
		return Region{}, err
	}
	if err := checkRadius(radiusM); err != nil {
		return Region{}, err
	}
	return Region{center: center, radiusM: radiusM, near: true}, nil
}

// Cover is the conservative covering for the region.
func (r Region) Cover(maxRanges int) ([]Range, error) {
	switch {
	case r.near:
		return CoverCap(r.center, r.radiusM, maxRanges)
	case len(r.polygons) > 0:
		return CoverPolygonSet(r.polygons, maxRanges)
	}
	return nil, fmt.Errorf("%w: the zero Region", ErrInvalidArgument)
}

// match is the exact check.
func (r Region) match(p Position) (bool, float64, error) {
	if r.near {
		d, err := DistanceM(r.center, p)
		return d <= r.radiusM, d, err
	}
	in, err := Contains(r.polygons, p)
	return in, 0, err
}

// Query resolves, filters and orders a candidate set.
//
//  1. dedupe by token (a duplicate with the same content is dropped; one that disagrees is an
//     error),
//  2. resolve each candidate's position for the mode at the ONE captured time `at`,
//  3. run the EXACT check (containment or spherical distance) — the covering that produced the
//     candidates is only ever a prefilter and a covering hit is never treated as a match,
//  4. order stably: (distance, token) for a near region, token for a within region.
//
// It returns the FULL filtered set. Pagination is the caller's job, over this set, so that counts
// and pages describe the same rows. More than DefaultMaxCandidates distinct candidates returns
// ErrLimitExceeded and no results.
func Query(candidates iter.Seq[Candidate], region Region, mode Mode, at time.Time) ([]Result, error) {
	if mode != ModeInstalled && mode != ModeReported && mode != ModeEffective {
		return nil, fmt.Errorf("%w: mode %v", ErrInvalidArgument, mode)
	}
	if !region.near && len(region.polygons) == 0 {
		return nil, fmt.Errorf("%w: the zero Region", ErrInvalidArgument)
	}
	seen := make(map[string]Candidate)
	var out []Result
	var failure error
	for c := range candidates {
		if prior, dup := seen[c.Token]; dup {
			if !sameCandidate(prior, c) {
				return nil, fmt.Errorf("%w: %q", ErrConflictingCandidate, c.Token)
			}
			continue
		}
		if len(seen) >= DefaultMaxCandidates {
			return nil, fmt.Errorf("%w: more than %d", ErrLimitExceeded, DefaultMaxCandidates)
		}
		seen[c.Token] = c
		pos, src, ok := Effective(c.Reported, c.Freshness, c.Installed, at, mode)
		if !ok {
			continue
		}
		hit, dist, err := region.match(pos)
		if err != nil {
			failure = fmt.Errorf("candidate %q: %w", c.Token, err)
			break
		}
		if !hit {
			continue
		}
		out = append(out, Result{Token: c.Token, Position: pos, Source: src, DistanceM: dist})
	}
	if failure != nil {
		return nil, failure
	}
	if region.near {
		sort.Slice(out, func(i, j int) bool {
			if out[i].DistanceM != out[j].DistanceM {
				return out[i].DistanceM < out[j].DistanceM
			}
			return out[i].Token < out[j].Token
		})
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	}
	return out, nil
}

// sameCandidate compares two candidates by VALUE, bitwise on the coordinates. Struct equality is not
// enough: NaN != NaN, so an identical duplicate carrying a corrupt coordinate would read as a
// conflict even though Effective treats that coordinate as absent.
func sameCandidate(a, b Candidate) bool {
	if a.Token != b.Token || a.Freshness != b.Freshness {
		return false
	}
	if (a.Installed == nil) != (b.Installed == nil) || (a.Installed != nil && !samePosition(*a.Installed, *b.Installed)) {
		return false
	}
	if (a.Reported == nil) != (b.Reported == nil) {
		return false
	}
	return a.Reported == nil || (samePosition(a.Reported.Position, b.Reported.Position) && a.Reported.At.Equal(b.Reported.At))
}

func samePosition(a, b Position) bool {
	return math.Float64bits(a.Lat) == math.Float64bits(b.Lat) &&
		math.Float64bits(a.Lon) == math.Float64bits(b.Lon) &&
		math.Float64bits(a.Elevation) == math.Float64bits(b.Elevation) &&
		a.HasElevation == b.HasElevation
}
