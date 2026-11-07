// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package geotest is the correctness oracle for the spatial engine: a brute-force scan that shares
// nothing with the covering code, and an in-memory index that scans encoded ranges the way a
// database btree would. Every spatial test, in this module or in a service that stores cells,
// compares the indexed answer with the brute-force one.
package geotest

import (
	"iter"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/geo"
)

// BruteForce answers a spatial question by resolving EVERY candidate and running the exact check.
// It never sees a covering, a range or a cell, which is what makes agreement with the indexed path
// evidence rather than tautology.
func BruteForce(all []geo.Candidate, region geo.Region, mode geo.Mode, at time.Time) ([]geo.Result, error) {
	return geo.Query(func(yield func(geo.Candidate) bool) {
		for _, c := range all {
			if !yield(c) {
				return
			}
		}
	}, region, mode, at)
}

// MemIndex mimics the two btree indexes (installed and reported) a database would hold.
type MemIndex struct {
	installed []entry
	reported  []entry
}

type entry struct {
	cell  int64
	token string
	cand  geo.Candidate
}

// NewMemIndex indexes the candidates by the encoded leaf cell of each position they carry.
func NewMemIndex(all []geo.Candidate) (*MemIndex, error) {
	ix := &MemIndex{}
	for _, c := range all {
		if c.Installed != nil {
			cell, err := geo.LeafCell(*c.Installed)
			if err != nil {
				return nil, err
			}
			ix.installed = append(ix.installed, entry{cell, c.Token, c})
		}
		if c.Reported != nil {
			cell, err := geo.LeafCell(c.Reported.Position)
			if err != nil {
				return nil, err
			}
			ix.reported = append(ix.reported, entry{cell, c.Token, c})
		}
	}
	for _, s := range [][]entry{ix.installed, ix.reported} {
		s := s
		sort.Slice(s, func(i, j int) bool { return s[i].cell < s[j].cell })
	}
	return ix, nil
}

// Scan range-scans the indexes the mode may read and yields every hit. A candidate found through
// both indexes is yielded twice, as a real two-index scan would; Query dedupes.
//
// Comparison is on the SIGNED encoded value, exactly as a bigint btree orders it.
func (ix *MemIndex) Scan(ranges []geo.Range, mode geo.Mode) iter.Seq[geo.Candidate] {
	var sources [][]entry
	switch mode {
	case geo.ModeInstalled:
		sources = [][]entry{ix.installed}
	case geo.ModeReported:
		sources = [][]entry{ix.reported}
	default:
		sources = [][]entry{ix.reported, ix.installed}
	}
	return func(yield func(geo.Candidate) bool) {
		for _, src := range sources {
			for _, r := range ranges {
				i := sort.Search(len(src), func(i int) bool { return src[i].cell >= r.Lo })
				for ; i < len(src) && src[i].cell <= r.Hi; i++ {
					if !yield(src[i].cand) {
						return
					}
				}
			}
		}
	}
}

// Indexed answers the question the way production does: cover, range-scan, then Query.
func Indexed(ix *MemIndex, region geo.Region, mode geo.Mode, at time.Time) ([]geo.Result, error) {
	ranges, err := region.Cover(geo.MaxCoverRanges)
	if err != nil {
		return nil, err
	}
	return geo.Query(ix.Scan(ranges, mode), region, mode, at)
}
