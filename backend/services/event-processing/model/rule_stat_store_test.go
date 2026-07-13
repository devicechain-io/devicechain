// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestRuleStatStore(t *testing.T) *RuleStatStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&RuleStat{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRuleStatStore(&rdb.RdbManager{Database: db})
}

func load(t *testing.T, s *RuleStatStore, tenant, id string) RuleStat {
	t.Helper()
	m, err := s.LoadByIDs(context.Background(), tenant, []string{id})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st, ok := m[id]
	if !ok {
		t.Fatalf("stat %q not found", id)
	}
	return st
}

// The first fire inserts the row; a later fire increments the count and advances
// last_fired_at / last_edge to the newer detection.
func TestRuleStatStore_RecordFireAdvances(t *testing.T) {
	s := newTestRuleStatStore(t)
	ctx := context.Background()
	id, tenant := "acme/p@1/hot", "acme"
	t0 := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	if err := s.RecordFire(ctx, id, tenant, t0, "raised"); err != nil {
		t.Fatalf("first fire: %v", err)
	}
	st := load(t, s, tenant, id)
	if st.FireCount != 1 || !st.LastFiredAt.Equal(t0) || st.LastEdge != "raised" {
		t.Fatalf("after first fire: %+v", st)
	}

	t1 := t0.Add(5 * time.Minute)
	if err := s.RecordFire(ctx, id, tenant, t1, "resolved"); err != nil {
		t.Fatalf("second fire: %v", err)
	}
	st = load(t, s, tenant, id)
	if st.FireCount != 2 || !st.LastFiredAt.Equal(t1) || st.LastEdge != "resolved" {
		t.Fatalf("after second fire: %+v", st)
	}
}

// Replaying an OLDER detection (a restart re-emitting from the last checkpoint) must not
// rewind last_fired_at or last_edge — the monotonic replay guard — even though fire_count,
// being an accepted approximation, does increment.
func TestRuleStatStore_OlderReplayDoesNotRewind(t *testing.T) {
	s := newTestRuleStatStore(t)
	ctx := context.Background()
	id, tenant := "acme/p@1/hot", "acme"
	tNew := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	tOld := tNew.Add(-10 * time.Minute)

	if err := s.RecordFire(ctx, id, tenant, tNew, "raised"); err != nil {
		t.Fatalf("newer fire: %v", err)
	}
	if err := s.RecordFire(ctx, id, tenant, tOld, "resolved"); err != nil {
		t.Fatalf("older replay: %v", err)
	}
	st := load(t, s, tenant, id)
	if !st.LastFiredAt.Equal(tNew) {
		t.Errorf("last_fired_at rewound to %v, want %v", st.LastFiredAt, tNew)
	}
	if st.LastEdge != "raised" {
		t.Errorf("last_edge rewound to %q, want %q", st.LastEdge, "raised")
	}
	if st.FireCount != 2 {
		t.Errorf("fire_count = %d, want 2 (approximate count increments on replay)", st.FireCount)
	}
}

// LoadByIDs is tenant-scoped and returns only the requested ids; a never-fired id is simply
// absent from the map (the "never fired" case the health view renders as null last-fired).
func TestRuleStatStore_LoadByIDsTenantScopedAndSparse(t *testing.T) {
	s := newTestRuleStatStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	if err := s.RecordFire(ctx, "acme/p@1/a", "acme", t0, "raised"); err != nil {
		t.Fatalf("fire a: %v", err)
	}
	if err := s.RecordFire(ctx, "other/p@1/a", "other", t0, "raised"); err != nil {
		t.Fatalf("fire other: %v", err)
	}

	m, err := s.LoadByIDs(ctx, "acme", []string{"acme/p@1/a", "acme/p@1/never", "other/p@1/a"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := m["acme/p@1/a"]; !ok {
		t.Error("acme's fired rule missing")
	}
	if _, ok := m["acme/p@1/never"]; ok {
		t.Error("never-fired rule should be absent")
	}
	if _, ok := m["other/p@1/a"]; ok {
		t.Error("another tenant's rule leaked past the tenant filter")
	}
}
