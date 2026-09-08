// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The payload preimages are content-addressed identities, so their FIELD NAMES and
// declaration ORDER are stored data rather than local style: renaming or reordering one
// changes every stored row's payload_id, which makes a redelivery miss the idempotency
// index and insert a duplicate.
//
// 🔴 EVERY PIN BELOW HASHES A LITERAL JSON DOCUMENT, NOT ONE RE-DERIVED FROM THE STRUCT
// UNDER TEST. A fixture built by calling the same code cannot notice that code moving —
// it moves with it and stays green. The literal is an independent statement of what the
// preimage IS, so renaming `Occurred` to `OccurredTime`, or moving `Speed` above
// `Accuracy`, fails here rather than in production six weeks later.
//
// These pins became load-bearing when the preimages moved out of model/api.go into
// payload_identity.go so the live subscription could derive the same identity: the move
// itself is exactly the kind of change that could have altered a digest silently.

var preimageOccurred = time.Date(2026, 7, 1, 11, 58, 30, 0, time.UTC)

// preimageEventId is an arbitrary but fixed parent identity. It is not derived, because
// what is under test is the ENTRY half of the preimage.
var preimageEventId = []byte{0x11, 0x22, 0x33, 0x44}

func float64Ptr(v float64) *float64 { return &v }
func uintPtr(v uint) *uint          { return &v }
func strPtr(v string) *string       { return &v }

// A location row's preimage is every stored field of the fix, in the declared order,
// plus the SAMPLE's instant under the name `Occurred`.
func TestLocationPayloadPreimageIsPinned(t *testing.T) {
	request := &LocationEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: preimageOccurred,
		Latitude:          float64Ptr(33.749),
		Longitude:         float64Ptr(-84.388),
		Elevation:         float64Ptr(320.5),
		Accuracy:          float64Ptr(4.2),
		Speed:             float64Ptr(1.75),
		Heading:           float64Ptr(271.5),
	}

	got, err := DeriveLocationPayloadId(request)
	require.NoError(t, err)

	want := DerivePayloadId(preimageEventId, []byte(
		`{"Lat":33.749,"Lon":-84.388,"Elev":320.5,"Accuracy":4.2,"Speed":1.75,`+
			`"Heading":271.5,"Occurred":"2026-07-01T11:58:30Z"}`))
	assert.Equal(t, want, got,
		"the location preimage moved; every stored location row's payload_id moves with it")
}

// A measurement row's preimage. This is the one with two producers — the persist path
// and the live subscription — so a drift here does not merely change ids, it makes the
// two disagree about one reading.
func TestMeasurementPayloadPreimageIsPinned(t *testing.T) {
	request := &MeasurementEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: preimageOccurred,
		Name:              "temperature",
		Value:             float64Ptr(21.5),
		Classifier:        uintPtr(3),
		Unit:              strPtr("Cel"),
		DataType:          strPtr("DOUBLE"),
	}

	got, err := DeriveMeasurementPayloadId(request)
	require.NoError(t, err)

	want := DerivePayloadId(preimageEventId, []byte(
		`{"Name":"temperature","Value":21.5,"Classifier":3,"Unit":"Cel",`+
			`"DataType":"DOUBLE","Occurred":"2026-07-01T11:58:30Z"}`))
	assert.Equal(t, want, got,
		"the measurement preimage moved; the persist path and the live stream would "+
			"now name one reading with two different ids")
}

// An unset optional is a JSON null in the preimage rather than an omitted key, which is
// what keeps "reported as absent" distinguishable from "reported as zero".
func TestMeasurementPayloadPreimageKeepsAbsentFieldsAsNull(t *testing.T) {
	request := &MeasurementEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: preimageOccurred,
		Name:              "state",
	}

	got, err := DeriveMeasurementPayloadId(request)
	require.NoError(t, err)

	want := DerivePayloadId(preimageEventId, []byte(
		`{"Name":"state","Value":null,"Classifier":null,"Unit":null,`+
			`"DataType":null,"Occurred":"2026-07-01T11:58:30Z"}`))
	assert.Equal(t, want, got, "an absent optional must stay a null in the preimage")
}

// An alert row's preimage carries Message and Source, so a second alert of the same type
// with a different message is a different row rather than a swallowed duplicate.
func TestAlertPayloadPreimageIsPinned(t *testing.T) {
	request := &AlertEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: preimageOccurred,
		Type:              "SENSOR_FAULT",
		Level:             3,
		Message:           "probe unresponsive",
		Source:            "firmware",
	}

	got, err := DeriveAlertPayloadId(request)
	require.NoError(t, err)

	want := DerivePayloadId(preimageEventId, []byte(
		`{"Type":"SENSOR_FAULT","Level":3,"Message":"probe unresponsive",`+
			`"Source":"firmware","Occurred":"2026-07-01T11:58:30Z"}`))
	assert.Equal(t, want, got,
		"the alert preimage moved; every stored alert row's payload_id moves with it")
}

// The negative control for all four pins above: a preimage that really is different has
// to produce a different id, or the assertions are comparing two constants that would
// agree no matter what the code did.
func TestPayloadPreimagesDiscriminate(t *testing.T) {
	base := &MeasurementEventCreateRequest{
		Event:             Event{EventId: preimageEventId},
		EntryOccurredTime: preimageOccurred,
		Name:              "temperature",
		Value:             float64Ptr(21.5),
	}
	first, err := DeriveMeasurementPayloadId(base)
	require.NoError(t, err)

	changed := *base
	changed.Value = float64Ptr(21.6)
	second, err := DeriveMeasurementPayloadId(&changed)
	require.NoError(t, err)

	assert.NotEqual(t, first, second,
		"two different readings must not share one payload identity")
}

// DeriveEventIdForPayload exists so the persist path and the live subscription
// canonicalize a payload the same way. It is pinned against DeriveEventId fed a literal
// document, so the marshalling step it added is asserted rather than assumed.
func TestDeriveEventIdForPayloadMarshalsThePayloadItself(t *testing.T) {
	event := &Event{DeviceToken: "device-4", OccurredTime: preimageOccurred}
	payload := struct {
		Name  string
		Value float64
	}{"temperature", 21.5}

	got, err := DeriveEventIdForPayload("tenant1", event, payload)
	require.NoError(t, err)

	want := DeriveEventId("tenant1", event, []byte(`{"Name":"temperature","Value":21.5}`))
	assert.Equal(t, want, got,
		"the event identity must hash the payload's own JSON, unchanged")
}
