// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "time"

// The duplicate-event guard as it was before the tables were re-sized (power-of-two tables, doubled
// at 70% load, single-slot linear probing), vendored as the oracle for the differential test. Do
// not "improve" it: its answers are the specification the new tables must reproduce.

type oldGuard struct {
	retentionGens int64
	maxEntries    int
	headLimit     int
	total         int
	gens          []*oldGeneration
}

type oldGeneration struct {
	gen   int64
	count int
	slots []uint64
}

// newOldGuard builds a guard with the given retention (0 means DefaultAppliedRetention) and
// entry cap (0 means DefaultAppliedMaxEntries).
func newOldGuard(retention time.Duration, maxEntries int) *oldGuard {
	if retention <= 0 {
		retention = DefaultAppliedRetention
	}
	if maxEntries <= 0 {
		maxEntries = DefaultAppliedMaxEntries
	}
	// n oldGenerations back plus the current one cover n*genSeconds seconds at minimum.
	n := (int64(retention/time.Second) + genSeconds - 1) / genSeconds
	// A oldGeneration may hold twice its even share of the cap: ample for a steady rate, and the
	// bound that stops a skewed clock growing one without limit.
	limit := max(1024, 2*maxEntries/int(n+1))
	return &oldGuard{retentionGens: n, maxEntries: maxEntries, headLimit: limit}
}

// Len is the number of remembered events.
func (g *oldGuard) Len() int { return g.total }

// Generations is how many oldGenerations are held (tests, and the state gauge).
func (g *oldGuard) Generations() int { return len(g.gens) }

// MaxEntries is the entry cap.
func (g *oldGuard) MaxEntries() int { return g.maxEntries }

// Retention is the retention the guard keeps, rounded up to its oldGenerations.
func (g *oldGuard) Retention() time.Duration {
	return time.Duration(g.retentionGens*genSeconds) * time.Second
}

// Admit records id as applied by a message appended at t and reports true, or reports false (and
// records nothing new) when it is already remembered: the event is a duplicate to skip. Times
// should be non-decreasing (a stream's append times are); one that is not is recorded in the
// newest oldGeneration.
func (g *oldGuard) Admit(t time.Time, id Digest) bool {
	gn := t.Unix() / genSeconds
	if n := len(g.gens); n == 0 || gn > g.gens[n-1].gen {
		g.gens = append(g.gens, &oldGeneration{gen: gn})
	} else if g.gens[n-1].count >= g.headLimit {
		g.gens = append(g.gens, &oldGeneration{gen: g.gens[n-1].gen + 1})
	}
	head := g.gens[len(g.gens)-1]
	// Expire: everything not within retentionGens of the newest oldGeneration.
	for len(g.gens) > 1 && (head.gen-g.gens[0].gen > g.retentionGens || g.total > g.maxEntries) {
		g.total -= g.gens[0].count
		g.gens = g.gens[1:]
	}
	f := id.Fingerprint()
	// 🔴 A DUPLICATE REFRESHES THE ENTRY (it is re-recorded in the newest oldGeneration). Remembered
	// then means "SEEN, applied or refused, within the retention", which depends only on that
	// suffix of the stream, so priming from the retention's worth of history rebuilds exactly
	// the set the live loop held. Were a refusal not to refresh, the set would depend on which
	// occurrence came first, which a primed suffix cannot know.
	seen := false
	for _, gen := range g.gens {
		if gen.has(f) {
			seen = true
			break
		}
	}
	if seen {
		if !head.has(f) {
			head.add(f)
			g.total++
		}
		return false
	}
	head.add(f)
	g.total++
	return true
}

func (s *oldGeneration) has(f uint64) bool {
	if len(s.slots) == 0 {
		return false
	}
	mask := uint64(len(s.slots) - 1)
	for i := mix(f) & mask; ; i = (i + 1) & mask {
		switch s.slots[i] {
		case 0:
			return false
		case f:
			return true
		}
	}
}

func (s *oldGeneration) add(f uint64) {
	if len(s.slots) == 0 || (s.count+1)*10 > len(s.slots)*7 {
		s.grow()
	}
	mask := uint64(len(s.slots) - 1)
	i := mix(f) & mask
	for s.slots[i] != 0 {
		i = (i + 1) & mask
	}
	s.slots[i] = f
	s.count++
}

func (s *oldGeneration) grow() {
	old := s.slots
	n := 1024
	if len(old) > 0 {
		n = len(old) * 2
	}
	s.slots = make([]uint64, n)
	mask := uint64(n - 1)
	for _, f := range old {
		if f == 0 {
			continue
		}
		i := mix(f) & mask
		for s.slots[i] != 0 {
			i = (i + 1) & mask
		}
		s.slots[i] = f
	}
}
