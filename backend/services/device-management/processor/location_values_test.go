// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exist because location resolution had NO value-level coverage: every
// other test naming Location used it as an inert enum label or an envelope whose
// Entries slice was empty, so deleting the coordinate assignments from
// ResolveLocationsEventPayload left the whole suite green. They therefore carry real,
// DISTINCT values through the real resolver and read each field back individually —
// distinctness is load-bearing, since identical values would let a mapping that
// assigned Accuracy to Speed pass unnoticed.

// A resolver adequate for the location branch. ResolveLocationsEventPayload reads
// only event.Payload — no API, no device, no relation — so the collaborators are
// deliberately nil: a resolver that started needing one would fail here loudly
// rather than being quietly satisfied by a mock.
func locationResolver() *EventResolver {
	return NewEventResolver(1, nil, "", nil, nil, nil, nil, nil)
}

// strptr is a local helper so each literal below reads as its own distinct value.
func strptr(s string) *string {
	return &s
}

// Resolve the given entries and return the resolved payload.
func resolveLocations(t *testing.T, entries ...esmodel.UnresolvedLocationEntry) *model.ResolvedLocationsPayload {
	t.Helper()
	event := &esmodel.UnresolvedEvent{
		Source:    "test-source",
		Device:    "TEST-123",
		EventType: esmodel.Location,
		Payload:   &esmodel.UnresolvedLocationsPayload{Entries: entries},
	}
	resolved, err := locationResolver().ResolveLocationsEventPayload(context.Background(), nil, nil, event)
	require.NoError(t, err)
	payload, ok := resolved.(*model.ResolvedLocationsPayload)
	require.True(t, ok, "expected *model.ResolvedLocationsPayload, got %T", resolved)
	return payload
}

// Every field of a fix must survive resolution unaltered. The values are distinct and
// non-round on purpose: latitude/longitude/elevation/accuracy/speed/heading are all
// decimal strings of the same shape, so a mapping that crossed two of them would be
// invisible under uniform literals.
func TestResolveLocationsCarriesEveryField(t *testing.T) {
	occurred := "2026-08-09T14:32:07.125Z"
	entry := esmodel.UnresolvedLocationEntry{
		Latitude:     strptr("33.749"),
		Longitude:    strptr("-84.388"),
		Elevation:    strptr("320.5"),
		Accuracy:     strptr("4.2"),
		Speed:        strptr("1.75"),
		Heading:      strptr("271.5"),
		OccurredTime: &occurred,
	}

	payload := resolveLocations(t, entry)

	require.Len(t, payload.Entries, 1)
	got := payload.Entries[0]
	require.NotNil(t, got.Latitude, "latitude was dropped in resolution")
	assert.Equal(t, "33.749", *got.Latitude)
	require.NotNil(t, got.Longitude, "longitude was dropped in resolution")
	assert.Equal(t, "-84.388", *got.Longitude)
	require.NotNil(t, got.Elevation, "elevation was dropped in resolution")
	assert.Equal(t, "320.5", *got.Elevation)
	require.NotNil(t, got.Accuracy, "accuracy was dropped in resolution")
	assert.Equal(t, "4.2", *got.Accuracy)
	require.NotNil(t, got.Speed, "speed was dropped in resolution")
	assert.Equal(t, "1.75", *got.Speed)
	require.NotNil(t, got.Heading, "heading was dropped in resolution")
	assert.Equal(t, "271.5", *got.Heading)
	require.NotNil(t, got.OccurredTime, "occurred time was dropped in resolution")
	assert.Equal(t, occurred, *got.OccurredTime)
}

// A device that reports only a position must resolve with the rest genuinely ABSENT.
// nil and "" are different on the wire (the proto fields carry explicit presence) and
// different in the database, so "not reported" must not become "reported as empty".
func TestResolveLocationsLeavesUnsetFieldsNil(t *testing.T) {
	entry := esmodel.UnresolvedLocationEntry{
		Latitude:  strptr("41.8781"),
		Longitude: strptr("-87.6298"),
	}

	payload := resolveLocations(t, entry)

	require.Len(t, payload.Entries, 1)
	got := payload.Entries[0]
	require.NotNil(t, got.Latitude)
	assert.Equal(t, "41.8781", *got.Latitude)
	require.NotNil(t, got.Longitude)
	assert.Equal(t, "-87.6298", *got.Longitude)
	assert.Nil(t, got.Elevation, "unset elevation must stay nil, not become an empty string")
	assert.Nil(t, got.Accuracy, "unset accuracy must stay nil, not become an empty string")
	assert.Nil(t, got.Speed, "unset speed must stay nil, not become an empty string")
	assert.Nil(t, got.Heading, "unset heading must stay nil, not become an empty string")
	assert.Nil(t, got.OccurredTime, "unset occurred time must stay nil, not become an empty string")
}

// A batched fix set must resolve entry-for-entry, in order, each keeping its OWN
// values. This is the guard against a loop that reuses one entry variable or appends
// the same entry twice — a shape that keeps the COUNT right while collapsing a track
// into a single repeated point.
func TestResolveLocationsKeepsPerEntryValuesAndOrder(t *testing.T) {
	firstTime := "2026-08-09T14:32:07.125Z"
	secondTime := "2026-08-09T14:32:09.500Z"
	first := esmodel.UnresolvedLocationEntry{
		Latitude:     strptr("33.749"),
		Longitude:    strptr("-84.388"),
		Elevation:    strptr("320.5"),
		Accuracy:     strptr("4.2"),
		Speed:        strptr("1.75"),
		Heading:      strptr("271.5"),
		OccurredTime: &firstTime,
	}
	second := esmodel.UnresolvedLocationEntry{
		Latitude:     strptr("47.6062"),
		Longitude:    strptr("-122.3321"),
		Elevation:    strptr("54.25"),
		Accuracy:     strptr("9.8"),
		Speed:        strptr("12.5"),
		Heading:      strptr("18.75"),
		OccurredTime: &secondTime,
	}

	payload := resolveLocations(t, first, second)

	require.Len(t, payload.Entries, 2)

	got := payload.Entries[0]
	require.NotNil(t, got.Latitude)
	assert.Equal(t, "33.749", *got.Latitude, "first entry lost its own latitude")
	require.NotNil(t, got.Longitude)
	assert.Equal(t, "-84.388", *got.Longitude)
	require.NotNil(t, got.Elevation)
	assert.Equal(t, "320.5", *got.Elevation)
	require.NotNil(t, got.Accuracy)
	assert.Equal(t, "4.2", *got.Accuracy)
	require.NotNil(t, got.Speed)
	assert.Equal(t, "1.75", *got.Speed)
	require.NotNil(t, got.Heading)
	assert.Equal(t, "271.5", *got.Heading)
	require.NotNil(t, got.OccurredTime)
	assert.Equal(t, firstTime, *got.OccurredTime)

	got = payload.Entries[1]
	require.NotNil(t, got.Latitude)
	assert.Equal(t, "47.6062", *got.Latitude, "second entry did not keep its own latitude — entries may be aliased")
	require.NotNil(t, got.Longitude)
	assert.Equal(t, "-122.3321", *got.Longitude)
	require.NotNil(t, got.Elevation)
	assert.Equal(t, "54.25", *got.Elevation)
	require.NotNil(t, got.Accuracy)
	assert.Equal(t, "9.8", *got.Accuracy)
	require.NotNil(t, got.Speed)
	assert.Equal(t, "12.5", *got.Speed)
	require.NotNil(t, got.Heading)
	assert.Equal(t, "18.75", *got.Heading)
	require.NotNil(t, got.OccurredTime)
	assert.Equal(t, secondTime, *got.OccurredTime)
}

// A payload of the wrong type must be refused rather than resolving to an empty fix
// set — an empty-but-successful resolution is exactly how a dropped location looks.
func TestResolveLocationsRejectsWrongPayloadType(t *testing.T) {
	event := &esmodel.UnresolvedEvent{
		Source:    "test-source",
		Device:    "TEST-123",
		EventType: esmodel.Location,
		Payload:   &esmodel.UnresolvedMeasurementsPayload{},
	}
	resolved, err := locationResolver().ResolveLocationsEventPayload(context.Background(), nil, nil, event)
	assert.Error(t, err)
	assert.Nil(t, resolved)
}
