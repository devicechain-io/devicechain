// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestRuleStore(t *testing.T) *DetectRuleStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DetectRule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewDetectRuleStore(&rdb.RdbManager{Database: db})
}

// Upsert persists rules and LoadAll returns them; a second Upsert of the same id overwrites
// in place (idempotent redelivery / re-publish) rather than duplicating.
func TestDetectRuleStore_UpsertIsIdempotentAndLoads(t *testing.T) {
	s := newTestRuleStore(t)
	ctx := context.Background()

	rule := DetectRule{RuleId: "acme/p@1/hot", Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "hot", Definition: `{"v":1}`}
	if err := s.Upsert(ctx, []DetectRule{rule}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Re-publish the same id with a new definition (last-wins).
	rule.Definition = `{"v":2}`
	if err := s.Upsert(ctx, []DetectRule{rule}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 row after idempotent re-upsert, got %d", len(all))
	}
	if all[0].Definition != `{"v":2}` {
		t.Fatalf("expected last-wins definition, got %q", all[0].Definition)
	}
}

// LoadByID returns the single row for a composed id (the REACT resolution path) and reports
// found=false for an unknown id, never an error.
func TestDetectRuleStore_LoadByID(t *testing.T) {
	s := newTestRuleStore(t)
	ctx := context.Background()
	rule := DetectRule{RuleId: "acme/p@1/hot", Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "hot", Definition: `{"actions":[]}`}
	if err := s.Upsert(ctx, []DetectRule{rule}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, found, err := s.LoadByID(ctx, "acme/p@1/hot")
	if err != nil || !found {
		t.Fatalf("LoadByID present: found=%v err=%v", found, err)
	}
	if got.Definition != `{"actions":[]}` {
		t.Fatalf("wrong definition: %q", got.Definition)
	}
	_, found, err = s.LoadByID(ctx, "acme/p@1/missing")
	if err != nil {
		t.Fatalf("LoadByID of an unknown id must not error: %v", err)
	}
	if found {
		t.Fatal("LoadByID must report found=false for an unknown id")
	}
}

// An empty batch is a no-op, and rules across tenants/versions all load (retain-superseded).
func TestDetectRuleStore_EmptyAndMultiScope(t *testing.T) {
	s := newTestRuleStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, nil); err != nil {
		t.Fatalf("empty upsert should be a no-op: %v", err)
	}
	rows := []DetectRule{
		{RuleId: "acme/p@1/r", Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "r", Definition: `{}`},
		{RuleId: "acme/p@2/r", Tenant: "acme", ProfileVersionToken: "p@2", RuleToken: "r", Definition: `{}`},
		{RuleId: "beta/q@1/r", Tenant: "beta", ProfileVersionToken: "q@1", RuleToken: "r", Definition: `{}`},
	}
	if err := s.Upsert(ctx, rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows (both versions + other tenant retained), got %d", len(all))
	}
}
