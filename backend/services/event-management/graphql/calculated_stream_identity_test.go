// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// A calculated value on the live stream carries the identity its persisted row gets: the
// event id of the reading WITHOUT its calculated values, and the value-free derived payload
// id under it. The persist path copies the Derived flag into the create request and so does
// the stream; without it here, a streamed calculated value would carry the raw-preimage id —
// well-formed, unique, and naming no stored row — and a chart that merges the stream with
// history would show the reading twice.
func TestAStreamedCalculatedValueCarriesItsPersistedIds(t *testing.T) {
	sample := time.Date(2026, 10, 16, 11, 59, 0, 0, time.UTC)
	classifier, unit, dataType := uint64(5), "kW", "DOUBLE"
	raw := resolvedevent.ResolvedMeasurementEntry{Name: "voltage", Value: "230.5"}
	derived := func(value string) resolvedevent.ResolvedMeasurementEntry {
		return resolvedevent.ResolvedMeasurementEntry{Name: "power_kw", Value: value, Classifier: &classifier,
			Unit: &unit, DataType: &dataType, Derived: true}
	}
	resolvedWith := func(entries ...resolvedevent.ResolvedMeasurementEntry) (*resolvedevent.ResolvedEvent, resolvedevent.ResolvedMeasurementsEntry) {
		entry := resolvedevent.ResolvedMeasurementsEntry{Entries: entries, OccurredTime: sample}
		return &resolvedevent.ResolvedEvent{
			SourceDeviceToken: identityDevice,
			OccurredTime:      identityOccurred,
			EventType:         eventtype.Measurement,
			Source:            "mqtt",
			Payload:           &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{entry}},
		}, entry
	}

	first, firstEntry := resolvedWith(derived("2.823625"), raw)
	streamed, err := measurementFromResolved("tenant1", first, firstEntry, firstEntry.Entries[0])
	require.NoError(t, err)

	// The event id is the reading's without its calculated value.
	rawOnly, _ := resolvedWith(raw)
	parent := model.Event{DeviceToken: identityDevice, OccurredTime: identityOccurred, Source: "mqtt",
		EventType: eventtype.Measurement}
	eventId, err := model.DeriveEventIdForPayload("tenant1", &parent, rawOnly.Payload)
	require.NoError(t, err)
	assert.Equal(t, eventId, streamed.EventId, "a calculated value must not move the streamed reading's event id")

	// The payload id is the derived preimage, as a literal: no value, classifier, unit or type.
	want := model.DerivePayloadId(eventId, []byte(`{"Kind":"derived","Name":"power_kw","Occurred":"2026-10-16T11:59:00Z"}`))
	assert.Equal(t, want, streamed.PayloadId,
		"a streamed calculated value must carry the payload id its persisted row has")

	// And another formula's value for the same reading names the same row.
	second, secondEntry := resolvedWith(derived("2823.625"), raw)
	again, err := measurementFromResolved("tenant1", second, secondEntry, secondEntry.Entries[0])
	require.NoError(t, err)
	assert.Equal(t, streamed.PayloadId, again.PayloadId)
}
