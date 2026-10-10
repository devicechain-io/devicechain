// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newIdempotencyTestApi builds an in-memory sqlite Api with the Event table and
// the (tenant_id, alt_id, occurred_time) partial unique index that production
// gets from NewAltIdIdempotencyIndex — so both the dedup check and the unique
// backstop are exercised exactly as deployed (the production migration is
// Postgres/Timescale-qualified, so the index DDL is restated here for sqlite).
func newIdempotencyTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Event{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_events_tenant_device_alt_id ` +
		`ON events (tenant_id, device_token, alt_id, occurred_time) WHERE alt_id IS NOT NULL;`).Error; err != nil {
		t.Fatalf("failed to create dedup index: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// seedAltEvent inserts a base event carrying an alternateId under the tenant.
func seedAltEvent(t *testing.T, api *Api, tenant, altId string, occurred time.Time) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	ev := &Event{
		DeviceToken:  "device-100",
		EventType:    esmodel.Measurement,
		OccurredTime: occurred,
		Source:       "test",
		AltId:        sql.NullString{String: altId, Valid: true},
	}
	if err := api.RDB.DB(ctx).Create(ev).Error; err != nil {
		t.Fatalf("seed alt event failed: %v", err)
	}
}

// EventExistsByAltId is the dedup probe: it sees a persisted alternateId for the
// same tenant + occurred_time, and is blind to a different id, a different time,
// and another tenant's identical id (tenant isolation).
func TestEventExistsByAltId(t *testing.T) {
	api := newIdempotencyTestApi(t)
	occurred := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	seedAltEvent(t, api, "A", "evt-1", occurred)

	ctxA := core.WithTenant(context.Background(), "A")
	db := api.RDB.Database

	cases := []struct {
		name     string
		ctx      context.Context
		device   string
		altId    string
		occurred time.Time
		want     bool
	}{
		{"same tenant+device+id+time hits", ctxA, "device-100", "evt-1", occurred, true},
		{"another device of the tenant misses", ctxA, "device-101", "evt-1", occurred, false},
		{"different id misses", ctxA, "device-100", "evt-2", occurred, false},
		{"different time misses", ctxA, "device-100", "evt-1", occurred.Add(time.Second), false},
		{"other tenant cannot see A's id", core.WithTenant(context.Background(), "B"), "device-100", "evt-1", occurred, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := api.EventExistsByAltId(tc.ctx, db, tc.device, tc.altId, tc.occurred)
			if err != nil {
				t.Fatalf("EventExistsByAltId: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// The partial unique index rejects a duplicate (tenant, alt_id, occurred_time) —
// the race backstop behind the dedup check — while leaving events without an
// alternateId (NULL) entirely unconstrained.
func TestAltIdUniqueIndexBackstop(t *testing.T) {
	api := newIdempotencyTestApi(t)
	occurred := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	// A second row with the same (tenant, alt_id, occurred_time) is rejected.
	seedAltEvent(t, api, "A", "evt-1", occurred)
	ctxA := core.WithTenant(context.Background(), "A")
	dup := &Event{DeviceToken: "device-100", EventType: esmodel.Alert, OccurredTime: occurred, Source: "dup",
		AltId: sql.NullString{String: "evt-1", Valid: true}}
	if err := api.RDB.DB(ctxA).Create(dup).Error; err == nil {
		t.Fatal("expected the partial unique index to reject a duplicate alternateId")
	}

	// The same alt_id and instant from ANOTHER device of the tenant is a different message,
	// not a redelivery, and is stored.
	other := &Event{DeviceToken: "device-101", EventType: esmodel.Alert, OccurredTime: occurred, Source: "other",
		AltId: sql.NullString{String: "evt-1", Valid: true}}
	if err := api.RDB.DB(ctxA).Create(other).Error; err != nil {
		t.Fatalf("another device's event with the same alternateId must be stored: %v", err)
	}

	// The same alt_id under a different tenant is allowed (tenant_id is part of
	// the key), and two events with no alt_id never collide.
	seedAltEvent(t, api, "B", "evt-1", occurred)
	for i := 0; i < 2; i++ {
		ev := &Event{DeviceToken: "device-200", EventType: esmodel.Location, OccurredTime: occurred, Source: "no-alt"}
		if err := api.RDB.DB(ctxA).Create(ev).Error; err != nil {
			t.Fatalf("event without an alternateId must not be constrained: %v", err)
		}
	}
}

// EventsExistByAltId answers each key on its own instant, in one query. Stored: (a, t) and
// (b, t+1s); asked [(a,t), (a,t+1s), (b,t+1s), (c,t)]: a key whose alternate id is stored at
// ANOTHER instant in the window is not reported, so a probe that matched alternate ids alone
// and bounded the instants only by the window would answer [true, true, true, false]. Under
// another tenant every answer is false: the tenant predicate applies to the batch read too.
func TestEventsExistByAltIdAnswersEachKeyOnItsOwnInstant(t *testing.T) {
	api := newIdempotencyTestApi(t)
	at := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	seedAltEvent(t, api, "A", "a", at)
	seedAltEvent(t, api, "A", "b", at.Add(time.Second))
	keys := []AltIdKey{
		{DeviceToken: "device-100", AltId: "a", OccurredTime: at},
		{DeviceToken: "device-100", AltId: "a", OccurredTime: at.Add(time.Second)},
		{DeviceToken: "device-100", AltId: "b", OccurredTime: at.Add(time.Second)},
		{DeviceToken: "device-100", AltId: "c", OccurredTime: at},
	}
	for _, tc := range []struct {
		tenant string
		want   []bool
	}{
		{"A", []bool{true, false, true, false}},
		{"B", []bool{false, false, false, false}},
	} {
		t.Run("tenant "+tc.tenant, func(t *testing.T) {
			ctx := core.WithTenant(context.Background(), tc.tenant)
			got, err := api.EventsExistByAltId(ctx, api.RDB.Database, keys)
			if err != nil {
				t.Fatalf("EventsExistByAltId: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d answers for %d keys", len(got), len(keys))
			}
			for i := range keys {
				if got[i] != tc.want[i] {
					t.Errorf("key %d (%s at %s): got %v, want %v", i, keys[i].AltId,
						keys[i].OccurredTime.Format(time.RFC3339), got[i], tc.want[i])
				}
			}
		})
	}
}

// Two instants that differ below a microsecond are one key to the event store, which keeps
// microseconds; two a microsecond apart are two.
func TestAnAlternateIdKeyMatchesAtMicrosecondResolution(t *testing.T) {
	at := time.Date(2026, 6, 25, 12, 0, 0, 1000, time.UTC)
	k := AltIdKey{DeviceToken: "d", AltId: "a", OccurredTime: at}
	if k.Match() != (AltIdKey{DeviceToken: "d", AltId: "a", OccurredTime: at.Add(500 * time.Nanosecond)}).Match() {
		t.Error("two instants 500ns apart within one microsecond are different keys; want the same")
	}
	if k.Match() == (AltIdKey{DeviceToken: "d", AltId: "a", OccurredTime: at.Add(time.Microsecond)}).Match() {
		t.Error("two instants a microsecond apart are the same key; want different")
	}
	if k.Match() == (AltIdKey{DeviceToken: "d", AltId: "b", OccurredTime: at}).Match() {
		t.Error("two alternate ids at one instant are the same key; want different")
	}
	if k.Match() == (AltIdKey{DeviceToken: "e", AltId: "a", OccurredTime: at}).Match() {
		t.Error("two devices with one alternate id at one instant are the same key; want different")
	}
}
