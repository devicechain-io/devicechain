// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An anchor-filtered search finds every reading of a BATCHED event, including the ones
// stored at an instant the message itself never carried.
//
// 🔴 THIS IS THE READ PATH THAT PER-SAMPLE TIMES BREAK IF THE JOIN IS LEFT ALONE. An
// anchor row records the MESSAGE's instant — anchors belong to the event, not to a
// reading — while a payload row now records the SAMPLE's. The join used to be
// (device_token, event_type, occurred_time), so the moment those two diverge it matches
// nothing, and "the readings for customer X" comes back empty.
//
// Empty is the dangerous answer, which is why this is worth its own test: a query that
// errors gets investigated, and a query that confidently returns no rows gets believed.
func TestAnAnchorSearchFindsEveryReadingOfABatch(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	// The envelope is when the upload happened; the two readings were taken before it,
	// which is what a store-and-forward device produces.
	envelope := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	buffered := envelope.Add(-5 * time.Minute)

	parent := Event{
		DeviceToken:   "device-4",
		EventType:     esmodel.Measurement,
		OccurredTime:  envelope,
		Source:        "mqtt",
		ProcessedTime: envelope,
	}
	parent.EventId = DeriveEventId("acme", &parent, nil)

	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{
		{Event: parent, EntryOccurredTime: buffered, Name: "temperature", Value: f64(21.5)},
		{Event: parent, EntryOccurredTime: envelope, Name: "temperature", Value: f64(22.5)},
	})
	require.NoError(t, err)

	// One anchor for the whole event, stamped at the envelope — the shape the persistence
	// worker writes.
	require.NoError(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{{
		EventId: parent.EventId, DeviceToken: "device-4", EventType: esmodel.Measurement,
		OccurredTime: envelope, AnchorType: "customer", AnchorToken: "cust-3",
	}}))

	anchorType, anchorToken := "customer", "cust-3"
	results, err := api.MeasurementEvents(ctx, EventSearchCriteria{
		AnchorType: &anchorType, AnchorToken: &anchorToken,
	})
	require.NoError(t, err)
	require.Len(t, results.Results, 2,
		"both readings of the batch must be reachable through the event's anchor")

	got := map[float64]time.Time{}
	for _, m := range results.Results {
		got[m.Value.Float64] = m.OccurredTime
	}
	assert.True(t, got[21.5].Equal(buffered),
		"the buffered reading is stored at its own instant: got %v want %v", got[21.5], buffered)
	assert.True(t, got[22.5].Equal(envelope),
		"the live reading is stored at the message's instant: got %v want %v", got[22.5], envelope)
}

// The anchor filter still EXCLUDES an event that does not carry the anchor.
//
// Keying the join on the event id alone widens what it matches, so this is the
// counterweight: a filter that returned everything would satisfy the test above and
// silently leak one customer's readings into another's view.
func TestAnAnchorSearchStillExcludesUnanchoredEvents(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	anchored := Event{DeviceToken: "device-4", EventType: esmodel.Measurement, OccurredTime: occurred, Source: "mqtt"}
	anchored.EventId = DeriveEventId("acme", &anchored, nil)
	other := Event{DeviceToken: "device-9", EventType: esmodel.Measurement, OccurredTime: occurred, Source: "mqtt"}
	other.EventId = DeriveEventId("acme", &other, nil)

	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{
		{Event: anchored, EntryOccurredTime: occurred, Name: "temperature", Value: f64(21.5)},
		{Event: other, EntryOccurredTime: occurred, Name: "temperature", Value: f64(99.9)},
	})
	require.NoError(t, err)
	require.NoError(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{{
		EventId: anchored.EventId, DeviceToken: "device-4", EventType: esmodel.Measurement,
		OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3",
	}}))

	anchorType, anchorToken := "customer", "cust-3"
	results, err := api.MeasurementEvents(ctx, EventSearchCriteria{
		AnchorType: &anchorType, AnchorToken: &anchorToken,
	})
	require.NoError(t, err)
	require.Len(t, results.Results, 1, "only the anchored event's reading may come back")
	assert.InDelta(t, 21.5, results.Results[0].Value.Float64, 1e-9)
}

// A create request that names no instant of its own is REFUSED, for each payload type.
//
// 🔴 A fail-closed guard nothing exercises is a guard nobody has seen fire, which is how
// this repo previously shipped a command-delivery check that could not fire at all. The
// guard matters because the alternative it refuses — inheriting the parent event's time —
// is silent: the row lands, it reads as a real reading, and a batch quietly collapses back
// onto one instant. Every caller in the tree sets the field, so this is the only place the
// refusal is ever observed.
func TestACreateRequestWithNoEntryTimeIsRefused(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	parent := Event{DeviceToken: "device-4", EventType: esmodel.Measurement, OccurredTime: occurred, Source: "mqtt"}
	parent.EventId = DeriveEventId("acme", &parent, nil)

	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{
		{Event: parent, Name: "temperature", Value: f64(21.5)}, // EntryOccurredTime omitted
	})
	require.Error(t, err, "a measurement request with no entry time must be refused")
	assert.Contains(t, err.Error(), "never inherited from the parent event")

	_, err = api.CreateAlertEvents(ctx, api.RDB.DB(ctx), []*AlertEventCreateRequest{
		{Event: parent, Type: "SENSOR_FAULT", Level: 3},
	})
	require.Error(t, err, "an alert request with no entry time must be refused")

	_, err = api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{
		{Event: parent, Latitude: f64(33.749), Longitude: f64(-84.388)},
	})
	require.Error(t, err, "a location request with no entry time must be refused")

	// Nothing was written. A guard that errors AFTER inserting some of the batch would
	// leave the caller with a partial write and an error that reads like a full rollback.
	var measurements int64
	require.NoError(t, api.RDB.DB(ctx).Model(&MeasurementEvent{}).Count(&measurements).Error)
	assert.Zero(t, measurements, "a refused batch must write nothing")
}
