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
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newPayloadIdentityTestApi carries the production keys for the base event AND for the
// payload tables: each child dedups on (tenant_id, payload_id, occurred_time), which is
// what makes a redelivery a no-op for the rows the base-event key cannot cover.
func newPayloadIdentityTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(
		&Event{}, &LocationEvent{}, &MeasurementEvent{}, &AlertEvent{}), "migrate")
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_events_identity `+
		`ON events (tenant_id, event_id, occurred_time);`).Error, "event identity key")
	for _, table := range []string{"location_events", "measurement_events", "alert_events"} {
		require.NoError(t, db.Exec(
			`CREATE UNIQUE INDEX uq_`+table+`_idem ON `+table+
				` (tenant_id, payload_id, occurred_time);`).Error, "payload identity key on "+table)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// noAltIdEvent builds a base event carrying NO alternateId — the shape lwm2m-ingest and
// sparkplug-ingest actually produce, since neither service sets AltId anywhere.
func noAltIdEvent(tenant, device string, occurred time.Time, payload string) Event {
	ev := Event{
		DeviceToken:  device,
		EventType:    esmodel.Measurement,
		OccurredTime: occurred,
		Source:       "lwm2m",
	}
	ev.EventId = DeriveEventId(tenant, &ev, []byte(payload))
	return ev
}

// TestRedeliveryOfANoAltIdEventDoesNotDuplicatePayloadRows is the regression test for the
// defect this change fixes.
//
// 🔴 THE EXPOSURE IS NOT HYPOTHETICAL AND IT IS NOT SMALL. A grep for AltId across
// lwm2m-ingest and sparkplug-ingest returns ZERO non-test hits: neither transport sets an
// alternateId, so every event they produce has alt_id NULL. PersistEvent's dedup probe is
// keyed on alt_id, so for those two transports it never engages at all — and redelivery is
// a designed-for path (ADR-022 at-least-once). The base event converged correctly on its
// content digest, but the payload rows inserted unconditionally, so a redelivery left ONE
// envelope owning N copies of its own measurements.
//
// state_change_events already solved exactly this, and only for itself: its
// uq_state_change_events_idem index exists because "a StateChange carries no AltId". The
// same reasoning applies verbatim to location, measurement and alert rows, and was not
// carried across — the fix was applied to the instance rather than to the shape.
func TestRedeliveryOfANoAltIdEventDoesNotDuplicatePayloadRows(t *testing.T) {
	api := newPayloadIdentityTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := noAltIdEvent("acme", "device-1", occurred, "temperature=21.5")
	require.False(t, ev.AltId.Valid, "negative control: this event must carry NO alternateId, "+
		"otherwise PersistEvent's probe would catch the redelivery earlier and this test "+
		"would be exercising a path that was never broken")

	// Three deliveries of the same message, as at-least-once permits.
	for i := 0; i < 3; i++ {
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
			[]*MeasurementEventCreateRequest{{Event: ev,
				EntryOccurredTime: ev.OccurredTime, Name: "temperature", Value: f64(21.5)}})
		require.NoErrorf(t, err, "delivery %d must not error", i+1)
	}

	var events []Event
	require.NoError(t, api.RDB.DB(ctx).Find(&events).Error)
	assert.Len(t, events, 1, "the base event converges on its identity")

	var measurements []MeasurementEvent
	require.NoError(t, api.RDB.DB(ctx).Find(&measurements).Error)
	assert.Len(t, measurements, 1,
		"the payload row must converge too — one envelope owning three copies of its own "+
			"measurement is the defect")
}

// TestOneEventKeepsItsDistinctPayloadRows is the counterweight, and it is the reason the
// discriminator is a content digest rather than something cheaper.
//
// Deduplicating payload rows is only safe while genuinely distinct entries of ONE message
// still survive. Two measurements under different names, and two alerts of the SAME type
// carrying different messages, are distinct readings — an index keyed on (event_id, name)
// would hold the first and an index keyed on (event_id, type) would silently eat the
// second alert.
func TestOneEventKeepsItsDistinctPayloadRows(t *testing.T) {
	api := newPayloadIdentityTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := noAltIdEvent("acme", "device-1", occurred, "batch")
	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{
		{Event: ev, EntryOccurredTime: ev.OccurredTime, Name: "temperature", Value: f64(21.5)},
		{Event: ev, EntryOccurredTime: ev.OccurredTime, Name: "humidity", Value: f64(55)},
	})
	require.NoError(t, err)

	var measurements []MeasurementEvent
	require.NoError(t, api.RDB.DB(ctx).Order("name").Find(&measurements).Error)
	require.Len(t, measurements, 2, "distinct names in one message are distinct readings")
	assert.NotEqual(t, measurements[0].PayloadId, measurements[1].PayloadId)

	alertEv := ev
	alertEv.EventType = esmodel.Alert
	alertEv.EventId = DeriveEventId("acme", &alertEv, []byte("alerts"))
	_, err = api.CreateAlertEvents(ctx, api.RDB.DB(ctx), []*AlertEventCreateRequest{
		{Event: alertEv, EntryOccurredTime: alertEv.OccurredTime,
			Type: "SENSOR_FAULT", Level: 3, Message: "probe A open circuit"},
		{Event: alertEv, EntryOccurredTime: alertEv.OccurredTime,
			Type: "SENSOR_FAULT", Level: 3, Message: "probe B open circuit"},
	})
	require.NoError(t, err)

	var alerts []AlertEvent
	require.NoError(t, api.RDB.DB(ctx).Order("message").Find(&alerts).Error)
	require.Len(t, alerts, 2,
		"two alerts of one type carrying different messages are distinct — this is what a "+
			"(event_id, type) key would have silently collapsed")
	assert.NotEqual(t, alerts[0].PayloadId, alerts[1].PayloadId)
}

// TestDerivePayloadIdIsStableAndScopedToItsEvent pins the two properties the payload
// identity relies on: stability across a redelivery (so the dedup engages at all), and
// scoping to the parent event (so identical readings under two different events stay
// distinct rather than one eating the other).
func TestDerivePayloadIdIsStableAndScopedToItsEvent(t *testing.T) {
	eventA := []byte{1, 2, 3}
	eventB := []byte{4, 5, 6}
	content := []byte("temperature=21.5")

	assert.Equal(t, DerivePayloadId(eventA, content), DerivePayloadId(eventA, content),
		"identical input must derive an identical identity (redelivery convergence)")
	assert.NotEqual(t, DerivePayloadId(eventA, content), DerivePayloadId(eventB, content),
		"the same reading under a DIFFERENT event must stay distinct")
	assert.NotEqual(t, DerivePayloadId(eventA, content), DerivePayloadId(eventA, []byte("humidity=55")),
		"different content under one event must stay distinct")
}
