// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newReadTestApi spins up an in-memory sqlite database with the tenant-scope
// callbacks registered and the base Event table migrated, then wraps it in an
// Api so the read path can be exercised exactly as production does.
func newReadTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	// Only the base Event table is needed to prove tenant-scoped reads; the
	// typed-table FKs are skipped here to keep the in-memory schema simple.
	if err := db.AutoMigrate(&Event{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// seedEvent inserts a base event row under the given tenant.
func seedEvent(t *testing.T, api *Api, tenant string, deviceId uint, occurred time.Time) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	ev := &Event{
		DeviceId:     deviceId,
		EventType:    esmodel.Location,
		OccurredTime: occurred,
		Source:       "test",
	}
	if err := api.RDB.DB(ctx).Create(ev).Error; err != nil {
		t.Fatalf("seed under %s failed: %v", tenant, err)
	}
}

// (a) A tenant-A context returns only tenant-A events for a device/time-range
// query, even though tenant B has matching rows.
func TestEvents_TenantScopedRead(t *testing.T) {
	api := newReadTestApi(t)
	base := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	// Tenant A: two rows for device 100 within the window.
	seedEvent(t, api, "A", 100, base.Add(1*time.Minute))
	seedEvent(t, api, "A", 100, base.Add(2*time.Minute))
	// Tenant A: one row for a different device, outside the device filter.
	seedEvent(t, api, "A", 999, base.Add(1*time.Minute))
	// Tenant B: a matching device/time row that must NOT leak into A's results.
	seedEvent(t, api, "B", 100, base.Add(1*time.Minute))

	ctxA := core.WithTenant(context.Background(), "A")
	deviceId := uint(100)
	start := base
	end := base.Add(10 * time.Minute)
	results, err := api.Events(ctxA, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		DeviceId:   &deviceId,
		StartTime:  &start,
		EndTime:    &end,
	})
	if err != nil {
		t.Fatalf("Events query under A failed: %v", err)
	}
	if len(results.Results) != 2 {
		t.Fatalf("expected 2 tenant-A rows for device 100, got %d (%+v)", len(results.Results), results.Results)
	}
	for _, ev := range results.Results {
		if ev.TenantId != "A" {
			t.Fatalf("tenant-A read leaked a row with tenant_id=%q", ev.TenantId)
		}
		if ev.DeviceId != 100 {
			t.Fatalf("device filter leaked device %d", ev.DeviceId)
		}
	}
	if results.Pagination.TotalRecords != 2 {
		t.Fatalf("expected total records 2, got %d", results.Pagination.TotalRecords)
	}
}

// (b) A read with NO tenant in context must fail closed with core.ErrNoTenant.
func TestEvents_FailsClosedWithoutTenant(t *testing.T) {
	api := newReadTestApi(t)
	// Seed a row so a non-failing query would actually return data.
	seedEvent(t, api, "A", 100, time.Now())

	_, err := api.Events(context.Background(), EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
	})
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant on read without tenant, got %v", err)
	}
}
