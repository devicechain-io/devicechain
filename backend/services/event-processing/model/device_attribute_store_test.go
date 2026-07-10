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

func newTestAttributeStore(t *testing.T) *DeviceAttributeStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DeviceAttribute{}, &DeviceAttributeDeletion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewDeviceAttributeStore(&rdb.RdbManager{Database: db})
}

var attrBase = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// find returns the live value for a (tenant, device, scope, key) from LoadAll, and whether present.
func find(t *testing.T, s *DeviceAttributeStore, tenant, device, scope, key string) (float64, bool) {
	t.Helper()
	all, err := s.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	for _, a := range all {
		if a.Tenant == tenant && a.DeviceToken == device && a.Scope == scope && a.AttrKey == key {
			return a.Value, true
		}
	}
	return 0, false
}

// A set upserts in place (last-wins on value); LoadAll returns only live rows.
func TestDeviceAttributeStore_UpsertAndLoad(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "maxTemp", Value: 100, LastEventAt: attrBase}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "maxTemp", Value: 120, LastEventAt: attrBase.Add(time.Hour)}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if v, ok := find(t, s, "acme", "d1", "SHARED", "maxTemp"); !ok || v != 120 {
		t.Fatalf("expected in-place overwrite to 120, got v=%v ok=%v", v, ok)
	}
}

// The same key in the two eligible scopes is TWO distinct rows: a write/removal in one never
// annihilates the other (the H1 blocker the fact's scope field closed).
func TestDeviceAttributeStore_SameKeyDistinctPerScope(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "maxTemp", Value: 100, LastEventAt: attrBase}); err != nil {
		t.Fatalf("shared: %v", err)
	}
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", AttrKey: "maxTemp", Value: 90, LastEventAt: attrBase}); err != nil {
		t.Fatalf("server: %v", err)
	}
	// Remove the SHARED one; SERVER must survive.
	if err := s.Remove(ctx, "acme", "d1", "SHARED", "maxTemp", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("remove shared: %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "maxTemp"); ok {
		t.Fatalf("SHARED should be tombstoned")
	}
	if v, ok := find(t, s, "acme", "d1", "SERVER", "maxTemp"); !ok || v != 90 {
		t.Fatalf("SERVER sibling must survive a SHARED removal, got v=%v ok=%v", v, ok)
	}
}

// A per-attribute removal tombstones the row; a STALE older set cannot resurrect it, but a genuine
// newer re-set does.
func TestDeviceAttributeStore_RemovalTombstoneResurrectionRules(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 5, LastEventAt: attrBase}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Remove(ctx, "acme", "d1", "SHARED", "k", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Redeliver the ORIGINAL set (older than the removal) — must not resurrect.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 5, LastEventAt: attrBase}); err != nil {
		t.Fatalf("stale re-set: %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("a stale set must not resurrect a removal tombstone")
	}
	// A genuine newer re-set brings it back live.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 7, LastEventAt: attrBase.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("genuine re-set: %v", err)
	}
	if v, ok := find(t, s, "acme", "d1", "SHARED", "k"); !ok || v != 7 {
		t.Fatalf("a newer re-set must resurrect the value, got v=%v ok=%v", v, ok)
	}
}

// A stale (older) set must not regress a newer value, and a removal that races ahead of the set
// (cross-consumer reordering) lands a tombstone a still-older set cannot lift.
func TestDeviceAttributeStore_StaleSetAndRemoveBeforeSet(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 120, LastEventAt: attrBase.Add(time.Hour)}); err != nil {
		t.Fatalf("newer set: %v", err)
	}
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: attrBase}); err != nil {
		t.Fatalf("stale set: %v", err)
	}
	if v, _ := find(t, s, "acme", "d1", "SHARED", "k"); v != 120 {
		t.Fatalf("stale set must not regress the value, got %v", v)
	}
	// Remove-before-set for key j: the removal lands first (fresh tombstone); the older set can't lift it.
	if err := s.Remove(ctx, "acme", "d1", "SHARED", "j", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("remove-first: %v", err)
	}
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "j", Value: 1, LastEventAt: attrBase}); err != nil {
		t.Fatalf("older set after remove: %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "j"); ok {
		t.Fatalf("an older set must not lift a newer removal tombstone")
	}
}

// A device deletion purges all the device's attribute rows and fences the device so a straggler set
// reordered after the deletion — even for a key that had NO row to tombstone — cannot resurrect a
// phantom value.
func TestDeviceAttributeStore_PurgeAndFence(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: attrBase}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Device deleted after the set.
	if err := s.PurgeDevice(ctx, "acme", "d1", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("purge must remove the device's attribute rows")
	}
	// A straggler set for the SAME key (older than the deletion) reordered after the purge: fenced.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: attrBase}); err != nil {
		t.Fatalf("straggler set (existing key): %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("a straggler set for a deleted device must be fenced")
	}
	// A straggler set for a NEVER-PROJECTED key (no row existed at delete, so nothing tombstoned it):
	// the fence — not a per-row tombstone — is what blocks it.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", AttrKey: "ghost", Value: 42, LastEventAt: attrBase.Add(30 * time.Minute)}); err != nil {
		t.Fatalf("straggler set (ghost key): %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SERVER", "ghost"); ok {
		t.Fatalf("a straggler set for a never-projected key of a deleted device must be fenced")
	}
	// A straggler REMOVAL for the deleted device is also fenced (no lingering tombstone insert).
	if err := s.Remove(ctx, "acme", "d1", "SHARED", "k", attrBase.Add(30*time.Minute)); err != nil {
		t.Fatalf("straggler removal: %v", err)
	}
	all, _ := s.LoadAll(ctx)
	if len(all) != 0 {
		t.Fatalf("a fenced device must have no live rows after straggler set/removal, got %+v", all)
	}
}

// Upsert's verify-after-write closes the check→insert TOCTOU against a concurrent PurgeDevice. The
// seam injects a full PurgeDevice (fence-write + row-sweep, the sweep finding nothing since our row
// is not inserted yet) into the window AFTER our pre-check passed but BEFORE our insert — the exact
// interleave that would otherwise leave a permanent phantom live row for the deleted device. The
// post-insert verify must detect the now-visible fence and sweep our straggler row. Without the
// verify step this test fails (the phantom survives).
func TestDeviceAttributeStore_VerifyAfterWriteSweepsConcurrentPurge(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	s.beforeInsertForTest = func() {
		if err := s.PurgeDevice(ctx, "acme", "d1", attrBase.Add(time.Hour)); err != nil {
			t.Errorf("injected concurrent purge: %v", err)
		}
		s.beforeInsertForTest = nil // fire once, so our own insert is not re-entered
	}
	// A straggler set (older than the injected deletion); no fence is visible at the pre-check.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 9, LastEventAt: attrBase}); err != nil {
		t.Fatalf("straggler upsert: %v", err)
	}
	if all, _ := s.LoadAll(ctx); len(all) != 0 {
		t.Fatalf("verify-after-write must sweep the phantom left by a concurrent purge, got %+v", all)
	}
	// And a genuine post-reuse set (strictly after the fence) still lands live.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 50, LastEventAt: attrBase.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("post-reuse set: %v", err)
	}
	if v, ok := find(t, s, "acme", "d1", "SHARED", "k"); !ok || v != 50 {
		t.Fatalf("a post-reuse set must land live past the fence, got v=%v ok=%v", v, ok)
	}
}

// Retry-after-error safety: if a prior Upsert attempt inserted a phantom row and then errored on its
// post-insert verify/sweep, persistBeforeAck retries the whole call. The retry now sees the fence at
// the pre-check — and must SWEEP the lingering phantom, not bare-return and ack the fact with the
// phantom still live.
func TestDeviceAttributeStore_FencedRetrySweepsPriorPhantom(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	if err := s.PurgeDevice(ctx, "acme", "d1", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	// The phantom a prior attempt's insert left (live, straggler t <= deletion) before erroring.
	phantom := &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 9, LastEventAt: attrBase}
	if err := s.rdb.DB(ctx).Create(phantom).Error; err != nil {
		t.Fatalf("seed phantom: %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "k"); !ok {
		t.Fatalf("precondition: phantom should be present")
	}
	// The retry: the same fenced straggler fact. Pre-check is fenced → must sweep the phantom.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 9, LastEventAt: attrBase}); err != nil {
		t.Fatalf("fenced retry upsert: %v", err)
	}
	if all, _ := s.LoadAll(ctx); len(all) != 0 {
		t.Fatalf("a fenced Upsert must sweep a prior phantom (retry-after-error safety), got %+v", all)
	}
}

// Token reuse: a device deleted, then a new device with the same token sets attributes with LATER
// timestamps — those are NOT fenced and land live; a redelivered OLD deletion cannot erase them.
func TestDeviceAttributeStore_TokenReuse(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	// Original device: set, then deleted.
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: attrBase}); err != nil {
		t.Fatalf("orig set: %v", err)
	}
	if err := s.PurgeDevice(ctx, "acme", "d1", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	// New device reuses the token; sets the same key with a later time (post-reuse).
	if err := s.Upsert(ctx, &DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 55, LastEventAt: attrBase.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("reuse set: %v", err)
	}
	if v, ok := find(t, s, "acme", "d1", "SHARED", "k"); !ok || v != 55 {
		t.Fatalf("a post-reuse set (later than the fence) must land live, got v=%v ok=%v", v, ok)
	}
	// A redelivered OLD deletion (same deletion time) must not erase the reused device's newer value.
	if err := s.PurgeDevice(ctx, "acme", "d1", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("redelivered purge: %v", err)
	}
	if v, ok := find(t, s, "acme", "d1", "SHARED", "k"); !ok || v != 55 {
		t.Fatalf("a redelivered old deletion must not erase a token-reused device's newer value, got v=%v ok=%v", v, ok)
	}
}

// The composite key keeps a device token that repeats across tenants distinct, and a purge only
// touches the matching tenant's rows.
func TestDeviceAttributeStore_CompositeKeyAndScopedPurge(t *testing.T) {
	s := newTestAttributeStore(t)
	ctx := context.Background()
	for _, tn := range []string{"acme", "beta"} {
		if err := s.Upsert(ctx, &DeviceAttribute{Tenant: tn, DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: attrBase}); err != nil {
			t.Fatalf("upsert %s: %v", tn, err)
		}
	}
	if err := s.PurgeDevice(ctx, "acme", "d1", attrBase.Add(time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := find(t, s, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("acme's row must be purged")
	}
	if v, ok := find(t, s, "beta", "d1", "SHARED", "k"); !ok || v != 100 {
		t.Fatalf("beta's same-token row must survive a scoped purge, got v=%v ok=%v", v, ok)
	}
}
