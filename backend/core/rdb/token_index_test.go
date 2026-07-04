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

// credThing mirrors DeviceCredential's uniqueness shape: a soft-deletable,
// tenant-scoped entity whose (tenant_id, type, id) tuple must be unique among live
// rows only (ADR-014) — per-tenant, since resolution always runs under a tenant.
type credThing struct {
	gorm.Model
	TenantId       string
	CredentialType string
	CredentialId   string
}

// CreatePartialUniqueIndex enforces uniqueness among LIVE rows only: a duplicate
// live tuple is rejected, the same tuple is allowed across tenants, a soft-deleted
// row frees the tuple for reuse, and the creation is idempotent.
func TestCreatePartialUniqueIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&credThing{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mk := func() error {
		return CreatePartialUniqueIndex(db, &credThing{}, "uix_cred_thing_lookup", "tenant_id", "credential_type", "credential_id")
	}
	if err := mk(); err != nil {
		t.Fatalf("create index: %v", err)
	}
	// Idempotent — a re-applied migration must not error.
	if err := mk(); err != nil {
		t.Fatalf("create index (2nd call must be a no-op): %v", err)
	}

	row := func(tenant string) *credThing {
		return &credThing{TenantId: tenant, CredentialType: "mqtt", CredentialId: "dev-1"}
	}

	if err := db.Create(row("A")).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// The same (type, id) under a different tenant — allowed (per-tenant, not global).
	if err := db.Create(row("B")).Error; err != nil {
		t.Fatalf("cross-tenant credential reuse must be allowed, got: %v", err)
	}
	// A second LIVE (A, mqtt, dev-1) — rejected.
	if err := db.Create(row("A")).Error; err == nil {
		t.Fatalf("duplicate live credential tuple within a tenant must be rejected")
	}
	// After a soft-delete the tuple is reusable under tenant A.
	if err := db.Where("tenant_id = ? and credential_type = ? and credential_id = ?", "A", "mqtt", "dev-1").
		Delete(&credThing{}).Error; err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	if err := db.Create(row("A")).Error; err != nil {
		t.Fatalf("tuple must be reusable after soft-delete, got: %v", err)
	}
}
