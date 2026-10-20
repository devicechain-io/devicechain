// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Scoped reads of the event store against a REAL TimescaleDB. The scope predicates are
// correlated EXISTS subqueries over a hypertable, and bucketedMeasurements has a
// continuous-aggregate path sqlite cannot have at all, so this is where the rule is
// proven on the engine it runs on. Run it with:
//
//	DC_IT_PGPORT=<port> DC_IT_PGPASSWORD=<password> \
//	  go test -tags integration -count=1 ./graphql/... -run OwnershipPeriod -v
package graphql

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newScopeIntegrationApi runs the real migration chain on a live TimescaleDB and returns
// an Api on it, with every event table emptied first so a rerun starts from nothing.
func newScopeIntegrationApi(t *testing.T) *model.Api {
	t.Helper()
	const instance = "itscopeownership"
	port, err := strconv.Atoi(itEnv("DC_IT_PGPORT", "5432"))
	require.NoError(t, err)
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"),
		itEnv("DC_IT_PGPASSWORD", "devicechain")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-management"},
		Migrations:   migrationsThrough(t, os.Getenv("DC_IT_MIGRATE_THROUGH")),
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{"events", "location_events", "measurement_events",
		"alert_events", "event_anchors", "state_change_events"} {
		require.NoError(t, sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "event-management".%q`, table)).Error)
	}
	return model.NewApi(mgr)
}

// migrationsThrough is the whole chain, or — when id is set — the chain up to and
// including migration id. The prefix exists for a server whose TimescaleDB is newer than
// the image CI pins: the key-rebuild migrations read a catalog column
// (_timescaledb_catalog.chunk.compressed_chunk_id) that later releases removed, so the
// full chain cannot run there. The scope rules under test read no index and no key, so
// a prefix that stops before the rebuilds proves the same behaviour; the full chain is
// the default and what CI's pinned image runs.
func migrationsThrough(t *testing.T, id string) []*gormigrate.Migration {
	t.Helper()
	if id == "" {
		return model.Migrations
	}
	for i, m := range model.Migrations {
		if m.ID == id {
			return model.Migrations[:i+1]
		}
	}
	t.Fatalf("migration %s is not in the chain", id)
	return nil
}

func fp(v float64) *float64 { return &v }

const ownershipTenant = "tenant1"

// recordPeriod writes, for one device and one owner, the three kinds of event a device
// reports — a measurement event carrying a BUFFERED reading (30 s before its envelope)
// and a live one, a location fix and an alert — each anchored to anchor, as the
// persistence worker writes them while the device belongs to that owner.
func recordPeriod(t *testing.T, api *model.Api, device, anchorToken string, at time.Time, base float64) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), ownershipTenant)
	db := api.RDB.DB(ctx)
	parent := func(kind eventtype.EventType, when time.Time) model.Event {
		e := model.Event{DeviceToken: device, EventType: kind, OccurredTime: when, ProcessedTime: when, Source: "mqtt"}
		e.EventId = model.DeriveEventId(ownershipTenant, &e, []byte(anchorToken))
		return e
	}
	m := parent(eventtype.Measurement, at)
	_, err := api.CreateMeasurementEvents(ctx, db, []*model.MeasurementEventCreateRequest{
		{Event: m, EntryOccurredTime: at.Add(-30 * time.Second), Name: "temperature", Value: fp(base)},
		{Event: m, EntryOccurredTime: at, Name: "temperature", Value: fp(2 * base)},
	})
	require.NoError(t, err)
	l := parent(eventtype.Location, at.Add(time.Minute))
	_, err = api.CreateLocationEvents(ctx, db, []*model.LocationEventCreateRequest{
		{Event: l, EntryOccurredTime: l.OccurredTime, Latitude: fp(base), Longitude: fp(base)},
	})
	require.NoError(t, err)
	a := parent(eventtype.Alert, at.Add(2*time.Minute))
	_, err = api.CreateAlertEvents(ctx, db, []*model.AlertEventCreateRequest{
		{Event: a, EntryOccurredTime: a.OccurredTime, Type: "door", Level: 1, Message: anchorToken + "-alert", Source: "device"},
	})
	require.NoError(t, err)
	anchors := make([]*model.EventAnchor, 0, 3)
	for _, e := range []model.Event{m, l, a} {
		anchors = append(anchors, &model.EventAnchor{EventId: e.EventId, DeviceToken: device, EventType: e.EventType,
			OccurredTime: e.OccurredTime, AnchorType: "customer", AnchorToken: anchorToken})
	}
	require.NoError(t, api.CreateEventAnchors(ctx, db, anchors))
}

// A device belonged to Acme, then was reassigned to Globex. Each customer's member reads
// exactly the events recorded while the device was theirs — through every event read the
// API offers, including both paths of the bucketed aggregation — and the unscoped member
// reads both periods (the negative control: what a scoped member does not see was
// filtered, not missing).
func TestEventsVisibleForTheOwnershipPeriod(t *testing.T) {
	api := newScopeIntegrationApi(t)
	acmeAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	globexAt := acmeAt.Add(time.Hour)
	recordPeriod(t, api, "dev-1", "acme", acmeAt, 1)      // readings 1, 2
	recordPeriod(t, api, "dev-1", "globex", globexAt, 10) // readings 10, 20

	authorities := []string{string(auth.EventRead), string(auth.LocationRead)}
	acme := scopedCtx(api, ownershipTenant, []string{"customer:acme"}, authorities...)
	globex := scopedCtx(api, ownershipTenant, []string{"customer:globex"}, authorities...)
	everyone := scopedCtx(api, ownershipTenant, nil, authorities...)
	r := &SchemaResolver{}
	page := func(anchor *EventAnchorInput) struct{ Criteria EventSearchCriteriaInput } {
		return struct{ Criteria EventSearchCriteriaInput }{
			Criteria: EventSearchCriteriaInput{PageNumber: 1, PageSize: 100, Anchor: anchor}}
	}

	// events: the base envelopes, as their occurred times.
	events := func(ctx context.Context, anchor *EventAnchorInput) []string {
		t.Helper()
		found, err := r.Events(ctx, page(anchor))
		require.NoError(t, err)
		out := []string{}
		for _, e := range found.Results() {
			at, err := time.Parse(time.RFC3339, *e.OccurredTime())
			require.NoError(t, err)
			out = append(out, at.UTC().Format(time.RFC3339))
		}
		sort.Strings(out)
		return out
	}
	times := func(at time.Time) []string {
		out := []string{}
		for _, d := range []time.Duration{0, time.Minute, 2 * time.Minute} {
			out = append(out, at.Add(d).Format(time.RFC3339))
		}
		return out
	}
	assert.Equal(t, times(acmeAt), events(acme, nil), "events: Acme sees its period only")
	assert.Equal(t, times(globexAt), events(globex, nil), "events: Globex sees its period only")
	assert.Len(t, events(everyone, nil), 6, "events control: the unscoped member sees both periods")

	// measurementEvents, including the buffered reading stored before its envelope's
	// instant: a payload row is reached through its event's id, not through a time.
	values := func(ctx context.Context, anchor *EventAnchorInput) []float64 {
		t.Helper()
		found, err := r.MeasurementEvents(ctx, page(anchor))
		require.NoError(t, err)
		out := []float64{}
		for _, m := range found.Results() {
			out = append(out, *m.Value())
		}
		sort.Float64s(out)
		return out
	}
	assert.Equal(t, []float64{1, 2}, values(acme, nil), "measurementEvents: Acme")
	assert.Equal(t, []float64{10, 20}, values(globex, nil), "measurementEvents: Globex")
	assert.Equal(t, []float64{1, 2, 10, 20}, values(everyone, nil), "measurementEvents control")

	// locationEvents, behind location:read.
	latitudes := func(ctx context.Context) []float64 {
		t.Helper()
		found, err := r.LocationEvents(ctx, page(nil))
		require.NoError(t, err)
		out := []float64{}
		for _, l := range found.Results() {
			out = append(out, *l.Latitude())
		}
		sort.Float64s(out)
		return out
	}
	assert.Equal(t, []float64{1}, latitudes(acme), "locationEvents: Acme")
	assert.Equal(t, []float64{10}, latitudes(globex), "locationEvents: Globex")
	assert.Equal(t, []float64{1, 10}, latitudes(everyone), "locationEvents control")
	// location:read alone admits a scoped member, and still only to its own period.
	locOnly := scopedCtx(api, ownershipTenant, []string{"customer:acme"}, string(auth.LocationRead))
	assert.Equal(t, []float64{1}, latitudes(locOnly), "locationEvents: location:read alone, scoped")

	// alertEvents.
	alerts := func(ctx context.Context) []string {
		t.Helper()
		found, err := r.AlertEvents(ctx, page(nil))
		require.NoError(t, err)
		out := []string{}
		for _, a := range found.Results() {
			out = append(out, *a.Message())
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, []string{"acme-alert"}, alerts(acme), "alertEvents: Acme")
	assert.Equal(t, []string{"globex-alert"}, alerts(globex), "alertEvents: Globex")
	assert.Equal(t, []string{"acme-alert", "globex-alert"}, alerts(everyone), "alertEvents control")

	// bucketedMeasurements, on a rollup-eligible interval (60 s, which an unscoped
	// caller is served from the continuous aggregate) and on a sub-minute one (raw
	// either way). The sum over every bucket is the readings the caller can see.
	start, end := acmeAt.Add(-time.Hour).Format(time.RFC3339), globexAt.Add(time.Hour).Format(time.RFC3339)
	total := func(ctx context.Context, interval int32, anchor *EventAnchorInput) float64 {
		t.Helper()
		buckets, err := r.BucketedMeasurements(ctx, struct {
			Criteria MeasurementAggregationCriteriaInput
		}{Criteria: MeasurementAggregationCriteriaInput{
			StartTime: &start, EndTime: &end, IntervalSeconds: interval, Anchor: anchor}})
		require.NoError(t, err)
		sum := 0.0
		for _, b := range buckets {
			if s := b.Sum(); s != nil {
				sum += *s
			}
		}
		return sum
	}
	for _, interval := range []int32{60, 10} {
		assert.InDeltaf(t, 3, total(acme, interval, nil), 1e-9, "bucketedMeasurements @%ds: Acme", interval)
		assert.InDeltaf(t, 30, total(globex, interval, nil), 1e-9, "bucketedMeasurements @%ds: Globex", interval)
		assert.InDeltaf(t, 33, total(everyone, interval, nil), 1e-9, "bucketedMeasurements @%ds control", interval)
	}

	// The anchor filter argument cannot reach past the scope: Acme filtering by Globex
	// finds nothing, on every read that takes the argument, while filtering by its own
	// anchor still finds its rows (the control that the filter itself works).
	globexAnchor := &EventAnchorInput{Type: "customer", Token: "globex"}
	acmeAnchor := &EventAnchorInput{Type: "customer", Token: "acme"}
	assert.Empty(t, events(acme, globexAnchor), "events: Acme filtering by Globex")
	assert.Empty(t, values(acme, globexAnchor), "measurementEvents: Acme filtering by Globex")
	assert.Zero(t, total(acme, 60, globexAnchor), "bucketedMeasurements: Acme filtering by Globex")
	assert.Equal(t, times(acmeAt), events(acme, acmeAnchor), "control: Acme filtering by Acme")
	assert.Equal(t, []float64{1, 2}, values(acme, acmeAnchor), "control: Acme filtering by Acme")
	assert.Equal(t, []float64{10, 20}, values(everyone, globexAnchor), "control: unscoped filtering by Globex")
}
