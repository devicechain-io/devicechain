// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "sort"

// applyCorrelation handles a Correlation rule: count the DISTINCT members (Event.Member —
// e.g. the device token) that have reported under an anchor (the series key, e.g. an area)
// within the trailing sliding Window, and fire on the rising edge where that distinct count
// reaches Count. Edge-triggered like Repeating: it fires once when a newly-seen member pushes
// the count over the line, and re-arms only after members age out below Count. Eviction is by
// event time, so replay re-derives the identical crossings from the snapshotted member set.
func (e *Engine) applyCorrelation(ev Event, r Rule) {
	if !ev.Match || r.Count <= 0 || ev.Member == "" {
		return
	}
	cutoff := ev.Time.Add(-r.Window).UnixNano()
	members := e.corr[ev.Key]
	if members == nil {
		members = map[string]int64{}
		e.corr[ev.Key] = members
	}
	for m, ts := range members {
		if ts <= cutoff {
			delete(members, m)
		}
	}
	prev := len(members)
	// Keep the member's MOST RECENT sighting: a bounded-late out-of-order refresh must not
	// regress the timestamp, or the member evicts early and a later event re-crosses the
	// threshold — a spurious second fire for a cohort that was continuously present. Test
	// existence explicitly so a first sighting is recorded regardless of its unix-nanos sign.
	ts := ev.Time.UnixNano()
	if cur, exists := members[ev.Member]; !exists || ts > cur {
		members[ev.Member] = ts
	}
	// Memory backstop: bound the distinct members retained per anchor. The real per-tenant
	// state budget is enforced upstream (ADR-023, Slice 4/6); this is a local guard so a
	// single hot anchor can't grow unbounded between checkpoints. Count must be ≤ MemberCap
	// to ever fire, which the compiler validates at publish.
	if r.MemberCap > 0 && len(members) > r.MemberCap {
		capMembers(members, r.MemberCap)
	}
	if prev < r.Count && len(members) >= r.Count {
		e.emit(r, ev.Key, ev.Time)
	}
}

// capMembers evicts oldest-by-(time, member) members until at most limit remain — a
// deterministic bound so the snapshot and every replay agree on which members survived.
func capMembers(members map[string]int64, limit int) {
	type mt struct {
		m string
		t int64
	}
	all := make([]mt, 0, len(members))
	for m, t := range members {
		all = append(all, mt{m: m, t: t})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].t != all[j].t {
			return all[i].t < all[j].t
		}
		return all[i].m < all[j].m
	})
	for i := 0; i < len(all)-limit; i++ {
		delete(members, all[i].m)
	}
}

// --- snapshot / restore ---

type snapCorrMember struct {
	Member string `json:"member"`
	Time   int64  `json:"time"`
}

type snapCorr struct {
	Rule    string           `json:"rule"`
	Series  string           `json:"series"`
	Members []snapCorrMember `json:"members"`
}

func (e *Engine) snapshotCorr() []snapCorr {
	out := make([]snapCorr, 0, len(e.corr))
	for k, members := range e.corr {
		ms := make([]snapCorrMember, 0, len(members))
		for m, t := range members {
			ms = append(ms, snapCorrMember{Member: m, Time: t})
		}
		sort.Slice(ms, func(i, j int) bool { return ms[i].Member < ms[j].Member })
		out = append(out, snapCorr{Rule: k.Rule, Series: k.Series, Members: ms})
	}
	sortByRuleSeries(out, func(i int) (string, string) { return out[i].Rule, out[i].Series })
	return out
}

func (e *Engine) restoreCorr(in []snapCorr) {
	for _, c := range in {
		members := make(map[string]int64, len(c.Members))
		for _, m := range c.Members {
			members[m.Member] = m.Time
		}
		e.corr[SeriesKey{Rule: c.Rule, Series: c.Series}] = members
	}
}
