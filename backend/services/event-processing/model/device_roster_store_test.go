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

// Load returns a live device (present, not tombstoned), reports live=false for a tombstoned one,
// and live=false with no error for an absent one — the authoritative post-merge read the arming
// loop reflects onto the engine (slice 4c-2b-2b).
func TestDeviceRosterStore_Load(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "prof", ExpectedSince: rosterBase}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	row, live, err := s.Load(ctx, "acme", "dev1")
	if err != nil || !live {
		t.Fatalf("live device: got live=%v err=%v", live, err)
	}
	if row.ProfileToken != "prof" || !row.ExpectedSince.Equal(rosterBase) {
		t.Fatalf("live row fields wrong: %+v", row)
	}
	// Absent device: not found, no error, disarm.
	if _, live, err := s.Load(ctx, "acme", "nope"); err != nil || live {
		t.Fatalf("absent device: got live=%v err=%v", live, err)
	}
	// Tombstoned device: found but not live.
	if err := s.Delete(ctx, "acme", "dev1", rosterBase.Add(time.Hour)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, live, err := s.Load(ctx, "acme", "dev1"); err != nil || live {
		t.Fatalf("tombstoned device: got live=%v err=%v", live, err)
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
	// Deleting acme's dev1 (with a later lifecycle time) must leave beta's dev1 live.
	if err := s.Delete(ctx, "acme", "dev1", rosterBase.Add(time.Hour)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, _ = s.LoadAll(ctx)
	if len(all) != 1 || all[0].Tenant != "beta" {
		t.Fatalf("scoped delete should tombstone only acme's row, got %+v", all)
	}
}

// A stale (older lifecycle time) upsert must NOT overwrite a newer row — the monotonic guard
// closes the failed-ack redelivery regression.
func TestDeviceRosterStore_StaleUpsertDoesNotRegress(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "prof2", ExpectedSince: rosterBase.Add(time.Hour)}); err != nil {
		t.Fatalf("newer upsert: %v", err)
	}
	// A redelivered older create (prof1, earlier) must be ignored.
	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "prof1", ExpectedSince: rosterBase}); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}
	all, _ := s.LoadAll(ctx)
	if len(all) != 1 || all[0].ProfileToken != "prof2" || !all[0].ExpectedSince.Equal(rosterBase.Add(time.Hour)) {
		t.Fatalf("stale upsert must not regress the row, got %+v", all)
	}
}

// A stale redelivered create cannot RESURRECT a tombstoned device, but a genuine newer re-create
// (token reuse with a fresh lifecycle time) does bring it back live.
func TestDeviceRosterStore_TombstoneResurrectionRules(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()
	create := &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p", ExpectedSince: rosterBase}
	if err := s.Upsert(ctx, create); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Delete(ctx, "acme", "dev1", rosterBase.Add(time.Hour)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Redeliver the ORIGINAL create (older than the deletion) — must not resurrect. Upsert mutates
	// its argument's Deleted/LastEventAt, so pass a fresh copy matching the original fact.
	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p", ExpectedSince: rosterBase}); err != nil {
		t.Fatalf("stale re-create: %v", err)
	}
	if all, _ := s.LoadAll(ctx); len(all) != 0 {
		t.Fatalf("a stale create must not resurrect a tombstone, got %+v", all)
	}
	// A genuine re-create with a NEWER lifecycle time brings the token back live.
	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p2", ExpectedSince: rosterBase.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("genuine re-create: %v", err)
	}
	all, _ := s.LoadAll(ctx)
	if len(all) != 1 || all[0].ProfileToken != "p2" {
		t.Fatalf("a newer re-create must resurrect the token live, got %+v", all)
	}
}

// A stale redelivered delete must NOT erase a newer live device, and a delete that races ahead of
// the device's create (cross-stream reordering) lands a tombstone a still-older create cannot lift.
func TestDeviceRosterStore_StaleDeleteAndDeleteBeforeCreate(t *testing.T) {
	s := newTestRosterStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p", ExpectedSince: rosterBase.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if err := s.Delete(ctx, "acme", "dev1", rosterBase.Add(time.Hour)); err != nil {
		t.Fatalf("stale delete: %v", err)
	}
	if all, _ := s.LoadAll(ctx); len(all) != 1 {
		t.Fatalf("a stale delete must not erase a newer live device, got %+v", all)
	}

	// Delete-before-create: the delete for dev2 is applied first (lands a fresh tombstone); the
	// device's own OLDER create then cannot lift it.
	if err := s.Delete(ctx, "acme", "dev2", rosterBase.Add(time.Hour)); err != nil {
		t.Fatalf("delete-first: %v", err)
	}
	if err := s.Upsert(ctx, &DeviceRoster{Tenant: "acme", DeviceToken: "dev2", ProfileToken: "p", ExpectedSince: rosterBase}); err != nil {
		t.Fatalf("older create after delete: %v", err)
	}
	all, _ := s.LoadAll(ctx)
	for _, r := range all {
		if r.DeviceToken == "dev2" {
			t.Fatalf("an older create must not lift a newer tombstone (delete-before-create), got %+v", r)
		}
	}
}
