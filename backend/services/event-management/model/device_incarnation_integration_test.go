// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Every read by device token, on the real hypertables and the continuous aggregate: a
// token deleted and created again reads as its current incarnation only.
func TestIntegrationTokenReadsSeeOnlyTheCurrentIncarnation(t *testing.T) {
	api := newPostgresApi(t, freshInstance(t, "itreincarnation"))
	const tenant = "acme"
	ctx := core.WithTenant(context.Background(), tenant)

	// The other payload tables, one old-incarnation and one current row each, plus a
	// customer anchor on every pump-1 event so a scoped read can be checked too.
	for i, at := range []time.Duration{-time.Hour, time.Hour} {
		ev := func(typ eventtype.EventType, tag string) Event {
			e := Event{DeviceToken: "pump-1", EventType: typ, Source: "mqtt",
				OccurredTime: reincarnationDeletedAt.Add(at), ProcessedTime: reincarnationDeletedAt.Add(at)}
			e.EventId = DeriveEventId(tenant, &e, []byte(tag))
			return e
		}
		lat := float64(i + 1)
		loc, alert, change := ev(eventtype.Location, "l"), ev(eventtype.Alert, "a"), ev(eventtype.StateChange, "s")
		require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := api.CreateLocationEvents(ctx, tx, []*LocationEventCreateRequest{{
				Event: loc, EntryOccurredTime: loc.OccurredTime, Latitude: &lat, Longitude: &lat}}); err != nil {
				return err
			}
			if _, err := api.CreateAlertEvents(ctx, tx, []*AlertEventCreateRequest{{
				Event: alert, EntryOccurredTime: alert.OccurredTime, Type: "overheat", Level: uint32(i + 1)}}); err != nil {
				return err
			}
			_, _, err := api.CreateStateChangeEvents(ctx, tx, []*StateChangeEventCreateRequest{{
				Event: change, State: "connected", SessionId: uint64(i + 1)}})
			return err
		}))
	}
	seedReincarnation(t, api, tenant)

	// Customer anchors on every pump-1 base event, written AFTER the deletion was handled and
	// straight into the table, past the late-anchor filter: as if the deletion's anchor cleanup
	// had failed. A scoped read must then still be bounded by the read itself.
	var pumpEvents []Event
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", "pump-1").Find(&pumpEvents).Error)
	var anchors []*EventAnchor
	for _, e := range pumpEvents {
		anchors = append(anchors, &EventAnchor{EventId: e.EventId, DeviceToken: e.DeviceToken, EventType: e.EventType,
			OccurredTime: e.OccurredTime, AnchorType: "customer", AnchorToken: "cust-1"})
	}
	require.NoError(t, api.RDB.DB(ctx).Create(&anchors).Error)
	var anchored int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&anchored).Error)
	require.EqualValues(t, len(pumpEvents), anchored, "precondition: the old incarnation's anchors are still stored")

	assertTokenReadsSeeOnlyTheCurrentIncarnation(t, api, tenant)

	token := "pump-1"
	criteria := EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		DeviceToken: &token, StartTime: &reincarnationRange.start, EndTime: &reincarnationRange.end}
	locations, err := api.LocationEvents(ctx, criteria)
	require.NoError(t, err)
	if assert.Len(t, locations.Results, 1, "locations of a reused token") {
		assert.InDelta(t, 2, locations.Results[0].Latitude.Float64, 0, "only the current incarnation's fix")
	}
	alerts, err := api.AlertEvents(ctx, criteria)
	require.NoError(t, err)
	if assert.Len(t, alerts.Results, 1, "alerts of a reused token") {
		assert.EqualValues(t, 2, alerts.Results[0].Level, "only the current incarnation's alert")
	}
	changes, err := api.StateChangeEvents(ctx, criteria)
	require.NoError(t, err)
	if assert.Len(t, changes.Results, 1, "state changes of a reused token") {
		assert.EqualValues(t, 2, changes.Results[0].SessionId, "only the current incarnation's transition")
	}

	// Bucketed reads, both paths: one bucket over the whole range, so sum and count are the
	// totals of what the read admitted. The interval is rollup-eligible; the raw path is
	// forced with the operator's kill-switch.
	refreshRetentionRollup(t, systemDB(api.RDB))
	bucketed := func(name string, token string) (sum float64, count int64) {
		t.Helper()
		buckets, err := api.BucketedMeasurements(ctx, MeasurementAggregationCriteria{
			DeviceToken: &token, StartTime: &reincarnationRange.start, EndTime: &reincarnationRange.end,
			IntervalSeconds: int64(6 * time.Hour / time.Second)})
		require.NoErrorf(t, err, "%s bucketed read of %s", name, token)
		for _, b := range buckets {
			sum += b.Sum.Float64
			count += b.Count
		}
		return sum, count
	}
	for _, path := range []struct {
		name     string
		disabled bool
	}{{"rollup-eligible", false}, {"raw", true}} {
		api.RollupReadsDisabled = path.disabled
		for _, token := range []string{"pump-1", "pump-2"} {
			var wantSum float64
			want := wantReincarnation(token)
			for _, v := range want {
				wantSum += v
			}
			sum, count := bucketed(path.name, token)
			assert.EqualValuesf(t, len(want), count, "%s bucketed count of %s", path.name, token)
			assert.InDeltaf(t, wantSum, sum, 1e-9, "%s bucketed sum of %s", path.name, token)
		}
	}
	api.RollupReadsDisabled = false

	// The calculated-metric preview reads the device's stored readings by token too.
	preview, err := api.CalculatedEvents(ctx, CalculatedEventsRequest{DeviceToken: "pump-1",
		Expression: "temp * 2", DataType: "DOUBLE", Inputs: []CalculatedInput{{Key: "temp", DataType: "DOUBLE"}},
		From: reincarnationRange.start, To: reincarnationRange.end})
	require.NoError(t, err)
	assert.Len(t, preview.Samples, len(wantReincarnation("pump-1")),
		"the calculated preview samples only the current incarnation's readings")

	// A scoped caller is bounded too, by the read and not only by the anchor cleanup.
	scoped := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, ScopeAnchors: []string{"customer:cust-1"}})
	scopedMeasurements, err := api.MeasurementEvents(scoped, criteria)
	require.NoError(t, err)
	var got []float64
	for _, m := range scopedMeasurements.Results {
		got = append(got, m.Value.Float64)
	}
	assert.ElementsMatch(t, wantReincarnation("pump-1"), got, "a scoped read by token sees only the current incarnation")
	measurementsOnly := criteria
	measurementsOnly.EventTypes = []eventtype.EventType{eventtype.Measurement}
	scopedEvents, err := api.Events(scoped, measurementsOnly)
	require.NoError(t, err)
	assert.Len(t, scopedEvents.Results, len(wantReincarnation("pump-1")), "a scoped base-event read by token")
}
