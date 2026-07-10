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

func newTestRosterStore(t *testing.T) *DeviceRosterStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DeviceRoster{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewDeviceRosterStore(&rdb.RdbManager{Database: db})
}

var rosterBase = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// Upsert persists a roster entry; a re-type upserts in place (last-wins on profile + since);
// LoadAll returns the whole table.
func TestDeviceRosterStore_UpsertIsIdempotentAndLoads(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()

	r := &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "prof", ExpectedSince: rosterBase}
	if err := s.Upsert(ctx, r); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Re-type: same (tenant, device) key, new profile + a later membership-began time.
	r2 := &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "prof2", ExpectedSince: rosterBase.Add(time.Hour)}
	if err := s.Upsert(ctx, r2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 row after in-place re-type upsert, got %d", len(all))
	}
	if all[0].ProfileToken != "prof2" || !all[0].ExpectedSince.Equal(rosterBase.Add(time.Hour)) {
		t.Fatalf("re-type should overwrite profile + since in place, got %+v", all[0])
	}
}

// The composite (tenant, deviceToken) key keeps a device token that repeats across tenants
// distinct, and Delete only removes the matching tenant's row.
func TestDeviceRosterStore_CompositeKeyAndScopedDelete(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()
	for _, r := range []*DeviceRoster{
		{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p", ExpectedSince: rosterBase},
		{Tenant: "beta", DeviceToken: "dev1", ProfileToken: "p", ExpectedSince: rosterBase},
	} {
		if err := s.Upsert(ctx, r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	all, _ := s.LoadAll(ctx)
	if len(all) != 2 {
		t.Fatalf("same device token across two tenants should be two rows, got %d", len(all))
	}
	// Deleting acme's dev1 must leave beta's dev1 intact.
	if err := s.Delete(ctx, "acme", "dev1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, _ = s.LoadAll(ctx)
	if len(all) != 1 || all[0].Tenant != "beta" {
		t.Fatalf("scoped delete should remove only acme's row, got %+v", all)
	}
}

// Delete is idempotent: removing an absent device is a no-op (a redelivered entity-deleted fact).
func TestDeviceRosterStore_DeleteAbsentIsNoOp(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()
	if err := s.Delete(ctx, "acme", "ghost"); err != nil {
		t.Fatalf("deleting an absent row should be a no-op, got %v", err)
	}
}
