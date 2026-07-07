// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// extIdThing is the shape CreateTenantExternalIdIndex targets: a tenant-scoped,
// soft-deletable entity carrying an optional external id (ADR-049).
type extIdThing struct {
	gorm.Model
	TenantScoped
	ExternalReference
}

func newExternalIdIndexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.AutoMigrate(&extIdThing{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := CreateTenantExternalIdIndex(db, &extIdThing{}); err != nil {
		t.Fatalf("create index: %v", err)
	}
	// Idempotent — a re-run (e.g. re-applied migration) must not error.
	if err := CreateTenantExternalIdIndex(db, &extIdThing{}); err != nil {
		t.Fatalf("create index (2nd call must be a no-op): %v", err)
	}
	return db
}

func createExtId(t *testing.T, db *gorm.DB, ctx context.Context, externalId *string) error {
	t.Helper()
	return db.WithContext(ctx).Create(&extIdThing{
		ExternalReference: ExternalReference{ExternalId: NullStrOf(externalId)},
	}).Error
}

// The per-tenant partial unique index on the external id: unique within a tenant
// among live rows that CARRY an id, cross-tenant reuse allowed, soft-delete frees it.
func TestCreateTenantExternalIdIndex(t *testing.T) {
	db := newExternalIdIndexDB(t)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")
	vin := "1HGCM82633A004352"

	// Same VIN under two tenants — allowed (per-tenant, not global).
	if err := createExtId(t, db, ctxA, &vin); err != nil {
		t.Fatalf("A create vin: %v", err)
	}
	if err := createExtId(t, db, ctxB, &vin); err != nil {
		t.Fatalf("cross-tenant external-id reuse must be allowed, got: %v", err)
	}

	// A second LIVE row with the same VIN under tenant A — rejected.
	if err := createExtId(t, db, ctxA, &vin); err == nil {
		t.Fatalf("duplicate live external id within a tenant must be rejected")
	}

	// After a soft-delete, the VIN is reusable under tenant A.
	if err := db.WithContext(ctxA).Where("external_id = ?", vin).Delete(&extIdThing{}).Error; err != nil {
		t.Fatalf("A soft-delete vin: %v", err)
	}
	if err := createExtId(t, db, ctxA, &vin); err != nil {
		t.Fatalf("external id must be reusable after soft-delete, got: %v", err)
	}
}

// The whole point of the external_id IS NOT NULL predicate: the many rows without
// an external id must coexist under one tenant — NULLs are not part of the
// uniqueness set. (A bare (tenant_id, external_id) unique index would still allow
// this on Postgres/SQLite since NULLs compare distinct, but the explicit predicate
// makes the intent — "unique only when present" — a guarantee, not an accident.)
func TestExternalIdIndexAllowsManyNulls(t *testing.T) {
	db := newExternalIdIndexDB(t)
	ctxA := core.WithTenant(context.Background(), "A")

	for i := 0; i < 3; i++ {
		if err := createExtId(t, db, ctxA, nil); err != nil {
			t.Fatalf("row %d with no external id must be allowed, got: %v", i, err)
		}
	}
}
