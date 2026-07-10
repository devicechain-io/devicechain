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

func newTestActiveStore(t *testing.T) *ProfileActiveStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&ProfileActive{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewProfileActiveStore(&rdb.RdbManager{Database: db})
}

// Upsert is last-fact-wins per (tenant, profileToken): a later publish (and a rollback re-emit
// with a fresh publish time) overwrites the active version + publish time in place.
func TestProfileActiveStore_LastFactWins(t *testing.T) {
	s := newTestActiveStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	if err := s.Upsert(ctx, &ProfileActive{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: base}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if err := s.Upsert(ctx, &ProfileActive{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@2", PublishedAt: base.Add(time.Hour)}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	// Rollback to v1: re-emitted with a FRESH publish time (later than v2's).
	if err := s.Upsert(ctx, &ProfileActive{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: base.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 active-version row per profile, got %d", len(all))
	}
	if all[0].ActiveVersionToken != "p@1" || !all[0].PublishedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("rollback re-emit should win (last-fact-wins), got %+v", all[0])
	}
}

// Distinct profiles and tenants keep distinct rows.
func TestProfileActiveStore_MultiScope(t *testing.T) {
	s := newTestActiveStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for _, a := range []*ProfileActive{
		{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: base},
		{Tenant: "acme", ProfileToken: "q", ActiveVersionToken: "q@1", PublishedAt: base},
		{Tenant: "beta", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: base},
	} {
		if err := s.Upsert(ctx, a); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	all, _ := s.LoadAll(ctx)
	if len(all) != 3 {
		t.Fatalf("expected 3 distinct (tenant, profile) rows, got %d", len(all))
	}
}
