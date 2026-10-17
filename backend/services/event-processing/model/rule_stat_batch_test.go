// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

func mustBatch(t *testing.T, s *RuleStatStore, tenant string, deltas ...RuleFireDelta) int {
	t.Helper()
	n, err := s.RecordFireBatch(context.Background(), tenant, deltas)
	if err != nil {
		t.Fatalf("RecordFireBatch(%s): %v", tenant, err)
	}
	return n
}

// A batch ADDS its counts to what is stored, and advances the last fire only forward: the
// same row a run of single fires would leave, written in one statement.
func TestRecordFireBatchIsAdditiveAndMonotonic(t *testing.T) {
	s := newTestRuleStatStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	const id = "acme/p@1/hot"

	if err := s.RecordFire(ctx, id, "acme", t0, "raised"); err != nil {
		t.Fatalf("single fire: %v", err)
	}
	if n := mustBatch(t, s, "acme", RuleFireDelta{RuleID: id, Count: 41, LastFiredAt: t0.Add(time.Minute), LastEdge: "resolved"}); n != 1 {
		t.Fatalf("wrote %d rows, want 1", n)
	}
	st := load(t, s, "acme", id)
	if st.FireCount != 42 || !st.LastFiredAt.Equal(t0.Add(time.Minute)) || st.LastEdge != "resolved" {
		t.Fatalf("after an additive batch: %+v, want count 42, last resolved at t0+1m", st)
	}

	// An OLDER batch (a replay) still adds its count but does not rewind the last fire.
	mustBatch(t, s, "acme", RuleFireDelta{RuleID: id, Count: 8, LastFiredAt: t0, LastEdge: "raised"})
	st = load(t, s, "acme", id)
	if st.FireCount != 50 || !st.LastFiredAt.Equal(t0.Add(time.Minute)) || st.LastEdge != "resolved" {
		t.Fatalf("an older batch rewound the row: %+v", st)
	}

	// An EQUAL-time batch replaces the edge: the later write wins a tie, as RecordFire's >= does.
	mustBatch(t, s, "acme", RuleFireDelta{RuleID: id, Count: 1, LastFiredAt: t0.Add(time.Minute), LastEdge: "raised"})
	if st = load(t, s, "acme", id); st.LastEdge != "raised" || st.FireCount != 51 {
		t.Fatalf("an equal-time batch did not take the edge: %+v", st)
	}
}

// Duplicate rule rows are merged before the INSERT (PostgreSQL refuses one statement that
// updates a row twice): counts sum, the newest fire wins, and of two at the same time the
// later entry wins.
func TestRecordFireBatchConsolidatesDuplicates(t *testing.T) {
	s := newTestRuleStatStore(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	a, b := "acme/p@1/a", "acme/p@1/b"
	n := mustBatch(t, s, "acme",
		RuleFireDelta{RuleID: b, Count: 1, LastFiredAt: t0.Add(2 * time.Second), LastEdge: "raised"},
		RuleFireDelta{RuleID: a, Count: 2, LastFiredAt: t0, LastEdge: "raised"},
		RuleFireDelta{RuleID: b, Count: 3, LastFiredAt: t0, LastEdge: "resolved"}, // older: count only
		RuleFireDelta{RuleID: a, Count: 4, LastFiredAt: t0, LastEdge: "resolved"}, // tie: later wins
	)
	if n != 2 {
		t.Fatalf("wrote %d rows for two distinct rules, want 2", n)
	}
	if st := load(t, s, "acme", a); st.FireCount != 6 || st.LastEdge != "resolved" || !st.LastFiredAt.Equal(t0) {
		t.Errorf("rule a: %+v, want 6 fires, last resolved at t0", st)
	}
	if st := load(t, s, "acme", b); st.FireCount != 4 || st.LastEdge != "raised" || !st.LastFiredAt.Equal(t0.Add(2*time.Second)) {
		t.Errorf("rule b: %+v, want 4 fires, last raised at t0+2s", st)
	}
}

func TestConsolidateFireDeltasSortsByRuleID(t *testing.T) {
	t0 := time.Now()
	rows, err := consolidateFireDeltas("acme", []RuleFireDelta{
		{RuleID: "acme/p@1/c", Count: 1, LastFiredAt: t0, LastEdge: "raised"},
		{RuleID: "acme/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"},
		{RuleID: "acme/p@1/b", Count: 1, LastFiredAt: t0, LastEdge: "raised"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"acme/p@1/a", "acme/p@1/b", "acme/p@1/c"} {
		if rows[i].RuleId != want {
			t.Fatalf("row %d is %s, want %s (rows must be in lock order)", i, rows[i].RuleId, want)
		}
	}
}

// What the store refuses, before anything is sent: and each refusal leaves no row.
func TestRecordFireBatchRefusesInvalidInput(t *testing.T) {
	t0 := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		tenant string
		deltas []RuleFireDelta
		want   error
	}{
		{"zero count", "acme", []RuleFireDelta{{RuleID: "acme/p@1/a", Count: 0, LastFiredAt: t0, LastEdge: "raised"}}, ErrInvalidFireBatch},
		{"negative count", "acme", []RuleFireDelta{{RuleID: "acme/p@1/a", Count: -3, LastFiredAt: t0, LastEdge: "raised"}}, ErrInvalidFireBatch},
		{"no edge", "acme", []RuleFireDelta{{RuleID: "acme/p@1/a", Count: 1, LastFiredAt: t0}}, ErrInvalidFireBatch},
		{"another tenant's rule", "acme", []RuleFireDelta{{RuleID: "beta/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"}}, ErrInvalidFireBatch},
		{"a prefix that is not the tenant", "acme", []RuleFireDelta{{RuleID: "acme-2/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"}}, ErrInvalidFireBatch},
		{"no tenant", "", []RuleFireDelta{{RuleID: "/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"}}, ErrInvalidFireBatch},
		{"mixed tenants", "acme", []RuleFireDelta{
			{RuleID: "acme/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"},
			{RuleID: "beta/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"}}, ErrInvalidFireBatch},
		{"overflowing duplicates", "acme", []RuleFireDelta{
			{RuleID: "acme/p@1/a", Count: math.MaxInt64, LastFiredAt: t0, LastEdge: "raised"},
			{RuleID: "acme/p@1/a", Count: 1, LastFiredAt: t0, LastEdge: "raised"}}, ErrFireCountOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestRuleStatStore(t)
			_, err := s.RecordFireBatch(context.Background(), tc.tenant, tc.deltas)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			for _, tenant := range []string{"acme", "beta"} {
				got, err := s.LoadByIDs(context.Background(), tenant, []string{"acme/p@1/a", "beta/p@1/a"})
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 0 {
					t.Fatalf("a refused batch wrote %+v", got)
				}
			}
		})
	}
	// The empty batch is a no-op, not a refusal.
	if n, err := newTestRuleStatStore(t).RecordFireBatch(context.Background(), "acme", nil); n != 0 || err != nil {
		t.Fatalf("empty batch: %d, %v", n, err)
	}
}

// One tenant's batch reaches only its own rows, and a read under the other tenant sees none.
func TestRecordFireBatchIsTenantScoped(t *testing.T) {
	s := newTestRuleStatStore(t)
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	mustBatch(t, s, "acme", RuleFireDelta{RuleID: "acme/p@1/hot", Count: 5, LastFiredAt: t0, LastEdge: "raised"})
	mustBatch(t, s, "beta", RuleFireDelta{RuleID: "beta/p@1/hot", Count: 7, LastFiredAt: t0, LastEdge: "raised"})
	if st := load(t, s, "acme", "acme/p@1/hot"); st.FireCount != 5 || st.Tenant != "acme" {
		t.Errorf("acme row: %+v", st)
	}
	if st := load(t, s, "beta", "beta/p@1/hot"); st.FireCount != 7 || st.Tenant != "beta" {
		t.Errorf("beta row: %+v", st)
	}
	got, err := s.LoadByIDs(context.Background(), "beta", []string{"acme/p@1/hot"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("beta read acme's row: %+v", got)
	}
}

// The fence cost of a batch, on the production callback chain: ONE fence read and ONE
// INSERT for the whole tenant batch, however many rules it carries. RecordFire pays one of
// each per fire (TestFenceCostOfRecordFire).
func TestFenceCostOfRecordFireBatch(t *testing.T) {
	s, _, counter := newFencedRuleStatStore(t)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var deltas []RuleFireDelta
	for _, r := range []string{"a", "b", "c", "d"} {
		deltas = append(deltas, RuleFireDelta{RuleID: fenceStatTenant + "/thermostat@1/" + r, Count: 3, LastFiredAt: t0, LastEdge: "raised"})
	}
	for pass := 1; pass <= 2; pass++ { // insert, then the conflict update
		counter.Reset()
		if n := mustBatch(t, s, fenceStatTenant, deltas...); n != 4 {
			t.Fatalf("pass %d wrote %d rows, want 4", pass, n)
		}
		if all, fence := counter.Counts(); all != 2 || fence != 1 {
			t.Errorf("pass %d: a 4-rule batch made %d statements with %d fence reads; want 2 with 1", pass, all, fence)
		}
	}
	if got := load(t, s, fenceStatTenant, fenceStatTenant+"/thermostat@1/c").FireCount; got != 6 {
		t.Errorf("FireCount = %d after two batches of 3, want 6", got)
	}
}

// A fenced tenant's batch is refused by the store's own fence, writes nothing, and lifting the
// fence lets the same batch land: the in-memory filter upstream is not what protects erasure.
func TestRecordFireBatchForAFencedTenantIsRefused(t *testing.T) {
	s, db, _ := newFencedRuleStatStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := fenceSysDB(db).Create(&rdb.PurgedTenant{Token: fenceStatTenant, Epoch: now, PlantedAt: now}).Error; err != nil {
		t.Fatalf("plant a fence: %v", err)
	}
	d := RuleFireDelta{RuleID: fenceStatRule, Count: 9, LastFiredAt: now, LastEdge: "raised"}
	if _, err := s.RecordFireBatch(context.Background(), fenceStatTenant, []RuleFireDelta{d}); !errors.Is(err, rdb.ErrTenantPurged) {
		t.Fatalf("a fenced batch returned %v; want ErrTenantPurged", err)
	}
	var n int64
	if err := fenceSysDB(db).Model(&RuleStat{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a refused batch left %d row(s)", n)
	}
	if err := fenceSysDB(db).Model(&rdb.PurgedTenant{}).Where("token = ?", fenceStatTenant).
		Update("completed_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("lift the fence: %v", err)
	}
	mustBatch(t, s, fenceStatTenant, d)
	if got := load(t, s, fenceStatTenant, fenceStatRule).FireCount; got != 9 {
		t.Errorf("FireCount = %d with the fence lifted, want 9", got)
	}
}
