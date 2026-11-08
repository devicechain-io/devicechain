// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

// mergeSnapshots combines the snapshot states of K shards into the one state a single engine would
// have written. Each shard's sections are already in the canonical order and the shards hold
// disjoint keys, so every section is a K-way merge of sorted runs rather than a concatenation that
// has to be sorted again. A section that is empty in every shard is taken from the first, so a
// nil section stays nil and an empty one stays empty, exactly as one engine writes it.
func mergeSnapshots(parts []snapshot) snapshot {
	m := snapshot{Frontier: parts[0].Frontier, Watermark: parts[0].Watermark, LastSeq: parts[0].LastSeq}
	m.Active = mergeSection(parts, func(s *snapshot) []snapRun { return s.Active }, func(a, b *snapRun) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Breaks = mergeSection(parts, func(s *snapshot) []snapBreak { return s.Breaks }, func(a, b *snapBreak) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Timers = mergeSection(parts, func(s *snapshot) []snapTimer { return s.Timers }, func(a, b *snapTimer) bool {
		if !a.Deadline.Equal(b.Deadline) {
			return a.Deadline.Before(b.Deadline)
		}
		return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series)
	})
	m.Gens = mergeSection(parts, func(s *snapshot) []snapGen { return s.Gens }, func(a, b *snapGen) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Sliding = mergeSection(parts, func(s *snapshot) []snapSliding { return s.Sliding }, func(a, b *snapSliding) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Panes = mergeSection(parts, func(s *snapshot) []snapPane { return s.Panes }, func(a, b *snapPane) bool {
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Series != b.Series {
			return a.Series < b.Series
		}
		return a.Start < b.Start
	})
	m.Deltas = mergeSection(parts, func(s *snapshot) []snapDelta { return s.Deltas }, func(a, b *snapDelta) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Counts = mergeSection(parts, func(s *snapshot) []snapCount { return s.Counts }, func(a, b *snapCount) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Sessions = mergeSection(parts, func(s *snapshot) []snapSession { return s.Sessions }, func(a, b *snapSession) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Slides = mergeSection(parts, func(s *snapshot) []snapSlide { return s.Slides }, func(a, b *snapSlide) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Corr = mergeSection(parts, func(s *snapshot) []snapCorr { return s.Corr }, func(a, b *snapCorr) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Expected = mergeSection(parts, func(s *snapshot) []snapExpected { return s.Expected }, func(a, b *snapExpected) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Raised = mergeSection(parts, func(s *snapshot) []snapRaised { return s.Raised }, func(a, b *snapRaised) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Presence = mergeSection(parts, func(s *snapshot) []snapPresence { return s.Presence }, func(a, b *snapPresence) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Pending = mergeSection(parts, func(s *snapshot) []snapPending { return s.Pending }, func(a, b *snapPending) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	m.Newest = mergeSection(parts, func(s *snapshot) []snapNewest { return s.Newest }, func(a, b *snapNewest) bool { return ruleSeriesLess(a.Rule, a.Series, b.Rule, b.Series) })
	return m
}

func ruleSeriesLess(ra, sa, rb, sb string) bool {
	if ra != rb {
		return ra < rb
	}
	return sa < sb
}

// mergeSection merges one section of every part.
func mergeSection[T any](parts []snapshot, get func(*snapshot) []T, less func(a, b *T) bool) []T {
	runs := make([][]T, 0, len(parts))
	total := 0
	for i := range parts {
		r := get(&parts[i])
		if len(r) > 0 {
			runs = append(runs, r)
			total += len(r)
		}
	}
	switch len(runs) {
	case 0:
		return get(&parts[0])
	case 1:
		return runs[0]
	}
	for len(runs) > 1 {
		next := runs[:0:0]
		for i := 0; i+1 < len(runs); i += 2 {
			next = append(next, merge2(runs[i], runs[i+1], less))
		}
		if len(runs)%2 == 1 {
			next = append(next, runs[len(runs)-1])
		}
		runs = next
	}
	return runs[0]
}

func merge2[T any](a, b []T, less func(a, b *T) bool) []T {
	out := make([]T, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if less(&b[j], &a[i]) {
			out = append(out, b[j])
			j++
		} else {
			out = append(out, a[i])
			i++
		}
	}
	out = append(out, a[i:]...)
	return append(out, b[j:]...)
}
