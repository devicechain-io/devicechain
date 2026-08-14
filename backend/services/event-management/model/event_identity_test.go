// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newIdentityTestApi builds an in-memory sqlite Api carrying the SAME keys production
// gets from the migration: the base event's (tenant_id, event_id, occurred_time) primary
// key and the (tenant_id, alt_id, occurred_time) partial unique index. It deliberately
// does NOT restate a unique index on the old natural key — that index is what this change
// removes, and leaving it here would keep the collision alive in the test while the
// production schema had moved on.
func newIdentityTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(&Event{}, &MeasurementEvent{}, &EventAnchor{}), "migrate")
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_events_identity `+
		`ON events (tenant_id, event_id, occurred_time);`).Error, "identity key")
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_measurement_events_idem `+
		`ON measurement_events (tenant_id, payload_id, occurred_time);`).Error, "payload key")
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_event_anchors_idem `+
		`ON event_anchors (tenant_id, event_id, occurred_time, anchor_type, anchor_token);`).Error, "anchor key")
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_events_tenant_alt_id `+
		`ON events (tenant_id, alt_id, occurred_time) WHERE alt_id IS NOT NULL;`).Error, "dedup index")
	return NewApi(&rdb.RdbManager{Database: db})
}

// altEvent builds a base event carrying an alternateId, with the content digest applied
// exactly as PersistEvent applies it in production.
func altEvent(device, altId string, occurred time.Time, payload string) Event {
	ev := Event{
		DeviceToken:  device,
		EventType:    esmodel.Measurement,
		OccurredTime: occurred,
		Source:       "mqtt",
		AltId:        sql.NullString{String: altId, Valid: altId != ""},
	}
	ev.EventId = DeriveEventId("acme", &ev, []byte(payload))
	return ev
}

// TestDistinctEventsAtOneNaturalKeyBothSurvive is the regression test for the defect this
// change exists to fix.
//
// A device that samples two sensors and publishes each as its own message with one shared
// timestamp produces two GENUINELY DISTINCT events sharing (tenant, device, event_type,
// occurred_time). While that tuple was the base event's primary key and parents were
// upserted ON CONFLICT DO NOTHING, the second envelope was silently discarded:
//
//   - its source, processed_time and alt_id were lost;
//   - its payload rows still inserted, and joined to the FIRST event's envelope;
//   - because its alt_id never reached the (tenant_id, alt_id, occurred_time) index, the
//     dedup probe could never see it again — so the event was not merely dropped once, it
//     became permanently un-deduplicable and every later redelivery re-inserted its rows.
//
// Reachability was not exotic: occurred_time is device-supplied, and a device stamping
// whole seconds collides with itself any time it emits twice in one second.
func TestDistinctEventsAtOneNaturalKeyBothSurvive(t *testing.T) {
	api := newIdentityTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)

	first := altEvent("device-1", "msg-A", occurred, `temperature=21.5`)
	second := altEvent("device-1", "msg-B", occurred, `humidity=55`)

	// Negative control: the two events really do share the old natural key, so this test
	// exercises the collision rather than sidestepping it.
	require.Equal(t, first.DeviceToken, second.DeviceToken)
	require.Equal(t, first.EventType, second.EventType)
	require.True(t, first.OccurredTime.Equal(second.OccurredTime))
	require.NotEqual(t, first.EventId, second.EventId,
		"distinct content must derive distinct identities")

	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
		[]*MeasurementEventCreateRequest{{Event: first,
			EntryOccurredTime: first.OccurredTime, Name: "temperature", Value: f64(21.5)}})
	require.NoError(t, err)
	_, err = api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
		[]*MeasurementEventCreateRequest{{Event: second,
			EntryOccurredTime: second.OccurredTime, Name: "humidity", Value: f64(55)}})
	require.NoError(t, err)

	// Both envelopes survive, each with its own alternateId intact — the alt_id being
	// present is what restores idempotency for the second event.
	var events []Event
	require.NoError(t, api.RDB.DB(ctx).Order("alt_id").Find(&events).Error)
	require.Len(t, events, 2, "both distinct events must be persisted")
	assert.Equal(t, "msg-A", events[0].AltId.String)
	assert.Equal(t, "msg-B", events[1].AltId.String)

	// And each payload row is attached to its OWN envelope, not merged onto the first.
	var measurements []MeasurementEvent
	require.NoError(t, api.RDB.DB(ctx).Order("name").Find(&measurements).Error)
	require.Len(t, measurements, 2)
	assert.Equal(t, events[1].EventId, measurements[0].EventId, "humidity belongs to msg-B")
	assert.Equal(t, events[0].EventId, measurements[1].EventId, "temperature belongs to msg-A")
}

// TestRedeliveryOfACollidingEventIsStillDeduplicated closes the second half of the defect.
//
// Restoring both envelopes is only half a fix: the reason the old behaviour was corrosive
// rather than merely lossy is that the dropped event's alt_id never entered the idempotency
// index, so redelivery could not be detected and each retry re-inserted the payload. With
// the envelope persisted, the dedup probe sees it and a redelivery is a no-op.
func TestRedeliveryOfACollidingEventIsStillDeduplicated(t *testing.T) {
	api := newIdentityTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)

	first := altEvent("device-1", "msg-A", occurred, `temperature=21.5`)
	second := altEvent("device-1", "msg-B", occurred, `humidity=55`)
	for _, ev := range []Event{first, second} {
		e := ev
		name := "temperature"
		if e.AltId.String == "msg-B" {
			name = "humidity"
		}
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
			[]*MeasurementEventCreateRequest{{Event: e,
				EntryOccurredTime: e.OccurredTime, Name: name, Value: f64(1)}})
		require.NoError(t, err)
	}

	// The colliding event's alternateId is now visible to the dedup probe. Before the fix
	// this returned false forever, which is what made every redelivery double-persist.
	exists, err := api.EventExistsByAltId(ctx, api.RDB.DB(ctx), "msg-B", occurred)
	require.NoError(t, err)
	assert.True(t, exists, "the second event's alternateId must be visible to the dedup probe")
}

// TestDeriveEventIdIsStableAndContentSensitive pins the two properties the identity relies
// on. Stability across an identical input is what makes a JetStream redelivery converge on
// the same row on EVERY transport, including lwm2m-ingest and sparkplug-ingest, which carry
// no capture-stream sequence to thread through. Sensitivity to each keyed field is what
// stops two distinct readings sharing an identity.
func TestDeriveEventIdIsStableAndContentSensitive(t *testing.T) {
	occurred := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	base := Event{DeviceToken: "d", EventType: esmodel.Measurement, OccurredTime: occurred,
		AltId: sql.NullString{String: "a", Valid: true}}
	want := DeriveEventId("acme", &base, []byte("p"))

	assert.Equal(t, want, DeriveEventId("acme", &base, []byte("p")),
		"identical input must derive an identical identity (redelivery convergence)")

	otherTenant := base
	assert.NotEqual(t, want, DeriveEventId("other", &otherTenant, []byte("p")), "tenant is keyed")

	otherDevice := base
	otherDevice.DeviceToken = "d2"
	assert.NotEqual(t, want, DeriveEventId("acme", &otherDevice, []byte("p")), "device is keyed")

	otherType := base
	otherType.EventType = esmodel.Alert
	assert.NotEqual(t, want, DeriveEventId("acme", &otherType, []byte("p")), "event type is keyed")

	otherTime := base
	otherTime.OccurredTime = occurred.Add(time.Nanosecond)
	assert.NotEqual(t, want, DeriveEventId("acme", &otherTime, []byte("p")),
		"occurred_time is keyed to NANOSECOND resolution — a coarser hash would reintroduce "+
			"exactly the collision this change removes")

	otherAlt := base
	otherAlt.AltId = sql.NullString{String: "b", Valid: true}
	assert.NotEqual(t, want, DeriveEventId("acme", &otherAlt, []byte("p")), "alternateId is keyed")

	assert.NotEqual(t, want, DeriveEventId("acme", &base, []byte("q")), "payload is keyed")
}

// TestDeriveEventIdIsOrderSensitiveOverAPayload pins the coupling that makes the producer's
// canonical ordering LOAD-BEARING, and it is here so that anyone who deletes the sort in
// device-management's ResolveMeasurementsEventPayload can find out why it was there.
//
// The digest takes the payload as opaque bytes, and json.Marshal of a SLICE preserves slice
// order. So two orderings of the SAME readings are two different events as far as this
// function is concerned. That is the correct behaviour for a digest over opaque bytes — it
// cannot know which slices carry meaning in their order (a location track does) and which do
// not. The consequence is a requirement on the PRODUCER: whatever emits a payload whose order
// is arbitrary has to pick a canonical one before publishing, because nothing downstream can
// recover it.
//
// The resolver's measurements were the case where that requirement went unmet: the entries
// came from ranging a map, so one reading produced a different id on each resolution and a
// redelivery double-persisted the event. Fixed at the producer; this test is the reason the
// fix cannot be quietly dropped.
func TestDeriveEventIdIsOrderSensitiveOverAPayload(t *testing.T) {
	occurred := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	base := Event{DeviceToken: "d", EventType: esmodel.Measurement, OccurredTime: occurred}

	ascending := &dmmodel.ResolvedMeasurementsPayload{
		Entries: []dmmodel.ResolvedMeasurementsEntry{{
			Entries: []dmmodel.ResolvedMeasurementEntry{
				{Name: "humidity", Value: "48"},
				{Name: "temperature", Value: "21.5"},
			},
			OccurredTime: occurred,
		}},
	}
	swapped := &dmmodel.ResolvedMeasurementsPayload{
		Entries: []dmmodel.ResolvedMeasurementsEntry{{
			Entries: []dmmodel.ResolvedMeasurementEntry{
				{Name: "temperature", Value: "21.5"},
				{Name: "humidity", Value: "48"},
			},
			OccurredTime: occurred,
		}},
	}

	ascendingBytes, err := json.Marshal(ascending)
	require.NoError(t, err)
	swappedBytes, err := json.Marshal(swapped)
	require.NoError(t, err)

	// The instrument check: if these encoded identically the assertion below would pass
	// while proving nothing about order.
	require.NotEqual(t, string(ascendingBytes), string(swappedBytes),
		"the two fixtures encoded identically, so this test cannot observe order at all")

	assert.NotEqual(t,
		DeriveEventId("acme", &base, ascendingBytes),
		DeriveEventId("acme", &base, swappedBytes),
		"the same readings in two orders derive two identities — which is exactly why the "+
			"producer must emit a canonical order rather than a map's iteration order")
}

// TestDeriveEventIdSeparatesFieldBoundaries guards against the classic concatenation flaw:
// hashing "a"+"bc" and "ab"+"c" to the same digest. Without an unambiguous separator a
// device token ending in the delimiter could forge another device's identity.
func TestDeriveEventIdSeparatesFieldBoundaries(t *testing.T) {
	occurred := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	a := Event{DeviceToken: "ab", EventType: esmodel.Measurement, OccurredTime: occurred}
	b := Event{DeviceToken: "a", EventType: esmodel.Measurement, OccurredTime: occurred}
	assert.NotEqual(t, DeriveEventId("t", &a, []byte("c")), DeriveEventId("t", &b, []byte("bc")),
		"field boundaries must be unambiguous")
}
