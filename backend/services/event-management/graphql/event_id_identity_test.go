// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `id` is declared `ID!` on all four event types, and until this file existed it did not
// behave like one on ANY of them: it was built from (deviceToken, eventType,
// occurredTime), the tuple event_id and payload_id were introduced to replace precisely
// because it is not an identity.
//
// 🔴 THE COLLISION IS THE ORDINARY CASE, NOT A CORNER ONE. A sample's measurements are
// stored one row per name, all carrying the sample's device, type and instant — so a
// device reporting `temp` and `humidity` at one moment returned two nodes with the SAME
// `ID!`. Every fixture below is built that way on purpose: the rows share the whole old
// key and differ only in the surrogate. A test whose rows differed in occurred time
// would pass against the defect.
//
// Uniqueness is asserted ACROSS A RESULT SET rather than per row, because "two nodes in
// one response share an id" is the property a consumer's cache actually breaks on.

var identityOccurred = time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)

const identityDevice = "sensor-1"

// assertIdsAreUniqueAcrossTheSet resolves every id in a result set and fails if any two
// are equal — naming both offenders, since "a duplicate exists" is not enough to debug
// one. It also fails on an id that will not resolve, so a set of empty ids cannot pass
// by being uniformly broken in a different way.
func assertIdsAreUniqueAcrossTheSet(t *testing.T, ids []func() (gql.ID, error)) {
	t.Helper()
	seen := map[gql.ID]int{}
	for i, resolve := range ids {
		id, err := resolve()
		require.NoErrorf(t, err, "result %d did not resolve an id", i)
		require.NotEmptyf(t, string(id), "result %d resolved an empty ID!", i)
		if first, dup := seen[id]; dup {
			t.Fatalf("results %d and %d share the id %q; ID! must address one node", first, i, id)
		}
		seen[id] = i
	}
	require.Len(t, seen, len(ids), "every result must contribute a distinct id")
}

// The defect's own case: one sample, two named measurements, one instant.
func TestMeasurementEventIdsAreUniqueWithinOneSample(t *testing.T) {
	shared := []byte{0xaa, 0xbb}
	rows := []model.MeasurementEvent{
		{EventId: shared, PayloadId: []byte{0x01}, DeviceToken: identityDevice,
			EventType: esmodel.Measurement, OccurredTime: identityOccurred, Name: "temp"},
		{EventId: shared, PayloadId: []byte{0x02}, DeviceToken: identityDevice,
			EventType: esmodel.Measurement, OccurredTime: identityOccurred, Name: "humidity"},
	}

	// The control that makes the assertion mean something: the two rows really are
	// indistinguishable under the key `id` used to be built from, so a failure below
	// is the id and not the fixture.
	require.Equal(t, rows[0].DeviceToken, rows[1].DeviceToken)
	require.Equal(t, rows[0].EventType, rows[1].EventType)
	require.Equal(t, rows[0].OccurredTime, rows[1].OccurredTime)
	require.Equal(t, rows[0].EventId, rows[1].EventId,
		"two measurements of one sample share a parent event, so event_id cannot address them either")

	resolvers := (&MeasurementEventSearchResultsResolver{
		M: model.MeasurementEventSearchResults{Results: rows},
	}).Results()
	require.Len(t, resolvers, 2)

	assertIdsAreUniqueAcrossTheSet(t, []func() (gql.ID, error){
		resolvers[0].Id, resolvers[1].Id,
	})
}

// Location rows batch the same way — a store-and-forward tracker flushes a track as one
// message, and every fix in it shares the envelope's device and type.
func TestLocationEventIdsAreUniqueWithinOneBatch(t *testing.T) {
	shared := []byte{0xaa, 0xbb}
	rows := []model.LocationEvent{
		{EventId: shared, PayloadId: []byte{0x01}, DeviceToken: identityDevice,
			EventType: esmodel.Location, OccurredTime: identityOccurred,
			Latitude: sql.NullFloat64{Float64: 33.749, Valid: true}},
		{EventId: shared, PayloadId: []byte{0x02}, DeviceToken: identityDevice,
			EventType: esmodel.Location, OccurredTime: identityOccurred,
			Latitude: sql.NullFloat64{Float64: 33.750, Valid: true}},
	}
	require.Equal(t, rows[0].OccurredTime, rows[1].OccurredTime,
		"the fixture must collide under the old key or it proves nothing")

	resolvers := (&LocationEventSearchResultsResolver{
		M: model.LocationEventSearchResults{Results: rows},
	}).Results()
	require.Len(t, resolvers, 2)

	assertIdsAreUniqueAcrossTheSet(t, []func() (gql.ID, error){
		resolvers[0].Id, resolvers[1].Id,
	})
}

// Alerts too: two alerts of one type at one instant differ only by their message, which
// is in the payload identity and was in nothing the old id read.
func TestAlertEventIdsAreUniqueWithinOneBatch(t *testing.T) {
	shared := []byte{0xaa, 0xbb}
	rows := []model.AlertEvent{
		{EventId: shared, PayloadId: []byte{0x01}, DeviceToken: identityDevice,
			EventType: esmodel.Alert, OccurredTime: identityOccurred,
			Type: "SENSOR_FAULT", Message: "probe A unresponsive"},
		{EventId: shared, PayloadId: []byte{0x02}, DeviceToken: identityDevice,
			EventType: esmodel.Alert, OccurredTime: identityOccurred,
			Type: "SENSOR_FAULT", Message: "probe B unresponsive"},
	}
	require.Equal(t, rows[0].Type, rows[1].Type,
		"the fixture must collide under the old key or it proves nothing")

	resolvers := (&AlertEventSearchResultsResolver{
		M: model.AlertEventSearchResults{Results: rows},
	}).Results()
	require.Len(t, resolvers, 2)

	assertIdsAreUniqueAcrossTheSet(t, []func() (gql.ID, error){
		resolvers[0].Id, resolvers[1].Id,
	})
}

// The base event is the one case where the old tuple was nearly an identity — and
// "nearly" is what event_id exists to end: a device that samples two sensors and
// publishes each as its own message under one timestamp produces two DISTINCT events
// sharing that whole tuple.
func TestBaseEventIdsAreUniqueWhenTheNaturalKeyCollides(t *testing.T) {
	rows := []model.Event{
		{EventId: []byte{0x01}, DeviceToken: identityDevice,
			EventType: esmodel.Measurement, OccurredTime: identityOccurred, Source: "mqtt"},
		{EventId: []byte{0x02}, DeviceToken: identityDevice,
			EventType: esmodel.Measurement, OccurredTime: identityOccurred, Source: "mqtt"},
	}
	require.Equal(t, rows[0].OccurredTime, rows[1].OccurredTime,
		"the fixture must collide under the old key or it proves nothing")

	resolvers := (&EventSearchResultsResolver{
		M: model.EventSearchResults{Results: rows},
	}).Results()
	require.Len(t, resolvers, 2)

	assertIdsAreUniqueAcrossTheSet(t, []func() (gql.ID, error){
		resolvers[0].Id, resolvers[1].Id,
	})
}

// A base event is addressed by event_id and a payload row by payload_id — never the
// other way round. This is what stops the fix being "use some surrogate": every payload
// row of one message shares its event_id, so resolving a payload id from it would
// reproduce the defect with a different column.
func TestPayloadRowsAreNotAddressedByTheirEventId(t *testing.T) {
	shared := []byte{0xaa, 0xbb}
	row := model.MeasurementEvent{EventId: shared, PayloadId: []byte{0x01},
		DeviceToken: identityDevice, EventType: esmodel.Measurement, OccurredTime: identityOccurred}

	id, err := (&MeasurementEventResolver{M: row}).Id()
	require.NoError(t, err)

	assert.Equal(t, gql.ID("01"), id, "a payload row's id is its payload_id")
	assert.NotEqual(t, gql.ID("aabb"), id, "a payload row must not be addressed by its parent's id")
}

// The live subscription is the path where an empty id would have shipped: a streamed
// reading is never read back from storage, so nothing fills its identity for it. These
// two properties are what make the streamed `ID!` real rather than well-formed.
func TestStreamedMeasurementsCarryDistinctPersistedIds(t *testing.T) {
	sample := time.Date(2026, 9, 4, 9, 59, 0, 0, time.UTC)
	resolved := &dmmodel.ResolvedEvent{
		SourceDeviceToken: identityDevice,
		OccurredTime:      identityOccurred,
		EventType:         esmodel.Measurement,
		Source:            "mqtt",
	}
	entry := dmmodel.ResolvedMeasurementsEntry{OccurredTime: sample}

	temp, err := measurementFromResolved("tenant1", resolved, entry,
		dmmodel.ResolvedMeasurementEntry{Name: "temp", Value: "21.5"})
	require.NoError(t, err)
	humidity, err := measurementFromResolved("tenant1", resolved, entry,
		dmmodel.ResolvedMeasurementEntry{Name: "humidity", Value: "48"})
	require.NoError(t, err)

	assertIdsAreUniqueAcrossTheSet(t, []func() (gql.ID, error){
		(&MeasurementEventResolver{M: temp}).Id,
		(&MeasurementEventResolver{M: humidity}).Id,
	})

	// And the id is the one the persisted row will carry, not merely a unique one:
	// a live chart and the history behind it name the same reading the same way.
	// Derived here through the model's own functions from the SAME inputs the
	// persister would use, so a stream that drifted from the persist path fails here.
	parent := model.Event{
		DeviceToken:  identityDevice,
		OccurredTime: identityOccurred,
		Source:       "mqtt",
		EventType:    esmodel.Measurement,
	}
	eventId, err := model.DeriveEventIdForPayload("tenant1", &parent, resolved.Payload)
	require.NoError(t, err)
	parent.EventId = eventId
	value := 21.5
	want, err := model.DeriveMeasurementPayloadId(&model.MeasurementEventCreateRequest{
		Event:             parent,
		EntryOccurredTime: sample,
		Name:              "temp",
		Value:             &value,
	})
	require.NoError(t, err)
	assert.Equal(t, want, temp.PayloadId,
		"a streamed reading must carry the identity its persisted row will have")
}
