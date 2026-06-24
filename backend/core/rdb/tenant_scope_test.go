/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
	if err := db.AutoMigrate(&widget{}, &gadget{}); err != nil {
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
