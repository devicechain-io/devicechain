// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Calculated metrics and event identity (ADR-082 decision 5). A calculated value is outside
// the event id, and its row's payload id is value-free, so one reading keeps one id and
// stores one row per value across a formula published between two copies of it. Every pin
// below hashes a LITERAL document, for the reason payload_preimage_test.go gives.

var calcOccurred = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func u64Ptr(v uint64) *uint64 { return &v }

// meterPayload is one meter reading as the resolver emits it: the raw readings sorted by
// name, with the derived entries the profile computed, if any, in their sorted places.
func meterPayload(derived ...dmmodel.ResolvedMeasurementEntry) *dmmodel.ResolvedMeasurementsPayload {
	entries := []dmmodel.ResolvedMeasurementEntry{
		{Name: "current", Value: "12.25", Classifier: u64Ptr(2), Unit: strPtr("A"), DataType: strPtr("DOUBLE")},
	}
	entries = append(entries, derived...)
	entries = append(entries,
		dmmodel.ResolvedMeasurementEntry{Name: "voltage", Value: "230.5", Classifier: u64Ptr(1), Unit: strPtr("V"), DataType: strPtr("DOUBLE")})
	return &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
		{Entries: entries, OccurredTime: calcOccurred},
	}}
}

func powerEntry(value string) dmmodel.ResolvedMeasurementEntry {
	return dmmodel.ResolvedMeasurementEntry{Name: "power_kw", Value: value, Classifier: u64Ptr(5),
		Unit: strPtr("kW"), DataType: strPtr("DOUBLE"), Derived: true}
}

// meterRawJSON is the raw reading's document exactly as event identity hashed it before
// calculated metrics existed: the entry struct has no JSON tags, so field names and order
// are the struct's, and a raw entry carries no Derived key.
const meterRawJSON = `{"Entries":[{"Entries":[` +
	`{"Name":"current","Value":"12.25","Classifier":2,"Unit":"A","DataType":"DOUBLE"},` +
	`{"Name":"voltage","Value":"230.5","Classifier":1,"Unit":"V","DataType":"DOUBLE"}],` +
	`"OccurredTime":"2026-10-16T12:00:00Z"}]}`

func meterEvent() *Event {
	return &Event{DeviceToken: "meter-1", EventType: esmodel.Measurement, OccurredTime: calcOccurred, Source: "lwm2m"}
}

// (a) A payload with no calculated value hashes to the id it had before the change: the
// literal is the document the old code hashed.
func TestARawOnlyPayloadKeepsItsEventId(t *testing.T) {
	got, err := DeriveEventIdForPayload("acme", meterEvent(), meterPayload())
	require.NoError(t, err)
	assert.Equal(t, DeriveEventId("acme", meterEvent(), []byte(meterRawJSON)), got,
		"a formula-free reading's event id moved; every stored event's id moved with it")

	// By value as well as by pointer: both shapes reach this function.
	byValue, err := DeriveEventIdForPayload("acme", meterEvent(), *meterPayload())
	require.NoError(t, err)
	assert.Equal(t, got, byValue)
}

// (b) The same reading with and without its calculated values — and with two different
// calculated values, as two formulas would give — is one event.
func TestCalculatedValuesAreOutsideTheEventId(t *testing.T) {
	raw, err := DeriveEventIdForPayload("acme", meterEvent(), meterPayload())
	require.NoError(t, err)
	v1, err := DeriveEventIdForPayload("acme", meterEvent(), meterPayload(powerEntry("2.823625")))
	require.NoError(t, err)
	v2, err := DeriveEventIdForPayload("acme", meterEvent(), meterPayload(powerEntry("2823.625")))
	require.NoError(t, err)
	assert.Equal(t, raw, v1, "adding a calculated value must not move the event id")
	assert.Equal(t, raw, v2, "a different formula's value must not move it either")
	byValue, err := DeriveEventIdForPayload("acme", meterEvent(), *meterPayload(powerEntry("2.823625")))
	require.NoError(t, err)
	assert.Equal(t, raw, byValue, "a payload passed by value is filtered as one passed by pointer")

	// The negative control: the same payload hashed WITHOUT the identity view — what the
	// id would be had the filter been missed — differs between the two formulas. Without
	// this the equalities above could hold because the derived entry never reached the
	// encoder at all.
	unfiltered := func(p *dmmodel.ResolvedMeasurementsPayload) []byte {
		return DeriveEventId("acme", meterEvent(), mustJSON(t, p))
	}
	assert.NotEqual(t, unfiltered(meterPayload(powerEntry("2.823625"))), unfiltered(meterPayload(powerEntry("2823.625"))),
		"hashed whole, the two formulas' payloads are two events: the filter is what makes them one")
	assert.Contains(t, string(mustJSON(t, meterPayload(powerEntry("1")))), `"Derived":true`,
		"a derived entry carries its flag on the encoded payload")
}

// The identity view never changes the payload it was taken from, and returns a formula-free
// payload itself rather than a copy.
func TestIdentityViewLeavesThePayloadAlone(t *testing.T) {
	raw := meterPayload()
	assert.Same(t, raw, raw.IdentityView(), "a payload with no derived entry is its own identity view")

	full := meterPayload(powerEntry("2.823625"))
	view := full.IdentityView()
	require.NotSame(t, full, view)
	assert.Len(t, full.Entries[0].Entries, 3, "the view must not filter the payload in place")
	assert.Len(t, view.Entries[0].Entries, 2)
	assert.Equal(t, meterRawJSON, string(mustJSON(t, view)))

	var nilPayload *dmmodel.ResolvedMeasurementsPayload
	assert.Nil(t, nilPayload.IdentityView())
}

// (c) A calculated row's payload id is value-free: the literal "derived", its name and the
// sample's instant, under the parent event. Two rows differing in value, classifier, unit
// or data type share it; a different name or instant does not; and it is not the raw
// preimage of the same reading.
func TestDerivedMeasurementPayloadPreimageIsPinned(t *testing.T) {
	request := &MeasurementEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: preimageOccurred,
		Name:              "power_kw",
		Value:             float64Ptr(2.823625),
		Classifier:        uintPtr(5),
		Unit:              strPtr("kW"),
		DataType:          strPtr("DOUBLE"),
		Derived:           true,
	}
	got, err := DeriveMeasurementPayloadId(request)
	require.NoError(t, err)
	want := DerivePayloadId(preimageEventId, []byte(
		`{"Kind":"derived","Name":"power_kw","Occurred":"2026-07-01T11:58:30Z"}`))
	assert.Equal(t, want, got, "the derived preimage moved; every stored calculated row's payload_id moves with it")

	other := *request
	other.Value, other.Classifier, other.Unit, other.DataType = float64Ptr(2823.625), uintPtr(9), strPtr("W"), strPtr("INT")
	second, err := DeriveMeasurementPayloadId(&other)
	require.NoError(t, err)
	assert.Equal(t, got, second, "a second formula's value for the same reading must collide with the first")

	renamed := *request
	renamed.Name = "temp_f"
	third, err := DeriveMeasurementPayloadId(&renamed)
	require.NoError(t, err)
	assert.NotEqual(t, got, third, "two calculated metrics of one sample are two rows")

	later := *request
	later.EntryOccurredTime = preimageOccurred.Add(time.Microsecond)
	fourth, err := DeriveMeasurementPayloadId(&later)
	require.NoError(t, err)
	assert.NotEqual(t, got, fourth, "two samples of one event are two rows")

	asRaw := *request
	asRaw.Derived = false
	raw, err := DeriveMeasurementPayloadId(&asRaw)
	require.NoError(t, err)
	assert.NotEqual(t, got, raw, "a calculated row and a raw row must never share an identity")
	assert.Equal(t, DerivePayloadId(preimageEventId, []byte(
		`{"Name":"power_kw","Value":2.823625,"Classifier":5,"Unit":"kW",`+
			`"DataType":"DOUBLE","Occurred":"2026-07-01T11:58:30Z"}`)), raw,
		"the raw preimage is untouched by the derived branch")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
