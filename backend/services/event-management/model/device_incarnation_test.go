// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// reincarnationDeletedAt is when the first "pump-1" was deleted in every fixture below.
var reincarnationDeletedAt = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// reincarnationFixture is one stored measurement of "pump-1" (or of an untouched
// bystander), placed by its device-reported instant and by when the platform RECEIVED it.
// value doubles as the reading and as the fixture's name in an assertion.
type reincarnationFixture struct {
	device              string
	value               float64
	occurred, processed time.Duration // offsets from reincarnationDeletedAt
	current             bool          // belongs to the token's CURRENT incarnation
}

// reincarnationFixtures is the whole scenario: a device, deleted, then created again under
// the same token. Only receipt time separates the incarnations: a deleted device resolves
// no new events, so anything received at or after its deletion is the new device's.
var reincarnationFixtures = []reincarnationFixture{
	{"pump-1", 1, -time.Hour, -time.Hour, false},             // the old device's ordinary reading
	{"pump-1", 2, 2 * time.Hour, -10 * time.Minute, false},   // the old device, its clock running ahead
	{"pump-1", 3, -time.Second, -time.Second, false},         // the old device, the instant before deletion
	{"pump-1", 10, time.Hour, time.Hour, true},               // the new device
	{"pump-1", 11, -2 * time.Hour, 30 * time.Minute, true},   // the new device's store-and-forward reading
	{"pump-1", 12, -90 * time.Minute, 0, true},               // received AT the deletion instant
	{"pump-2", 20, -time.Hour, -time.Hour, true},             // a bystander with no deletion at all
	{"pump-2", 21, 30 * time.Minute, 30 * time.Minute, true}, // ...after the other token's deletion
}

// seedReincarnation stores every fixture as a measurement (base event + payload row) and
// then records the deletion of the first "pump-1", exactly as the deletion consumer does.
// The deletion is recorded AFTER the old events are stored, and also after the new ones:
// the bound is a read-time property, so the order the rows arrived in must not matter.
func seedReincarnation(t *testing.T, api *Api, tenant string) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	for _, f := range reincarnationFixtures {
		ev := Event{DeviceToken: f.device, EventType: eventtype.Measurement, Source: "mqtt",
			OccurredTime:  reincarnationDeletedAt.Add(f.occurred),
			ProcessedTime: reincarnationDeletedAt.Add(f.processed)}
		ev.EventId = DeriveEventId(tenant, &ev, []byte{byte(f.value)})
		require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			_, err := api.CreateMeasurementEvents(ctx, tx, []*MeasurementEventCreateRequest{{
				Event: ev, EntryOccurredTime: ev.OccurredTime, Name: "temp", Value: f64(f.value)}})
			return err
		}))
	}
	_, err := api.DeleteAnchorsForEntity(ctx, "device", "pump-1", reincarnationDeletedAt)
	require.NoError(t, err)
}

// wantReincarnation is the readings a read of token must return: its current incarnation's.
func wantReincarnation(token string) []float64 {
	var want []float64
	for _, f := range reincarnationFixtures {
		if f.device == token && f.current {
			want = append(want, f.value)
		}
	}
	return want
}

var reincarnationRange = struct{ start, end time.Time }{
	reincarnationDeletedAt.Add(-3 * time.Hour), reincarnationDeletedAt.Add(3 * time.Hour)}

// assertTokenReadsSeeOnlyTheCurrentIncarnation reads the base events and the measurement
// payload rows by token. Shared by the sqlite unit test and the Postgres integration test.
func assertTokenReadsSeeOnlyTheCurrentIncarnation(t *testing.T, api *Api, tenant string) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	for _, token := range []string{"pump-1", "pump-2"} {
		want := wantReincarnation(token)
		criteria := EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
			DeviceToken: &token, StartTime: &reincarnationRange.start, EndTime: &reincarnationRange.end}

		// The base-event read is limited to measurements: the integration test stores the
		// other kinds under the same token as well.
		baseCriteria := criteria
		baseCriteria.EventTypes = []eventtype.EventType{eventtype.Measurement}
		events, err := api.Events(ctx, baseCriteria)
		require.NoError(t, err)
		var processed []time.Time
		for _, e := range events.Results {
			processed = append(processed, e.ProcessedTime.UTC())
		}
		var wantProcessed []time.Time
		for _, f := range reincarnationFixtures {
			if f.device == token && f.current {
				wantProcessed = append(wantProcessed, reincarnationDeletedAt.Add(f.processed))
			}
		}
		assert.ElementsMatchf(t, wantProcessed, processed,
			"events of %s: a read by token must return only its current incarnation's events", token)
		assert.EqualValuesf(t, len(want), events.Pagination.TotalRecords,
			"events of %s: the total must count only the current incarnation", token)

		measurements, err := api.MeasurementEvents(ctx, criteria)
		require.NoError(t, err)
		var got []float64
		for _, m := range measurements.Results {
			got = append(got, m.Value.Float64)
		}
		assert.ElementsMatchf(t, want, got,
			"measurements of %s: a read by token must return only its current incarnation's readings", token)
		assert.EqualValuesf(t, len(want), measurements.Pagination.TotalRecords,
			"measurements of %s: the total must count only the current incarnation", token)
	}
}

func TestTokenReadsSeeOnlyTheCurrentIncarnation(t *testing.T) {
	api := newPersistenceTestApi(t)
	seedReincarnation(t, api, "acme")
	assertTokenReadsSeeOnlyTheCurrentIncarnation(t, api, "acme")

	// Another tenant's same token is not bounded by this tenant's deletion.
	seedOtherTenant := func() {
		ctx := core.WithTenant(context.Background(), "other")
		ev := Event{DeviceToken: "pump-1", EventType: eventtype.Measurement, Source: "mqtt",
			OccurredTime: reincarnationDeletedAt.Add(-time.Hour), ProcessedTime: reincarnationDeletedAt.Add(-time.Hour)}
		ev.EventId = DeriveEventId("other", &ev, []byte("other"))
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{{
			Event: ev, EntryOccurredTime: ev.OccurredTime, Name: "temp", Value: f64(99)}})
		require.NoError(t, err)
	}
	seedOtherTenant()
	token := "pump-1"
	other := core.WithTenant(context.Background(), "other")
	criteria := EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}, DeviceToken: &token}
	events, err := api.Events(other, criteria)
	require.NoError(t, err)
	assert.Len(t, events.Results, 1, "a deletion bounds only its own tenant's reads")
	measurements, err := api.MeasurementEvents(other, criteria)
	require.NoError(t, err)
	assert.Len(t, measurements.Results, 1, "a deletion bounds only its own tenant's reads")
}

// A read without a device token is not bounded: the deletion bound is a property of the
// token, and a tenant-wide read never named it.
func TestATenantWideReadIsNotBoundedByADeletion(t *testing.T) {
	api := newPersistenceTestApi(t)
	seedReincarnation(t, api, "acme")
	ctx := core.WithTenant(context.Background(), "acme")
	events, err := api.Events(ctx, EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}})
	require.NoError(t, err)
	assert.Len(t, events.Results, len(reincarnationFixtures))
}
