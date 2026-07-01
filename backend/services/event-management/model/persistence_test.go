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
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// newPersistenceTestApi builds an in-memory sqlite Api with the base events table
// and the measurement_events child, wired by the same (device_id, event_type,
// occurred_time) foreign key as production. Foreign keys are enforced so the test
// proves the parent event is written before its children; the unique index on the
// natural key is both the FK target and the ON CONFLICT arbiter used by
// upsertParentEvents (production gets the composite primary key from the
// Postgres/Timescale migration, restated here for sqlite).
func newPersistenceTestApi(t *testing.T) *Api {
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
	if err := db.Exec(`CREATE UNIQUE INDEX idx_events_natural_key ` +
		`ON events (device_id, event_type, occurred_time);`).Error; err != nil {
		t.Fatalf("failed to create natural-key index: %v", err)
	}
	if err := db.AutoMigrate(&MeasurementEvent{}, &EventAnchor{}); err != nil {
		t.Fatalf("failed to migrate child tables: %v", err)
	}
	if err := db.Exec(`PRAGMA foreign_keys = ON;`).Error; err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// A device assigned to several targets records one anchor row per target for each
// reading, so the same measurement is found by every dimension — the capability
// the single-anchor schema could not express (ADR-013 addendum 2026-07-01).
func TestMeasurementsQueryableByEachAnchor(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 1, 20, 0, 0, 0, time.UTC)

	parent := Event{DeviceId: 4, EventType: esmodel.Measurement, OccurredTime: occurred, Source: "http1"}
	if _, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
		[]*MeasurementEventCreateRequest{{Event: parent, Name: "temperature", Value: f64(21.5)}}); err != nil {
		t.Fatalf("CreateMeasurementEvents: %v", err)
	}
	// The device is assigned to a customer AND an area: one anchor row per target.
	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		{DeviceId: 4, EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorId: 3},
		{DeviceId: 4, EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "area", AnchorId: 9},
	}); err != nil {
		t.Fatalf("CreateEventAnchors: %v", err)
	}

	byAnchor := func(atype string, aid uint) int {
		res, err := api.MeasurementEvents(ctx, EventSearchCriteria{AnchorType: &atype, AnchorId: &aid})
		if err != nil {
			t.Fatalf("MeasurementEvents(%s,%d): %v", atype, aid, err)
		}
		return len(res.Results)
	}

	assert.Equal(t, 1, byAnchor("customer", 3), "found by its customer anchor")
	assert.Equal(t, 1, byAnchor("area", 9), "found by its area anchor")
	assert.Equal(t, 0, byAnchor("asset", 1), "not found by an unassigned dimension")
}

// A measurement message carrying several metrics at one instant is one parent
// event with several child rows. CreateMeasurementEvents must write exactly one
// parent (the shared natural key is deduped, not re-upserted with the invalid
// composite ON CONFLICT that broke every insert), the children must reference it,
// and a redelivery must not duplicate the parent.
func TestCreateMeasurementEventsWritesParentAndChildren(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 1, 19, 12, 26, 0, time.UTC)

	parent := Event{
		DeviceId:      4,
		EventType:     esmodel.Measurement,
		OccurredTime:  occurred,
		Source:        "http1",
		ProcessedTime: occurred,
	}
	requests := []*MeasurementEventCreateRequest{
		{Event: parent, Name: "temperature", Value: f64(21.5)},
		{Event: parent, Name: "humidity", Value: f64(48)},
	}

	created, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), requests)
	if err != nil {
		t.Fatalf("CreateMeasurementEvents: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("expected 2 created children, got %d", len(created))
	}

	// One shared parent event, not one per child.
	var parents int64
	if err := api.RDB.DB(ctx).Model(&Event{}).Count(&parents).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if parents != 1 {
		t.Fatalf("expected 1 parent event, got %d", parents)
	}

	// Both children persisted against the parent's natural key.
	var children []MeasurementEvent
	if err := api.RDB.DB(ctx).Order("name").Find(&children).Error; err != nil {
		t.Fatalf("load children: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 measurement rows, got %d", len(children))
	}
	for _, c := range children {
		if c.DeviceId != 4 || c.EventType != esmodel.Measurement || !c.OccurredTime.Equal(occurred) {
			t.Fatalf("child fk columns do not match parent: %+v", c)
		}
	}
	if children[0].Name != "humidity" || children[1].Name != "temperature" {
		t.Fatalf("unexpected child names: %s, %s", children[0].Name, children[1].Name)
	}

	// A redelivery of the same message re-presents the same parent key: the parent
	// is not duplicated (ON CONFLICT DO NOTHING).
	if _, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), requests); err != nil {
		t.Fatalf("redelivery CreateMeasurementEvents: %v", err)
	}
	if err := api.RDB.DB(ctx).Model(&Event{}).Count(&parents).Error; err != nil {
		t.Fatalf("recount events: %v", err)
	}
	if parents != 1 {
		t.Fatalf("expected parent event to stay deduped at 1, got %d", parents)
	}
}

func f64(v float64) *float64 { return &v }
