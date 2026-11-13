// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"
)

// A pod from before the clear-announcement columns advances a row to a new cycle without
// knowing them, so the row can carry the previous cycle's paged set and claim. Neither may be
// read as the new cycle's.
func TestStaleClearBookkeepingFromAnEarlierCycleIsIgnored(t *testing.T) {
	c1 := sql.NullTime{Time: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), Valid: true}
	c2 := sql.NullTime{Time: c1.Time.Add(time.Hour), Valid: true}

	paged := mergePaged(nil, c1, []PagedTarget{{Channel: "pd", Recipients: []string{"a"}}})
	s := &NotificationState{CycleRaisedAt: c1, PagedTargets: paged}
	if got := s.PagedTargetSet(); !got["pd|a"] {
		t.Fatalf("the cycle's own targets must be read, got %v", got)
	}
	s.CycleRaisedAt = c2 // an old pod moved the row to the next cycle
	if got := s.PagedTargetSet(); len(got) != 0 {
		t.Fatalf("an earlier cycle's targets were read as this one's: %v", got)
	}
	// Merging into the new cycle starts from nothing rather than inheriting the old set.
	merged := &NotificationState{CycleRaisedAt: c2, PagedTargets: mergePaged(paged, c2, []PagedTarget{{Channel: "slack"}})}
	if got := merged.PagedTargetSet(); len(got) != 1 || !got["slack|"] {
		t.Fatalf("merge inherited the earlier cycle's targets: %v", got)
	}

	// The claim names a CYCLE. One naming an earlier cycle is not this cycle's, however the
	// clocks of the pod that took it and the event compare.
	future := sql.NullTime{Time: c2.Time.Add(time.Hour), Valid: true}
	st := &NotificationState{CycleRaisedAt: c2, ClearedAt: future,
		ClearNotifiedCycle: c1, ClearNotifiedAt: sql.NullTime{Time: c2.Time, Valid: true}}
	if st.ClearAnnounced() {
		t.Fatal("a claim naming an earlier cycle suppressed this cycle's clear")
	}
	st.ClearNotifiedCycle = c2
	st.ClearNotifiedAt = sql.NullTime{Time: c2.Time.Add(-time.Hour), Valid: true} // earlier than cleared_at: irrelevant
	if !st.ClearAnnounced() {
		t.Fatal("a claim naming this cycle must stand even when its time precedes the clear's")
	}
}
