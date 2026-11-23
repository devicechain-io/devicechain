// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func refreshJob(id int, view string) timescaleJob {
	return timescaleJob{ID: id, Proc: "policy_refresh_continuous_aggregate", View: view, Scheduled: true}
}

// One refresh job proves nothing about the other aggregate: a restore that keeps the
// minute level's policy and loses the hour level's must be named, and the reverse.
func TestEachRollupLevelNeedsItsOwnRefreshJob(t *testing.T) {
	compress := timescaleJob{ID: 9, Proc: "policy_compression", View: "measurement_events", Scheduled: true}
	for _, tc := range []struct {
		name    string
		jobs    []timescaleJob
		missing []string
	}{
		{"both levels", []timescaleJob{compress, refreshJob(1, rollupView), refreshJob(2, rollupView1h)}, nil},
		{"hour level lost", []timescaleJob{compress, refreshJob(1, rollupView)}, []string{rollupView1h}},
		{"minute level lost", []timescaleJob{refreshJob(2, rollupView1h)}, []string{rollupView}},
		{"none", []timescaleJob{compress}, []string{rollupView, rollupView1h}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, missing := refreshJobsByView(tc.jobs, rollupViews)
			if strings.Join(missing, ",") != strings.Join(tc.missing, ",") {
				t.Fatalf("missing = %v, want %v", missing, tc.missing)
			}
			for _, v := range rollupViews {
				if _, ok := found[v]; ok == contains(tc.missing, v) {
					t.Fatalf("found[%s] present=%v, but missing lists it %v", v, ok, tc.missing)
				}
			}
		})
	}
	// The minute level's own job id is the one the scheduler check watches.
	found, _ := refreshJobsByView([]timescaleJob{refreshJob(7, rollupView1h), refreshJob(3, rollupView)}, rollupViews)
	if found[rollupView] != 3 {
		t.Fatalf("the minute level's refresh job is %d, want 3", found[rollupView])
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The hour level's marker must come back exactly: absent and moved are both a
// mismatch, and only an identical row passes.
func TestTheHourLevelMarkerMustComeBackUnchanged(t *testing.T) {
	const seeded = "2026-11-22T14:00:00Z"
	if err := judgeHourLevelMarker(seeded, true, seeded); err != nil {
		t.Fatalf("an unchanged marker failed: %v", err)
	}
	for _, tc := range []struct {
		name    string
		got     string
		present bool
	}{
		{"absent", "", false},
		{"moved", "2026-11-22T15:00:00Z", true},
	} {
		err := judgeHourLevelMarker(tc.got, tc.present, seeded)
		if codeOf(err) != exitMismatch {
			t.Fatalf("%s: %v, want an exitMismatch verdict", tc.name, err)
		}
	}
}

// A receipt seeded before the hour level was checked is refused, not verified on
// less evidence.
func TestAnEventSeedWithoutTheHourLevelIsRefused(t *testing.T) {
	seed := EventSeed{Tenant: "t", DeviceToken: "d", Name: "n", Start: "s", End: "e", RawCount: 1,
		MaterializedCount: 1, CompressedChunks: 1, CompressedRows: 1, EventType: 1, Sum: 1}
	err := seed.Validate()
	if err == nil {
		t.Fatal("an event seed without the hour level validated")
	}
	for _, field := range []string{"events.hourMaterializedCount", "events.hourLevelCompleteFrom"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("%q is not named in %q", field, err)
		}
	}
	seed.HourMaterializedCount, seed.HourLevelCompleteFrom = 1, "2026-11-22T14:00:00Z"
	if err := seed.Validate(); err != nil {
		t.Fatalf("a complete event seed was refused: %v", err)
	}
}
