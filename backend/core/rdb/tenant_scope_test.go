// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Tenant-scoped model used by the isolation tests.
type widget struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
}

// Model WITHOUT TenantScoped, must be unaffected by the callbacks.
type gadget struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// Tenant-scoped model WITH soft-delete, to prove that an Unscoped (hard) delete —
// which disables the soft-delete clause — still has the tenant predicate injected
// and so can never cross a tenant boundary. This is the load-bearing guarantee
// behind hard deletes (e.g. dashboard-management, device-management, iam).
type sprocket struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name      string
	DeletedAt gorm.DeletedAt
}

// newTestDB spins up an in-memory pure-Go sqlite database with the tenant-scope
// callbacks registered.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&widget{}, &gadget{}, &sprocket{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

// (a) Writing rows under tenant A then B, a query with tenant A returns only A's rows.
func TestTenantIsolation_QueryReturnsOnlyOwnRows(t *testing.T) {
	db := newTestDB(t)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")

	if err := db.WithContext(ctxA).Create(&widget{Name: "a1"}).Error; err != nil {
		t.Fatalf("create under A failed: %v", err)
	}
	if err := db.WithContext(ctxA).Create(&widget{Name: "a2"}).Error; err != nil {
		t.Fatalf("create under A failed: %v", err)
	}
	if err := db.WithContext(ctxB).Create(&widget{Name: "b1"}).Error; err != nil {
		t.Fatalf("create under B failed: %v", err)
	}

	var found []widget
	if err := db.WithContext(ctxA).Find(&found).Error; err != nil {
		t.Fatalf("query under A failed: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 rows for tenant A, got %d (%+v)", len(found), found)
	}
	for _, w := range found {
		if w.TenantId != "A" {
			t.Fatalf("tenant A query leaked a row with tenant_id=%q", w.TenantId)
		}
	}

	// Sanity: tenant B sees only its row, and the create stamped tenant_id.
	var bRows []widget
	if err := db.WithContext(ctxB).Find(&bRows).Error; err != nil {
		t.Fatalf("query under B failed: %v", err)
	}
	if len(bRows) != 1 || bRows[0].TenantId != "B" {
		t.Fatalf("expected 1 row for tenant B with tenant_id=B, got %+v", bRows)
	}
}

// (b) A create with no tenant in context fails with ErrNoTenant.
func TestTenantIsolation_CreateWithoutTenantFails(t *testing.T) {
	db := newTestDB(t)
	err := db.WithContext(context.Background()).Create(&widget{Name: "orphan"}).Error
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant on create without tenant, got %v", err)
	}
}

// (c) A query with no tenant in context on a tenant-scoped model fails closed.
func TestTenantIsolation_QueryWithoutTenantFails(t *testing.T) {
	db := newTestDB(t)
	// Seed a row so a non-failing query would actually return data.
	ctxA := core.WithTenant(context.Background(), "A")
	if err := db.WithContext(ctxA).Create(&widget{Name: "a1"}).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	var found []widget
	err := db.WithContext(context.Background()).Find(&found).Error
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant on query without tenant, got %v (rows=%+v)", err, found)
	}
}

// (d) A model WITHOUT TenantScoped is unaffected and works with no tenant.
func TestTenantIsolation_NonScopedModelUnaffected(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WithContext(ctx).Create(&gadget{Name: "g1"}).Error; err != nil {
		t.Fatalf("create non-scoped model failed: %v", err)
	}
	var found []gadget
	if err := db.WithContext(ctx).Find(&found).Error; err != nil {
		t.Fatalf("query non-scoped model failed: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 gadget, got %d", len(found))
	}
}

// (e) A deliberate system context (core.WithSystemContext) reads across tenants:
// the sanctioned bypass used by the login lookup. It must neither fail closed nor
// inject a tenant predicate.
func TestTenantIsolation_SystemContextReadsAcrossTenants(t *testing.T) {
	db := newTestDB(t)
	if err := db.WithContext(core.WithTenant(context.Background(), "A")).Create(&widget{Name: "a1"}).Error; err != nil {
		t.Fatalf("seed A failed: %v", err)
	}
	if err := db.WithContext(core.WithTenant(context.Background(), "B")).Create(&widget{Name: "b1"}).Error; err != nil {
		t.Fatalf("seed B failed: %v", err)
	}

	sysctx := core.WithSystemContext(context.Background())
	var found []widget
	if err := db.WithContext(sysctx).Find(&found).Error; err != nil {
		t.Fatalf("system-context query failed (should bypass, not fail closed): %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected system context to see all 2 rows across tenants, got %d (%+v)", len(found), found)
	}
}

// (f) The system bypass is opt-in: WithSystemContext must be explicitly set. A
// plain context (no tenant, no system marker) still fails closed — proving the
// bypass cannot be triggered by omission.
func TestTenantIsolation_SystemContextIsOptIn(t *testing.T) {
	db := newTestDB(t)
	var found []widget
	if err := db.WithContext(context.Background()).Find(&found).Error; !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("a plain context must fail closed, not silently bypass; got %v", err)
	}
}

// An Unscoped (hard) delete still gets the tenant predicate: tenant A deleting by a
// non-token column that also matches tenant B's row must remove ONLY A's row. This
// guards the security property that hard deletes (Unscoped, used to free tokens on
// delete) can never cross a tenant boundary — Unscoped disables the soft-delete
// clause, NOT the tenant-scope callback.
func TestTenantIsolation_UnscopedDeleteStaysInTenant(t *testing.T) {
	db := newTestDB(t)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")

	// Both tenants hold a row with the same name (legal — they're isolated).
	if err := db.WithContext(ctxA).Create(&sprocket{Name: "shared"}).Error; err != nil {
		t.Fatalf("create under A failed: %v", err)
	}
	if err := db.WithContext(ctxB).Create(&sprocket{Name: "shared"}).Error; err != nil {
		t.Fatalf("create under B failed: %v", err)
	}

	// Tenant A hard-deletes by name; the injected tenant predicate must confine it.
	res := db.WithContext(ctxA).Unscoped().Where("name = ?", "shared").Delete(&sprocket{})
	if res.Error != nil {
		t.Fatalf("unscoped delete under A failed: %v", res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("expected exactly A's 1 row deleted, got %d (delete crossed tenant boundary?)", res.RowsAffected)
	}

	// Tenant B's row must be untouched.
	var bCount int64
	if err := db.WithContext(ctxB).Model(&sprocket{}).Where("name = ?", "shared").Count(&bCount).Error; err != nil {
		t.Fatalf("count under B failed: %v", err)
	}
	if bCount != 1 {
		t.Fatalf("tenant B's row must survive A's delete; got count=%d", bCount)
	}

	// A's row is truly gone (hard delete): invisible even to an Unscoped read under A.
	var aCount int64
	if err := db.WithContext(ctxA).Unscoped().Model(&sprocket{}).Count(&aCount).Error; err != nil {
		t.Fatalf("unscoped count under A failed: %v", err)
	}
	if aCount != 0 {
		t.Fatalf("A's row should be hard-deleted (no soft-deleted remnant); got count=%d", aCount)
	}
}
