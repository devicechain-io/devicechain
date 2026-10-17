// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	raw := dmmodel.ResolvedMeasurementEntry{Name: "voltage", Value: "230.5"}
	derived := func(value string) dmmodel.ResolvedMeasurementEntry {
		return dmmodel.ResolvedMeasurementEntry{Name: "power_kw", Value: value, Classifier: &classifier,
			Unit: &unit, DataType: &dataType, Derived: true}
	}
	resolvedWith := func(entries ...dmmodel.ResolvedMeasurementEntry) (*dmmodel.ResolvedEvent, dmmodel.ResolvedMeasurementsEntry) {
		entry := dmmodel.ResolvedMeasurementsEntry{Entries: entries, OccurredTime: sample}
		return &dmmodel.ResolvedEvent{
			SourceDeviceToken: identityDevice,
			OccurredTime:      identityOccurred,
			EventType:         esmodel.Measurement,
			Source:            "mqtt",
			Payload:           &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{entry}},
		}, entry
	}

	first, firstEntry := resolvedWith(derived("2.823625"), raw)
	streamed, err := measurementFromResolved("tenant1", first, firstEntry, firstEntry.Entries[0])
	require.NoError(t, err)

	// The event id is the reading's without its calculated value.
	rawOnly, _ := resolvedWith(raw)
	parent := model.Event{DeviceToken: identityDevice, OccurredTime: identityOccurred, Source: "mqtt",
		EventType: esmodel.Measurement}
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
