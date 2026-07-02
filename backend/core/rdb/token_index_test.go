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

// tokenThing is the shape CreateTenantTokenIndex targets: a token-referenced,
// tenant-scoped, soft-deletable entity (the standard registry-entity composition).
type tokenThing struct {
	gorm.Model
	TenantScoped
	TokenReference
	Name string
}

func newTokenIndexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.AutoMigrate(&tokenThing{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := CreateTenantTokenIndex(db, &tokenThing{}); err != nil {
		t.Fatalf("create index: %v", err)
	}
	// Idempotent — a re-run (e.g. re-applied migration) must not error.
	if err := CreateTenantTokenIndex(db, &tokenThing{}); err != nil {
		t.Fatalf("create index (2nd call must be a no-op): %v", err)
	}
	return db
}

func create(t *testing.T, db *gorm.DB, ctx context.Context, token string) error {
	t.Helper()
	return db.WithContext(ctx).Create(&tokenThing{TokenReference: TokenReference{Token: token}}).Error
}

// The per-tenant partial unique index: a token is unique within a tenant among
// live rows only — so tenants never collide and a deleted token frees for reuse.
func TestCreateTenantTokenIndex(t *testing.T) {
	db := newTokenIndexDB(t)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")

	// Same token under two tenants — allowed (per-tenant, not global).
	if err := create(t, db, ctxA, "x"); err != nil {
		t.Fatalf("A create x: %v", err)
	}
	if err := create(t, db, ctxB, "x"); err != nil {
		t.Fatalf("cross-tenant token reuse must be allowed, got: %v", err)
	}

	// A second LIVE "x" under tenant A — rejected.
	if err := create(t, db, ctxA, "x"); err == nil {
		t.Fatalf("duplicate live token within a tenant must be rejected")
	}

	// After a soft-delete, "x" is reusable under tenant A.
	if err := db.WithContext(ctxA).Where("token = ?", "x").Delete(&tokenThing{}).Error; err != nil {
		t.Fatalf("A soft-delete x: %v", err)
	}
	if err := create(t, db, ctxA, "x"); err != nil {
		t.Fatalf("token must be reusable after soft-delete, got: %v", err)
	}
}
