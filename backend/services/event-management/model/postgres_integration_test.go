// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the event write path against a REAL TimescaleDB.
//
// 🔴 WHY THIS FILE EXISTS, stated plainly: every other test of this package runs on
// in-memory sqlite, and sqlite cannot answer the questions that actually matter about
// this schema. event_id is a bytea column in a COMPOSITE PRIMARY KEY on a HYPERTABLE,
// upserted with ON CONFLICT — and sqlite stores []byte as a blob, implements ON CONFLICT
// differently, and has no hypertables, no chunks and no columnar compression at all. A
// green sqlite suite is therefore evidence about the Go code, not about the storage
// engine the code actually runs on.
//
// hack/migration-diff.sh does exercise the real server, but it compares
// `pg_dump --schema-only` and so captures NO ROWS: it can prove the table is shaped
// correctly and cannot prove a single insert works. Between the two gates there was a
// hole exactly the size of "does writing an event work", which is the whole change.
//
// Tagged `integration` so it stays out of the default `go test ./...` (which must run
// with no server). Run it with:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=devicechain -P \
//	  timescale/timescaledb:latest-pg16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration ./model/... -run Integration -v
package model

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPostgresApi runs the REAL migration chain through the REAL core/rdb path against a
// live TimescaleDB, then returns an Api on it — the same construction migrationdiff uses,
// so the schema under test is the deployed one rather than a lookalike. Each call uses a
// fresh instance id, so tests never share a database.
func newPostgresApi(t *testing.T, instance string) *Api {
	t.Helper()
	port, err := strconv.Atoi(envOr("DC_IT_PGPORT", "5432"))
	require.NoError(t, err, "DC_IT_PGPORT must be numeric")

	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "event-management"},
		Migrations:   Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": envOr("DC_IT_PGHOST", "localhost"),
				"port":     port,
				"username": envOr("DC_IT_PGUSER", "postgres"),
				"password": envOr("DC_IT_PGPASSWORD", "devicechain"),
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()), "run migrations on the real server")
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})

	// 🔴 Start from empty, EVERY run. The instance id is fixed, so a server that has run
	// these tests before still holds their rows — and because the payload tables carry no
	// unique constraint, a second run appends rather than colliding. That produced a test
	// which passed exactly once per container and then failed with counts that looked like
	// a regression in the code under test. A test whose result depends on how many times it
	// has been run before is worse than no test: it reports on the server's history.
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{
		"events", "location_events", "measurement_events",
		"alert_events", "event_anchors", "state_change_events",
	} {
		require.NoErrorf(t, sys.Exec(
			fmt.Sprintf(`TRUNCATE TABLE "event-management".%q`, table)).Error,
			"truncate %s before the run", table)
	}
	return NewApi(mgr)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// pgEvent builds a base event with its identity derived exactly as PersistEvent derives it.
func pgEvent(tenant, device, altId string, occurred time.Time, payload string) Event {
	ev := Event{
		DeviceToken:  device,
		EventType:    esmodel.Measurement,
		OccurredTime: occurred,
		Source:       "mqtt",
		AltId:        rdb.NullStrOf(&altId),
	}
	ev.EventId = DeriveEventId(tenant, &ev, []byte(payload))
	return ev
}

// TestIntegrationDistinctEventsAtOneNaturalKeyBothPersist is the acceptance test for the
// whole change, run where it can actually fail: two DISTINCT events sharing
// (tenant, device, event_type, occurred_time) must both survive an insert against real
// Postgres, through a real bytea composite primary key on a real hypertable.
//
// Under the old key the second event's envelope was silently dropped by ON CONFLICT DO
// NOTHING while its payload row still inserted against the first event.
func TestIntegrationDistinctEventsAtOneNaturalKeyBothPersist(t *testing.T) {
	api := newPostgresApi(t, "itdistinct")
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	first := pgEvent("acme", "device-1", "msg-A", occurred, "temperature=21.5")
	second := pgEvent("acme", "device-1", "msg-B", occurred, "humidity=55")

	// Negative control: they really do collide on the old key, so this test exercises
	// the defect rather than sidestepping it.
	require.Equal(t, first.DeviceToken, second.DeviceToken)
	require.Equal(t, first.EventType, second.EventType)
	require.True(t, first.OccurredTime.Equal(second.OccurredTime))
	require.NotEqual(t, first.EventId, second.EventId)

	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
		[]*MeasurementEventCreateRequest{{Event: first,
			EntryOccurredTime: first.OccurredTime, Name: "temperature", Value: f64(21.5)}})
	require.NoError(t, err, "first event must persist")
	_, err = api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
		[]*MeasurementEventCreateRequest{{Event: second,
			EntryOccurredTime: second.OccurredTime, Name: "humidity", Value: f64(55)}})
	require.NoError(t, err, "the colliding event must persist too")

	var events []Event
	require.NoError(t, api.RDB.DB(ctx).Order("alt_id").Find(&events).Error)
	require.Len(t, events, 2, "both distinct events must be stored")
	assert.Equal(t, "msg-A", events[0].AltId.String)
	assert.Equal(t, "msg-B", events[1].AltId.String)

	// Each payload row is attached to its OWN envelope, not merged onto the first.
	var measurements []MeasurementEvent
	require.NoError(t, api.RDB.DB(ctx).Order("name").Find(&measurements).Error)
	require.Len(t, measurements, 2)
	assert.Equal(t, events[1].EventId, measurements[0].EventId, "humidity belongs to msg-B")
	assert.Equal(t, events[0].EventId, measurements[1].EventId, "temperature belongs to msg-A")

	// And the colliding event is now visible to the dedup probe, which is what makes a
	// redelivery of it detectable at all.
	exists, err := api.EventExistsByAltId(ctx, api.RDB.DB(ctx), "msg-B", occurred)
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestIntegrationRedeliveryConvergesOnOneRow pins the other half of the identity contract
// on the real engine: re-presenting the SAME content derives the same event_id and the
// upsert's ON CONFLICT DO NOTHING absorbs it. This is the behaviour that makes the digest
// safe to use as a key at all, and ON CONFLICT semantics are engine-specific enough that
// sqlite agreeing proves nothing about Postgres.
func TestIntegrationRedeliveryConvergesOnOneRow(t *testing.T) {
	api := newPostgresApi(t, "itredeliver")
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := pgEvent("acme", "device-1", "msg-A", occurred, "temperature=21.5")
	for i := 0; i < 3; i++ {
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
			[]*MeasurementEventCreateRequest{{Event: ev,
				EntryOccurredTime: ev.OccurredTime, Name: "temperature", Value: f64(21.5)}})
		require.NoErrorf(t, err, "delivery %d must not error", i+1)
	}

	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Event{}).Count(&count).Error)
	assert.EqualValues(t, 1, count, "three deliveries of one event must converge on one row")
}

// TestIntegrationNoAltIdRedeliveryDuplicatesNothing exercises the shape lwm2m-ingest and
// sparkplug-ingest actually produce — an event carrying NO alternateId — on the real engine.
//
// Neither service sets AltId anywhere, so PersistEvent's dedup probe never engages for them,
// and redelivery is a designed-for path (ADR-022 at-least-once). Before the payload rows and
// the anchor set carried identities of their own, three deliveries left one envelope owning
// three copies of its measurement and three copies of its anchors. Postgres is where this
// has to be checked: the dedup rests on ON CONFLICT against unique indexes on bytea columns
// of hypertables, none of which sqlite models.
func TestIntegrationNoAltIdRedeliveryDuplicatesNothing(t *testing.T) {
	api := newPostgresApi(t, "itnoaltid")
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := Event{
		DeviceToken:  "device-1",
		EventType:    esmodel.Measurement,
		OccurredTime: occurred,
		Source:       "lwm2m",
	}
	ev.EventId = DeriveEventId("acme", &ev, []byte("temperature=21.5"))
	require.False(t, ev.AltId.Valid,
		"negative control: no alternateId, so nothing upstream can catch the redelivery")

	for i := 0; i < 3; i++ {
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
			[]*MeasurementEventCreateRequest{{Event: ev,
				EntryOccurredTime: ev.OccurredTime, Name: "temperature", Value: f64(21.5)}})
		require.NoErrorf(t, err, "delivery %d", i+1)
		require.NoErrorf(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{{
			EventId: ev.EventId, DeviceToken: "device-1", EventType: esmodel.Measurement,
			OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3",
		}}), "anchors, delivery %d", i+1)
	}

	for _, c := range []struct {
		what  string
		model any
	}{
		{"base events", &Event{}},
		{"measurement rows", &MeasurementEvent{}},
		{"anchor rows", &EventAnchor{}},
	} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(c.model).Count(&n).Error)
		assert.EqualValues(t, 1, n, "three deliveries must leave exactly one of the %s", c.what)
	}
}

// TestIntegrationCompressionEnablesWithTheIdentityKey is the risk this change most needed
// checking and that no existing gate could see.
//
// Compression is turned on at RUNTIME by the data-lifecycle reconciler, not by a
// migration, so hack/migration-diff.sh never reaches it. Timescale constrains which
// columns a compressed hypertable's unique constraints may cover relative to
// compress_segmentby / compress_orderby — and this change moved the primary key onto a
// column (event_id) that is in NEITHER. The old key had device_token and event_type
// outside them too, so the situation is believed equivalent; "believed equivalent" is
// exactly the claim worth converting into a measurement before tagging a release.
//
// It uses the production statement from lifecycle.go rather than a hand-written one, so
// a change to the real compression settings moves this test with it.
func TestIntegrationCompressionEnablesWithTheIdentityKey(t *testing.T) {
	api := newPostgresApi(t, "itcompress")
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := pgEvent("acme", "device-1", "msg-A", occurred, "temperature=21.5")
	_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx),
		[]*MeasurementEventCreateRequest{{Event: ev,
			EntryOccurredTime: ev.OccurredTime, Name: "temperature", Value: f64(21.5)}})
	require.NoError(t, err)

	// 🔴 EVERY GOVERNED HYPERTABLE, not a sample of three. The segment keys are now
	// per-table (compressSegmentBy), so "it worked on measurement_events" no longer
	// carries the others: a column named in one table's list and absent from another,
	// or one that collides with compress_orderby, is rejected by Timescale per table.
	// The reconciler's exec is best-effort, so such a rejection ships as a single ERROR
	// log line and a hypertable that silently never compresses — invisible until a disk
	// fills. Driving LifecycleHypertables means a table added to the set is covered the
	// day it joins, with no list here to fall out of step.
	sys := api.RDB.DB(core.WithSystemContext(ctx))
	for _, table := range LifecycleHypertables {
		require.NoErrorf(t, sys.Exec(enableCompressionStmt(table)).Error,
			"compression must enable on %s with segmentby %q and the identity primary key",
			table, segmentByFor(table))
	}

	// Enabling is necessary but not sufficient — compress a real chunk and read back
	// through it, because a compressed chunk that cannot be queried is the ADR-028
	// failure mode (cluster healthy, extension listed, every row returning nothing).
	var chunk string
	require.NoError(t, sys.Raw(
		`SELECT c.chunk_schema || '.' || c.chunk_name FROM timescaledb_information.chunks c
		 WHERE c.hypertable_name = 'events' LIMIT 1`).Scan(&chunk).Error)
	require.NotEmpty(t, chunk, "the insert above must have produced a chunk to compress")
	require.NoError(t, sys.Exec(fmt.Sprintf(`SELECT compress_chunk('%s')`, chunk)).Error,
		"a chunk of events must compress with the identity primary key")

	var events []Event
	require.NoError(t, api.RDB.DB(ctx).Find(&events).Error)
	require.Len(t, events, 1, "the row must still be readable through the compressed chunk")
	assert.Equal(t, ev.EventId, events[0].EventId)
}
