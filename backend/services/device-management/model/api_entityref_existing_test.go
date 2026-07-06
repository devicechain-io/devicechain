// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func newExistingRefsTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// ExistingEntityIds returns exactly the subset of ids that resolve to a live
// entity of the given type in the tenant — the primitive the reconciliation sweep
// uses to find orphaned anchors (ADR-044 decision 3).
func TestExistingEntityIds(t *testing.T) {
	api := newExistingRefsTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	d1 := &Device{}
	d1.Token = "dev-1"
	d2 := &Device{}
	d2.Token = "dev-2"
	for _, d := range []*Device{d1, d2} {
		if err := api.RDB.DB(ctx).Create(d).Error; err != nil {
			t.Fatalf("seed device: %v", err)
		}
	}

	// A missing id (99999) is dropped; the two live ids survive, order-independent.
	got, err := api.ExistingEntityIds(ctx, "device", []uint{d1.ID, 99999, d2.ID})
	assert.NoError(t, err)
	assert.ElementsMatch(t, []uint{d1.ID, d2.ID}, got)

	// Empty ids short-circuit to an empty slice (no query).
	got, err = api.ExistingEntityIds(ctx, "device", nil)
	assert.NoError(t, err)
	assert.Empty(t, got)

	// An unknown entity type is an error.
	_, err = api.ExistingEntityIds(ctx, "widget", []uint{1})
	assert.Error(t, err)
}
