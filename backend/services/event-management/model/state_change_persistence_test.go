// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newStateChangeTestApi builds an in-memory sqlite Api with the base events table,
// the state_change_events table, and the idempotency UNIQUE index that production
// gets from NewStateChangeEventsTable — so CreateStateChangeEvents' ON CONFLICT is
// exercised exactly as deployed (the production migration is Postgres/Timescale-
// qualified, so the index DDL is restated here for sqlite).
func newStateChangeTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Event{}); err != nil {
		t.Fatalf("failed to migrate events: %v", err)
	}
	// The base events natural-key PK lives in the Timescale migration, not the live
	// struct tags; restate it for sqlite so upsertParentEvents' ON CONFLICT resolves.
	if err := db.Exec(`CREATE UNIQUE INDEX idx_events_natural_key ` +
		`ON events (tenant_id, device_token, event_type, occurred_time);`).Error; err != nil {
		t.Fatalf("failed to create events natural-key index: %v", err)
	}
	if err := db.AutoMigrate(&StateChangeEvent{}); err != nil {
		t.Fatalf("failed to migrate state_change_events: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX uq_state_change_events_idem ` +
		`ON state_change_events (tenant_id, device_token, occurred_time, state, session_id);`).Error; err != nil {
		t.Fatalf("failed to create idempotency index: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

func scReq(device string, occurred time.Time, state string, session uint64) *StateChangeEventCreateRequest {
	return &StateChangeEventCreateRequest{
		Event: Event{
			DeviceToken:  device,
			EventType:    esmodel.StateChange,
			OccurredTime: occurred,
			Source:       "lwm2m",
		},
		State:     state,
		Reason:    "test",
		SessionId: session,
	}
}

func countStateChanges(t *testing.T, api *Api, ctx context.Context, device string) int64 {
	t.Helper()
	var n int64
	if err := api.RDB.DB(ctx).Model(&StateChangeEvent{}).
		Where("device_token = ?", device).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestStateChangePersistIdempotency proves the append-only presence history persists a
// connect/disconnect edge AND that the idempotency unique index makes a JetStream
// redelivery a no-op — a StateChange carries no AltId, so this index is the ONLY dedup.
func TestStateChangePersistIdempotency(t *testing.T) {
	api := newStateChangeTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	// A first CONNECT persists one row.
	if _, _, err := api.CreateStateChangeEvents(ctx, api.RDB.DB(ctx), []*StateChangeEventCreateRequest{scReq("d1", t0, "CONNECTED", 100)}); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if got := countStateChanges(t, api, ctx, "d1"); got != 1 {
		t.Fatalf("after first connect got %d rows, want 1", got)
	}

	// A redelivery of the SAME edge (tenant/device/time/state/session) is dropped by the
	// idempotency index — not a duplicate presence row (which would read as phantom flapping).
	// RowsAffected must be 0: that is the signal the worker uses to skip anchor persistence.
	_, affected, err := api.CreateStateChangeEvents(ctx, api.RDB.DB(ctx), []*StateChangeEventCreateRequest{scReq("d1", t0, "CONNECTED", 100)})
	if err != nil {
		t.Fatalf("redelivery must not error (ON CONFLICT DO NOTHING): %v", err)
	}
	if affected != 0 {
		t.Fatalf("redelivery reported %d rows affected, want 0 (the skip-anchors signal)", affected)
	}
	if got := countStateChanges(t, api, ctx, "d1"); got != 1 {
		t.Fatalf("redelivery duplicated a presence row: got %d, want 1", got)
	}

	// A birth+death at ONE instant differ by state — BOTH survive (the equal-stamp pair the
	// projection legitimizes; a unique NATURAL key would have dropped one).
	if _, _, err := api.CreateStateChangeEvents(ctx, api.RDB.DB(ctx), []*StateChangeEventCreateRequest{scReq("d1", t0, "DISCONNECTED", 100)}); err != nil {
		t.Fatalf("equal-stamp disconnect: %v", err)
	}
	if got := countStateChanges(t, api, ctx, "d1"); got != 2 {
		t.Fatalf("equal-stamp birth+death did not both persist: got %d, want 2", got)
	}

	// A late higher-session DISCONNECT echo (same time+state, different session) is RETAINED
	// for audit — the projection freezes LastDisconnectTime, but the history keeps the row.
	if _, _, err := api.CreateStateChangeEvents(ctx, api.RDB.DB(ctx), []*StateChangeEventCreateRequest{scReq("d1", t0, "DISCONNECTED", 200)}); err != nil {
		t.Fatalf("late echo: %v", err)
	}
	if got := countStateChanges(t, api, ctx, "d1"); got != 3 {
		t.Fatalf("late higher-session echo was not retained: got %d, want 3", got)
	}
}

// TestStateChangeTenantIsolation proves the idempotency key is per-tenant (device tokens
// are only per-tenant unique, ADR-042): two tenants' identical edges never cross-collide.
func TestStateChangeTenantIsolation(t *testing.T) {
	api := newStateChangeTestApi(t)
	t0 := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")

	if _, _, err := api.CreateStateChangeEvents(ctxA, api.RDB.DB(ctxA), []*StateChangeEventCreateRequest{scReq("shared", t0, "CONNECTED", 100)}); err != nil {
		t.Fatalf("tenant A: %v", err)
	}
	if _, _, err := api.CreateStateChangeEvents(ctxB, api.RDB.DB(ctxB), []*StateChangeEventCreateRequest{scReq("shared", t0, "CONNECTED", 100)}); err != nil {
		t.Fatalf("tenant B identical edge must not collide with A: %v", err)
	}
	if got := countStateChanges(t, api, ctxA, "shared"); got != 1 {
		t.Fatalf("tenant A sees %d rows, want 1 (B's write must not suppress or leak)", got)
	}
	if got := countStateChanges(t, api, ctxB, "shared"); got != 1 {
		t.Fatalf("tenant B sees %d rows, want 1", got)
	}
}
