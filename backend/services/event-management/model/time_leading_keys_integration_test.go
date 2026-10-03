// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the time-leading identity keys (NewTimeLeadingKeysSchema), against
// a real TimescaleDB. Run as postgres_integration_test.go describes.
package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// eventStoreIndexes is every index the six event hypertables carry at the end of the chain,
// by table, as pg_indexes renders its definition: the trim's set with the five identity keys
// rebuilt to lead with time and the four tenant-time indexes gone. Transcribed from the
// golden schema, so a changed column order fails as surely as an extra or missing name.
var eventStoreIndexes = map[string]map[string]string{
	"events": {
		"events_pkey":                           `CREATE UNIQUE INDEX events_pkey ON "event-management".events USING btree (tenant_id, occurred_time, event_id)`,
		"events_device_token_occurred_time_idx": `CREATE INDEX events_device_token_occurred_time_idx ON "event-management".events USING btree (device_token, occurred_time DESC)`,
		"idx_events_tenant_alt_id":              `CREATE UNIQUE INDEX idx_events_tenant_alt_id ON "event-management".events USING btree (tenant_id, alt_id, occurred_time) WHERE (alt_id IS NOT NULL)`,
	},
	"location_events": {
		"uq_location_events_idem": `CREATE UNIQUE INDEX uq_location_events_idem ON "event-management".location_events USING btree (tenant_id, occurred_time, payload_id)`,
	},
	"measurement_events": {
		"measurement_events_occurred_time_idx":    `CREATE INDEX measurement_events_occurred_time_idx ON "event-management".measurement_events USING btree (occurred_time DESC)`,
		"idx_measurement_tenant_device_name_time": `CREATE INDEX idx_measurement_tenant_device_name_time ON "event-management".measurement_events USING btree (tenant_id, device_token, name, occurred_time DESC)`,
		"uq_measurement_events_idem":              `CREATE UNIQUE INDEX uq_measurement_events_idem ON "event-management".measurement_events USING btree (tenant_id, occurred_time, payload_id)`,
	},
	"alert_events": {
		"uq_alert_events_idem": `CREATE UNIQUE INDEX uq_alert_events_idem ON "event-management".alert_events USING btree (tenant_id, occurred_time, payload_id)`,
	},
	"event_anchors": {
		"idx_event_anchors_lookup": `CREATE INDEX idx_event_anchors_lookup ON "event-management".event_anchors USING btree (tenant_id, anchor_type, anchor_token, occurred_time DESC)`,
		"uq_event_anchors_idem":    `CREATE UNIQUE INDEX uq_event_anchors_idem ON "event-management".event_anchors USING btree (tenant_id, occurred_time, event_id, anchor_type, anchor_token)`,
	},
	"state_change_events": {
		"uq_state_change_events_idem": `CREATE UNIQUE INDEX uq_state_change_events_idem ON "event-management".state_change_events USING btree (tenant_id, device_token, occurred_time, state, session_id)`,
	},
}

func rekeyID() string { return NewTimeLeadingKeysSchema().ID }

// testRekeyTiming is a fast timing for tests that are not about the bounds.
var testRekeyTiming = timeLeadingKeysTiming{
	lockTimeout: 500 * time.Millisecond, lockAttempt: time.Second, pause: 200 * time.Millisecond,
	tableBuild: 30 * time.Second, minBuild: 10 * time.Millisecond, budget: 60 * time.Second,
	countTimeout: 10 * time.Second, maxRows: 1_000_000, maxChunks: eventStoreMaxChunks, buildMemory: "64MB",
}

// assertFinalIndexes asserts every event hypertable, and every chunk of it, carries exactly
// the final set.
func assertFinalIndexes(t *testing.T, db *gorm.DB, minChunks int) {
	t.Helper()
	for _, table := range LifecycleHypertables {
		assert.Equalf(t, eventStoreIndexes[table], hypertableIndexes(t, db, table), "indexes on %s", table)
	}
	assertChunkSets(t, db, minChunks, func(table string) []string { return sortedNames(eventStoreIndexes[table]) })
}

// assertAfterTrimIndexes asserts the hypertables still carry the trim's set: every key on
// its OLD columns, every tenant-time index present.
func assertAfterTrimIndexes(t *testing.T, db *gorm.DB, what string) {
	t.Helper()
	for _, table := range LifecycleHypertables {
		assert.Equalf(t, afterTrimIndexes[table], hypertableIndexes(t, db, table), "%s: indexes on %s", what, table)
	}
}

func pkeyDef(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var def string
	require.NoError(t, db.Raw(`SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
		JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE n.nspname = 'event-management' AND c.conname = 'events_pkey'`).Scan(&def).Error)
	return def
}

// TestIntegrationIdentityKeysLeadWithTime is the by-value test of the change: after the
// real chain, every event hypertable and every chunk of it carries exactly the final set,
// and events_pkey is a PRIMARY KEY on (tenant_id, occurred_time, event_id). On main this
// fails listing five column orders and four extra names on every table and chunk.
func TestIntegrationIdentityKeysLeadWithTime(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "itrekey"))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	assertFinalIndexes(t, sys, 2)
	assert.Equal(t, "PRIMARY KEY (tenant_id, occurred_time, event_id)", pkeyDef(t, sys))

	// Negative control: the reader reads.
	require.NoError(t, sys.Exec(`CREATE INDEX t3_probe ON "event-management".alert_events (source)`).Error)
	assert.NotEqual(t, eventStoreIndexes["alert_events"], hypertableIndexes(t, sys, "alert_events"),
		"the index reader must see an index outside the final set")
	require.NoError(t, sys.Exec(`DROP INDEX "event-management".t3_probe`).Error)
}

// compressOldest enables compression on every event hypertable and compresses its oldest
// chunk, checking from the rows that it really is compressed. It returns the chunks.
func compressOldest(t *testing.T, sys *gorm.DB) map[string]string {
	t.Helper()
	oldChunk := map[string]string{}
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(enableCompressionStmt(table)).Error, "enable compression on %s", table)
		var chunk string
		require.NoError(t, sys.Raw(`SELECT format('%I.%I', chunk_schema, chunk_name)
			FROM timescaledb_information.chunks
			WHERE hypertable_schema = 'event-management' AND hypertable_name = ?
			ORDER BY range_start LIMIT 1`, table).Scan(&chunk).Error)
		require.NotEmpty(t, chunk)
		oldChunk[table] = chunk
		require.NoError(t, sys.Exec(`SELECT compress_chunk(?::regclass)`, chunk).Error, "compress %s", chunk)
		var status int
		require.NoError(t, sys.Raw(`SELECT c.status FROM _timescaledb_catalog.chunk c
			WHERE format('%I.%I', c.schema_name, c.table_name) = ?`, chunk).Scan(&status).Error)
		require.Equalf(t, 1, status&1, "%s must be compressed", chunk)
		var raw int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM ONLY `+chunk).Scan(&raw).Error)
		require.Zerof(t, raw, "%s must hold no uncompressed rows", chunk)
	}
	return oldChunk
}

// TestIntegrationTimeLeadingKeysApplyOverCompressedChunks upgrades a database whose older
// chunks are compressed — the shape every long-lived instance has — and proves the keys are
// rebuilt on the compressed chunks too, the reads still return every row in order, and
// uniqueness still holds in the compressed range: through the real writer (a redelivery
// stores nothing) and against a raw duplicate (refused by events_pkey).
func TestIntegrationTimeLeadingKeysApplyOverCompressedChunks(t *testing.T) {
	inst := freshInstance(t, "itrekeyz")
	before := newPostgresManagerWith(t, inst, migrationsBefore(t, rekeyID()))
	sys := systemDB(before)
	twoChunkSeed(t, sys)
	oldChunk := compressOldest(t, sys)
	// The test starts from the OLD shape, on every chunk, compressed ones included.
	assertChunkSets(t, sys, 2, func(table string) []string { return sortedNames(afterTrimIndexes[table]) })

	after := newPostgresManagerWith(t, inst, Migrations)
	sys = systemDB(after)
	assertFinalIndexes(t, sys, 2)
	assert.Equal(t, "PRIMARY KEY (tenant_id, occurred_time, event_id)", pkeyDef(t, sys))

	api := NewApi(after)
	ctx := core.WithTenant(context.Background(), "acme")
	d0 := "dev-0"
	newer := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	assertReads := func(stage string) {
		byDevice, err := api.Events(ctx, EventSearchCriteria{DeviceToken: &d0})
		require.NoError(t, err)
		require.Lenf(t, byDevice.Results, 2, "%s: device read", stage)
		assert.True(t, byDevice.Results[0].OccurredTime.Equal(newer), stage)
		assert.True(t, byDevice.Results[1].OccurredTime.Equal(older), stage)

		all, err := api.Events(ctx, EventSearchCriteria{})
		require.NoError(t, err)
		require.Lenf(t, all.Results, 4, "%s: tenant read", stage)
		assert.True(t, all.Results[0].OccurredTime.Equal(newer), stage)
		assert.True(t, all.Results[3].OccurredTime.Equal(older), stage)

		ms, err := api.MeasurementEvents(ctx, EventSearchCriteria{})
		require.NoError(t, err)
		require.Lenf(t, ms.Results, 4, "%s: measurement read", stage)
		assert.True(t, ms.Results[0].OccurredTime.Equal(newer), stage)
		assert.True(t, ms.Results[3].OccurredTime.Equal(older), stage)

		area := "area"
		token := "area-0"
		anchored, err := api.Events(ctx, EventSearchCriteria{AnchorType: &area, AnchorToken: &token})
		require.NoError(t, err)
		require.Lenf(t, anchored.Results, 2, "%s: anchor-filtered read of dev-0's area", stage)
		assert.Equal(t, "dev-0", anchored.Results[0].DeviceToken, stage)
	}
	assertReads("compressed")

	count := func(table string) int64 {
		var n int64
		require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".`+table).Scan(&n).Error)
		return n
	}
	// Through the real writer, in the COMPRESSED range: the first delivery of a new event
	// adds exactly one row to each table (so the "unchanged" below can see a change), and a
	// redelivery adds nothing.
	ev := Event{DeviceToken: "dev-0", EventType: esmodel.Measurement, OccurredTime: older.Add(time.Minute),
		Source: "lwm2m"}
	ev.EventId = DeriveEventId("acme", &ev, []byte("temperature=30"))
	require.False(t, ev.AltId.Valid, "no alternate id: the redelivery is caught by the keys, not the alt-id probe")
	write := func() {
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{{
			Event: ev, EntryOccurredTime: ev.OccurredTime, Name: "temperature", Value: f64(30)}})
		require.NoError(t, err)
		require.NoError(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{{
			EventId: ev.EventId, DeviceToken: "dev-0", EventType: esmodel.Measurement,
			OccurredTime: ev.OccurredTime, AnchorType: "customer", AnchorToken: "cust-1"}}))
	}
	e0, m0, a0 := count("events"), count("measurement_events"), count("event_anchors")
	write()
	assert.Equal(t, e0+1, count("events"), "a new event in the compressed range is stored")
	assert.Equal(t, m0+1, count("measurement_events"))
	assert.Equal(t, a0+1, count("event_anchors"))
	write()
	assert.Equal(t, e0+1, count("events"), "a redelivery into the compressed range stores nothing")
	assert.Equal(t, m0+1, count("measurement_events"))
	assert.Equal(t, a0+1, count("event_anchors"))

	// A raw duplicate of a SEEDED row, which lives in the compressed chunk's columnar store,
	// is refused by the rebuilt key.
	err := sys.Exec(`INSERT INTO "event-management".events (tenant_id, event_id, device_token, event_type, occurred_time, source)
		SELECT tenant_id, event_id, device_token, event_type, occurred_time, source FROM "event-management".events
		WHERE occurred_time = ? AND device_token = 'dev-0' AND tenant_id = 'acme'`, older).Error
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "a duplicate in the compressed range must be refused: %v", err)
	assert.Equal(t, "23505", pgErr.Code)
	assert.Truef(t, chunkIndexIs(pgErr.ConstraintName, "events_pkey"), "refused by %q", pgErr.ConstraintName)

	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, oldChunk[table]).Error)
	}
	assertChunkSets(t, sys, 2, func(table string) []string { return sortedNames(eventStoreIndexes[table]) })
	e0 = count("events")
	write()
	assert.Equal(t, e0, count("events"), "a redelivery after decompression stores nothing")
}

// TestIntegrationTimeLeadingKeysRefuseTooMuchHistory is the history gate, at its boundary:
// the small seed holds 4 rows in each of the five re-keyed tables (state_change_events is
// not re-keyed and not counted), so 20 rows. Under 19 the migration refuses, naming the
// recreate, and changes NOTHING; under 20 it applies. A compressed chunk's columnar rows
// are not counted, because the build does not index them.
func TestIntegrationTimeLeadingKeysRefuseTooMuchHistory(t *testing.T) {
	mgr := newPostgresManagerWith(t, freshInstance(t, "itrekeygate"), migrationsBefore(t, rekeyID()))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	timing := testRekeyTiming
	timing.maxRows = 19
	err := newTimeLeadingKeysSchema(timing).Migrate(sys)
	require.Error(t, err)
	for _, want := range []string{"more than 19 rows", "dcctl destroy", "dcctl bootstrap", "export",
		"events, measurement_events, location_events, alert_events, event_anchors"} {
		assert.Contains(t, err.Error(), want)
	}
	assertAfterTrimIndexes(t, sys, "after the refusal")

	timing.maxRows = 20
	require.NoError(t, newTimeLeadingKeysSchema(timing).Migrate(sys))
	assertFinalIndexes(t, sys, 2)

	// Control: with every chunk compressed, nothing is left for the build to index, and a
	// gate of 0 rows lets it apply. A gate that counted the compressed rows would refuse.
	mgr = newPostgresManagerWith(t, freshInstance(t, "itrekeygatez"), migrationsBefore(t, rekeyID()))
	sys = systemDB(mgr)
	twoChunkSeed(t, sys)
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(enableCompressionStmt(table)).Error)
		require.NoError(t, sys.Exec(`SELECT compress_chunk(format('%I.%I', chunk_schema, chunk_name)::regclass)
			FROM timescaledb_information.chunks
			WHERE hypertable_schema = 'event-management' AND hypertable_name = ?`, table).Error)
	}
	timing.maxRows = 0
	require.NoError(t, newTimeLeadingKeysSchema(timing).Migrate(sys))
	assertFinalIndexes(t, sys, 2)
}

// TestIntegrationTimeLeadingKeysWaitBoundedlyForALockAndResume holds one table across the
// migration: it ends within its budget with a busy error that names the table and the
// query finding its holder; the table before it is already re-keyed and the ones from it on
// are untouched (each table is its own transaction, so progress is monotone); the next run
// resumes and finishes; and no setting outlives its transaction.
func TestIntegrationTimeLeadingKeysWaitBoundedlyForALockAndResume(t *testing.T) {
	timing := timeLeadingKeysTiming{lockTimeout: 200 * time.Millisecond, lockAttempt: 500 * time.Millisecond,
		pause: 300 * time.Millisecond, tableBuild: 30 * time.Second, minBuild: 10 * time.Millisecond,
		budget: 3 * time.Second, countTimeout: 10 * time.Second, maxRows: 1_000_000, maxChunks: eventStoreMaxChunks, buildMemory: "77MB"}

	// Negative control: uncontended, the same migration finishes at once, so it is the lock
	// and not the harness that makes the run below wait.
	{
		mgr := newPostgresManagerWith(t, freshInstance(t, "itrekeyfree"), migrationsBefore(t, rekeyID()))
		sys := systemDB(mgr)
		twoChunkSeed(t, sys)
		start := time.Now()
		require.NoError(t, newTimeLeadingKeysSchema(timing).Migrate(sys))
		assert.Less(t, time.Since(start), 1500*time.Millisecond, "an uncontended re-key must not wait")
	}

	inst := freshInstance(t, "itrekeylock")
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, rekeyID()))
	sqlDB, err := mgr.Database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // the SHOWs at the end read the session the migration ran on
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	holder := connectInstance(t, inst)
	observer := connectInstance(t, inst)
	_, err = holder.Exec(context.Background(), `BEGIN; SELECT 1 FROM "event-management".measurement_events LIMIT 1`)
	require.NoError(t, err)
	holderPID := holder.PgConn().PID()

	start := time.Now()
	err = newTimeLeadingKeysSchema(timing).Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err, "a table busy past the budget must fail the migration")
	msg := err.Error()
	assert.Contains(t, msg, `"event-management".measurement_events`)
	assert.Contains(t, msg, "stayed busy")
	assert.Contains(t, msg, "continues from here on the next start")
	assert.Contains(t, msg, "already re-keyed: events")
	assert.NotContains(t, msg, "dcctl destroy", "a busy table is not a reason to recreate")
	assert.Contains(t, msg, indexTrimHolderQuery("measurement_events"))
	assert.Contains(t, holderPIDs(t, observer, indexTrimHolderQuery("measurement_events")), holderPID,
		"the busy error's query must find the session holding the table")
	assert.Less(t, elapsed, timing.budget+time.Second, "the run must end within its budget")

	// By value: events re-keyed, everything from the busy table on untouched.
	assert.Equal(t, eventStoreIndexes["events"], hypertableIndexes(t, sys, "events"))
	for _, table := range []string{"measurement_events", "location_events", "alert_events", "event_anchors"} {
		assert.Equalf(t, afterTrimIndexes[table], hypertableIndexes(t, sys, table), "%s after the failed run", table)
	}

	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)
	require.NoError(t, newTimeLeadingKeysSchema(timing).Migrate(sys), "the next run must resume and finish")
	assertFinalIndexes(t, sys, 2)

	for setting, want := range map[string]string{"lock_timeout": "0", "statement_timeout": "0"} {
		var v string
		require.NoError(t, sys.Raw("SHOW "+setting).Scan(&v).Error)
		assert.Equalf(t, want, v, "%s leaked out of the re-key's transaction", setting)
	}
	var mem string
	require.NoError(t, sys.Raw("SHOW maintenance_work_mem").Scan(&mem).Error)
	assert.NotEqual(t, "77MB", mem, "maintenance_work_mem leaked out of the re-key's transaction")
}

// TestIntegrationTimeLeadingKeysStartNoAttemptTheBudgetCannotHold: the budget is checked
// before every attempt, the first included. Another session holds events for part of the
// budget and measurement_events for all of it. events waits, gets its lock and is re-keyed;
// what is left then cannot hold a lock wait and the least swap, so measurement_events is
// not attempted at all: no ACCESS EXCLUSIVE queued on it (which would hold its ingest for
// a lock wait to no purpose), the run ends inside its budget with no slack, and the error
// is this start's budget being spent, not a busy table.
func TestIntegrationTimeLeadingKeysStartNoAttemptTheBudgetCannotHold(t *testing.T) {
	const holdFirst = 500 * time.Millisecond
	timing := timeLeadingKeysTiming{lockTimeout: time.Second, lockAttempt: time.Second,
		pause: 100 * time.Millisecond, tableBuild: 30 * time.Second, minBuild: 10 * time.Millisecond,
		// Room for events' first attempt to start (a lock wait and the least swap, plus
		// 390 ms for the catalog reads and the row count), but not for a second table once
		// events has waited holdFirst.
		budget: 1400 * time.Millisecond, countTimeout: 10 * time.Second, maxRows: 1_000_000, maxChunks: eventStoreMaxChunks, buildMemory: "64MB"}

	inst := freshInstance(t, "itrekeybudget")
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, rekeyID()))
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	first := connectInstance(t, inst)
	_, err := first.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS SHARE MODE`)
	require.NoError(t, err)
	second := connectInstance(t, inst)
	_, err = second.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".measurement_events IN ACCESS SHARE MODE`)
	require.NoError(t, err)
	defer func() { _, _ = second.Exec(context.Background(), `ROLLBACK`) }()

	released := make(chan error, 1)
	start := time.Now()
	go func() {
		time.Sleep(holdFirst)
		_, err := first.Exec(context.Background(), `ROLLBACK`)
		released <- err
	}()
	err = newTimeLeadingKeysSchema(timing).Migrate(sys)
	elapsed := time.Since(start)
	require.NoError(t, <-released)

	require.Error(t, err, "a budget spent before the second table must fail the migration")
	msg := err.Error()
	for _, want := range []string{`"event-management".measurement_events`, "budget of 1.4s was spent",
		"already re-keyed: events", "continues from here on the next start"} {
		assert.Contains(t, msg, want)
	}
	assert.NotContains(t, msg, "stayed busy", "the second table was never tried, so it was never busy")
	assert.NotContains(t, msg, "dcctl destroy")
	assert.GreaterOrEqual(t, elapsed, holdFirst, "events must have waited for its holder")
	assert.Less(t, elapsed, timing.budget, "the run must end inside its budget, with no slack")
	assert.Less(t, elapsed-holdFirst, timing.lockAttempt/2,
		"after events, nothing may wait on measurement_events' lock")

	assert.Equal(t, eventStoreIndexes["events"], hypertableIndexes(t, sys, "events"))
	for _, table := range []string{"measurement_events", "location_events", "alert_events", "event_anchors"} {
		assert.Equalf(t, afterTrimIndexes[table], hypertableIndexes(t, sys, table), "%s after the spent run", table)
	}
}

// TestIntegrationTimeLeadingKeysACancelledSwapIsNotTooSlow: an operator cancelling the
// swap (pg_cancel_backend raises the same 57014 a statement_timeout does) while it has its
// whole allowance is not the too-slow verdict. No marker is left, the error is the one
// retried on the next start, and the next start re-keys.
func TestIntegrationTimeLeadingKeysACancelledSwapIsNotTooSlow(t *testing.T) {
	inst := freshInstance(t, "itrekeycancel")
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, rekeyID()))
	sys := systemDB(mgr)
	// 200,000 rows a table, sorted in the least memory the server allows: a build long
	// enough to be caught running.
	seedEventStore(t, sys, []string{"acme"}, 200, 1000, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Minute)
	timing := testRekeyTiming
	timing.buildMemory = "1MB"

	done := make(chan error, 1)
	go func() { done <- newTimeLeadingKeysSchema(timing).Migrate(sys) }()

	observer := connectInstance(t, inst)
	cancelled := false
	for deadline := time.Now().Add(10 * time.Second); !cancelled && time.Now().Before(deadline); {
		var ok bool
		err := observer.QueryRow(context.Background(), `SELECT coalesce(bool_or(pg_cancel_backend(pid)), false)
			FROM pg_stat_activity WHERE state = 'active'
			AND query LIKE 'ALTER TABLE "event-management".events DROP CONSTRAINT events_pkey%'`).Scan(&ok)
		require.NoError(t, err)
		cancelled = ok
		if !cancelled {
			time.Sleep(2 * time.Millisecond)
		}
	}
	err := <-done
	require.True(t, cancelled, "the swap was never caught running; the migration returned %v", err)

	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "the server error stays wrapped: %v", err)
	assert.Equal(t, "57014", pgErr.Code)
	assert.Contains(t, err.Error(), "The next start tries again")
	assert.NotContains(t, err.Error(), "did not finish within", "a cancel is not the too-slow verdict")
	assert.Equal(t, "PRIMARY KEY (tenant_id, event_id, occurred_time)", pkeyDef(t, sys))
	var note *string
	require.NoError(t, sys.Raw(`SELECT obj_description('"event-management".events_pkey'::regclass, 'pg_class')`).Scan(&note).Error)
	assert.Nil(t, note, "no too-slow marker for a cancelled swap")

	require.NoError(t, newTimeLeadingKeysSchema(testRekeyTiming).Migrate(sys), "the next start re-keys")
	assertFinalIndexes(t, sys, 1)
}

// TestIntegrationTimeLeadingKeysLockChunksAndCompressedRelationsFirst holds, from another
// session, first one CHUNK of events and then one COMPRESSED relation of it — neither is
// the hypertable itself. Locking them up front makes either holder a busy table, retried
// and then reported as resumable. Were the swap to meet them instead, it would wait inside
// its build allowance (here shorter than lock_timeout), time out with the whole allowance,
// and be reported too slow: a recreate verdict from a transient holder.
func TestIntegrationTimeLeadingKeysLockChunksAndCompressedRelationsFirst(t *testing.T) {
	timing := timeLeadingKeysTiming{lockTimeout: time.Second, lockAttempt: 1500 * time.Millisecond,
		pause: 200 * time.Millisecond, tableBuild: 500 * time.Millisecond, minBuild: 10 * time.Millisecond,
		budget: 2500 * time.Millisecond, countTimeout: 10 * time.Second, maxRows: 1_000_000, maxChunks: eventStoreMaxChunks, buildMemory: "64MB"}

	for _, tc := range []struct {
		name     string
		relation func(t *testing.T, sys *gorm.DB) string
	}{
		{"a chunk", func(t *testing.T, sys *gorm.DB) string {
			var chunk string
			require.NoError(t, sys.Raw(`SELECT format('%I.%I', chunk_schema, chunk_name)
				FROM timescaledb_information.chunks
				WHERE hypertable_schema = 'event-management' AND hypertable_name = 'events'
				ORDER BY range_start DESC LIMIT 1`).Scan(&chunk).Error)
			return chunk
		}},
		{"a compressed relation", func(t *testing.T, sys *gorm.DB) string {
			compressOldest(t, sys)
			var rel string
			require.NoError(t, sys.Raw(`SELECT format('%I.%I', cc.schema_name, cc.table_name)
				FROM _timescaledb_catalog.chunk c
				JOIN _timescaledb_catalog.chunk cc ON cc.id = c.compressed_chunk_id
				JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
				WHERE h.schema_name = 'event-management' AND h.table_name = 'events'`).Scan(&rel).Error)
			return rel
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := freshInstance(t, "itrekeyheld")
			mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, rekeyID()))
			sys := systemDB(mgr)
			twoChunkSeed(t, sys)
			rel := tc.relation(t, sys)
			require.NotEmpty(t, rel)

			holder := connectInstance(t, inst)
			_, err := holder.Exec(context.Background(), `BEGIN; LOCK TABLE `+rel+` IN ACCESS SHARE MODE`)
			require.NoError(t, err)
			defer func() { _, _ = holder.Exec(context.Background(), `ROLLBACK`) }()

			err = newTimeLeadingKeysSchema(timing).Migrate(sys)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "stayed busy", "a held %s is a busy table", tc.name)
			assert.NotContains(t, err.Error(), "dcctl destroy", "a held %s must never read as too slow", tc.name)
			assert.Equal(t, "PRIMARY KEY (tenant_id, event_id, occurred_time)", pkeyDef(t, sys))
			var note *string
			require.NoError(t, sys.Raw(`SELECT obj_description('"event-management".events_pkey'::regclass, 'pg_class')`).Scan(&note).Error)
			assert.Nil(t, note, "no too-slow marker for a busy table")
		})
	}
}

// TestIntegrationTimeLeadingKeysTooSlowIsRolledBackAndSticks gives the swap a build
// allowance it cannot meet. The swap is rolled back whole (the table keeps its previous key
// and its tenant-time index), the error is the too-slow verdict with the recreate and what
// it costs, on the FIRST attempt; and the next start refuses at once, without locking
// anything — so a slow store does not stall ingest on every restart — until the marker is
// cleared, after which the re-key runs again.
func TestIntegrationTimeLeadingKeysTooSlowIsRolledBackAndSticks(t *testing.T) {
	inst := freshInstance(t, "itrekeyslow")
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, rekeyID()))
	sys := systemDB(mgr)
	// 200,000 rows a table: rebuilding events_pkey over them takes far more than 50 ms.
	seedEventStore(t, sys, []string{"acme"}, 200, 1000, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Minute)

	timing := timeLeadingKeysTiming{lockTimeout: time.Second, lockAttempt: 2 * time.Second,
		pause: 2 * time.Second, tableBuild: 50 * time.Millisecond, minBuild: 10 * time.Millisecond,
		budget: 20 * time.Second, countTimeout: 10 * time.Second, maxRows: 10_000_000, maxChunks: eventStoreMaxChunks, buildMemory: "64MB"}
	start := time.Now()
	err := newTimeLeadingKeysSchema(timing).Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err)
	for _, want := range []string{`"event-management".events`, "did not finish within 50ms", "previous key",
		"dcctl destroy", "dcctl bootstrap", "export", `COMMENT ON INDEX "event-management".events_pkey IS NULL`} {
		assert.Contains(t, err.Error(), want)
	}
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "the server error stays wrapped: %v", err)
	assert.Equal(t, "57014", pgErr.Code)
	assert.Less(t, elapsed, timing.pause, "too slow is decided on the first attempt, never retried")

	// Rolled back whole: the previous key and its tenant-time index are both still there.
	assert.Equal(t, "PRIMARY KEY (tenant_id, event_id, occurred_time)", pkeyDef(t, sys))
	assertAfterTrimIndexes(t, sys, "after the too-slow swap")
	var note string
	require.NoError(t, sys.Raw(`SELECT obj_description('"event-management".events_pkey'::regclass, 'pg_class')`).Scan(&note).Error)
	assert.True(t, strings.HasPrefix(note, timeLeadingKeysRefusedMarker), "the verdict is recorded: %q", note)

	// The next start, with a generous allowance, refuses at once — while another session
	// holds events ACCESS EXCLUSIVE, so any lock it tried to take would hang it.
	holder := connectInstance(t, inst)
	_, err = holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	start = time.Now()
	err = newTimeLeadingKeysSchema(testRekeyTiming).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refused key is refused without waiting on any lock")
	assert.Contains(t, err.Error(), "an earlier start found it too slow")
	assert.Contains(t, err.Error(), "dcctl destroy")
	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)

	require.NoError(t, sys.Exec(`COMMENT ON INDEX "event-management".events_pkey IS NULL`).Error)
	require.NoError(t, newTimeLeadingKeysSchema(testRekeyTiming).Migrate(sys), "cleared, the re-key runs again")
	assertFinalIndexes(t, sys, 1)
}

// TestIntegrationTimeLeadingKeysRefuseAShapeTheyDidNotWrite: a key in neither its old nor
// its new shape — other columns, or the old columns without uniqueness — ends the migration
// before ANY table is changed. Never guess at a shape this migration did not write.
func TestIntegrationTimeLeadingKeysRefuseAShapeTheyDidNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name, replace, want string
	}{
		{"other columns", `DROP INDEX "event-management".uq_location_events_idem;
			CREATE UNIQUE INDEX uq_location_events_idem ON "event-management".location_events (occurred_time, tenant_id, payload_id)`,
			"(occurred_time,tenant_id,payload_id)"},
		{"not unique", `DROP INDEX "event-management".uq_alert_events_idem;
			CREATE INDEX uq_alert_events_idem ON "event-management".alert_events (tenant_id, payload_id, occurred_time)`,
			"unique=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newPostgresManagerWith(t, freshInstance(t, "itrekeyshape"), migrationsBefore(t, rekeyID()))
			sys := systemDB(mgr)
			twoChunkSeed(t, sys)
			require.NoError(t, sys.Exec(tc.replace).Error)

			err := newTimeLeadingKeysSchema(testRekeyTiming).Migrate(sys)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "neither its previous shape")
			assert.Equal(t, afterTrimIndexes["events"], hypertableIndexes(t, sys, "events"),
				"no table may change, not even the ones before the unrecognised key")
			assert.Equal(t, afterTrimIndexes["measurement_events"], hypertableIndexes(t, sys, "measurement_events"))
		})
	}
}

// TestIntegrationTimeLeadingKeysRerunDoesNothing: run again on its own output, the migration
// changes nothing (every key keeps its oid) and locks nothing (it returns at once while
// another session holds events ACCESS EXCLUSIVE). A key already rebuilt whose tenant-time
// index is back gets that index dropped and nothing else.
func TestIntegrationTimeLeadingKeysRerunDoesNothing(t *testing.T) {
	inst := freshInstance(t, "itrekeyrerun")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)

	oids := func() map[string]uint32 {
		out := map[string]uint32{}
		for _, k := range timeLeadingKeys {
			var oid uint32
			require.NoError(t, sys.Raw(`SELECT ?::regclass::oid`, `"event-management".`+k.name).Scan(&oid).Error)
			out[k.name] = oid
		}
		return out
	}
	before := oids()

	holder := connectInstance(t, inst)
	_, err := holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, NewTimeLeadingKeysSchema().Migrate(sys))
	assert.Less(t, time.Since(start), time.Second, "a re-run takes no lock")
	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)
	assert.Equal(t, before, oids(), "a re-run rebuilds nothing")
	assertFinalIndexes(t, sys, 2)

	// Drop-only: the key is new, the redundant index is back.
	require.NoError(t, sys.Exec(`CREATE INDEX events_tenant_id_occurred_time_idx
		ON "event-management".events (tenant_id, occurred_time DESC)`).Error)
	require.NoError(t, NewTimeLeadingKeysSchema().Migrate(sys))
	assert.Equal(t, before, oids(), "the drop-only path rebuilds nothing")
	assertFinalIndexes(t, sys, 2)
}

// TestIntegrationReadsAreServedByTheTimeLeadingKeys EXPLAINs, on the final schema, every
// read whose serving index this change moved or relies on, over a seed large enough for
// the planner to prefer indexes, and asserts each is served by the index named for it.
// Rows (1)-(7) fail on main, where the tenant-wide reads name the tenant-time indexes this
// change drops.
//
// They plan the SQL gorm rendered, with literals inlined — a custom plan. Production binds
// parameters and may switch to a generic plan after five executions, so the device read and
// its COUNT are also planned generically.
//
// 🔴 THE SEED HAS 100 DEVICES A TENANT, AND THAT NUMBER IS A FINDING, NOT A CONVENIENCE.
// The trim's version of this test seeded 40, and at 40 the device PAGE (not its COUNT) is
// now planned on events_pkey: the key gives the page's whole order (occurred_time DESC,
// event_id DESC) with no sort, and with only 40 distinct tokens in the table the planner
// expects to find five rows of one device within about 200 of the tenant's newest. Before,
// every candidate needed an incremental sort on event_id and the device index won. At 100
// distinct tokens the device index wins again. The cost of the walk is the planner's guess
// being wrong: in a table with few distinct devices, the page of a device that has been
// quiet for a while walks the tenant's newer rows until it finds its five. That is recorded
// with the change; this test pins the per-device plan where it is the planner's choice.
func TestIntegrationReadsAreServedByTheTimeLeadingKeys(t *testing.T) {
	inst := freshInstance(t, "itrekeyplan")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	const perDevice = 50
	seedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	seedEventStore(t, sys, []string{"acme", "globex"}, 100, perDevice, seedStart, 19*24*time.Hour/perDevice)
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`ANALYZE "event-management".`+table).Error)
	}

	counter := rdbtest.NewStatementCounter(`"event-management"`)
	counter.Record(true)
	mgr.Database = mgr.Database.Session(&gorm.Session{Logger: counter})
	api := NewApi(mgr)
	api.RollupReadsDisabled = true
	ctx := core.WithTenant(context.Background(), "acme")
	device := "dev-7"
	page := rdb.Pagination{PageNumber: 1, PageSize: 5}

	// Tenant-wide reads, newest first: served by the key read backward.
	tenantRead := func(what, table, index string, call func() (int, error)) {
		counter.Reset()
		n, err := call()
		require.NoError(t, err, what)
		require.Equal(t, 5, n, what)
		requireServedBy(t, sys, explainScans(t, sys, nil, capture(t, counter, "ORDER BY")), table, index, what)
	}
	// (1) and (2): the base event page and its COUNT.
	counter.Reset()
	all, err := api.Events(ctx, EventSearchCriteria{Pagination: page})
	require.NoError(t, err)
	require.Len(t, all.Results, 5)
	assert.EqualValues(t, 100*perDevice, all.Pagination.TotalRecords)
	for i := 1; i < len(all.Results); i++ {
		assert.False(t, all.Results[i].OccurredTime.After(all.Results[i-1].OccurredTime), "newest first")
	}
	tenantData := capture(t, counter, "ORDER BY")
	requireServedBy(t, sys, explainScans(t, sys, nil, tenantData), "events", "events_pkey", "events tenant read")
	requireServedBy(t, sys, explainScans(t, sys, nil, capture(t, counter, "count(*)")), "events", "events_pkey",
		"events tenant COUNT")

	// (3) the load-test oracle's shape: a type and a time range, COUNT only.
	counter.Reset()
	from, to := seedStart.Add(5*24*time.Hour), seedStart.Add(6*24*time.Hour)
	_, err = api.Events(ctx, EventSearchCriteria{Pagination: page, EventTypes: []esmodel.EventType{esmodel.Measurement},
		StartTime: &from, EndTime: &to})
	require.NoError(t, err)
	scans := explainScans(t, sys, nil, capture(t, counter, "count(*)"))
	requireServedBy(t, sys, scans, "events", "events_pkey", "type + range COUNT")
	for _, s := range scansOf(t, sys, scans, "events") {
		assert.Contains(t, s.indexCond, "occurred_time", "the range must be sought, not filtered")
	}

	// (4) measurement tenant page, (6) location, (7) alert; and the device-filtered location
	// and alert reads, whose prefix is the same.
	tenantRead("measurement tenant read", "measurement_events", "uq_measurement_events_idem", func() (int, error) {
		r, err := api.MeasurementEvents(ctx, EventSearchCriteria{Pagination: page})
		return lenOr(r, err, func() int { return len(r.Results) })
	})
	tenantRead("location tenant read", "location_events", "uq_location_events_idem", func() (int, error) {
		r, err := api.LocationEvents(ctx, EventSearchCriteria{Pagination: page})
		return lenOr(r, err, func() int { return len(r.Results) })
	})
	tenantRead("alert tenant read", "alert_events", "uq_alert_events_idem", func() (int, error) {
		r, err := api.AlertEvents(ctx, EventSearchCriteria{Pagination: page})
		return lenOr(r, err, func() int { return len(r.Results) })
	})
	tenantRead("location device read", "location_events", "uq_location_events_idem", func() (int, error) {
		r, err := api.LocationEvents(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device})
		return lenOr(r, err, func() int { return len(r.Results) })
	})
	tenantRead("alert device read", "alert_events", "uq_alert_events_idem", func() (int, error) {
		r, err := api.AlertEvents(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device})
		return lenOr(r, err, func() int { return len(r.Results) })
	})

	// (5) the raw bucketed read without a device, over a day.
	counter.Reset()
	bfrom, bto := seedStart.Add(10*24*time.Hour), seedStart.Add(11*24*time.Hour)
	buckets, err := api.BucketedMeasurements(ctx, MeasurementAggregationCriteria{IntervalSeconds: 30,
		StartTime: &bfrom, EndTime: &bto})
	require.NoError(t, err)
	require.NotEmpty(t, buckets)
	scans = explainScans(t, sys, nil, capture(t, counter, "time_bucket"))
	requireServedBy(t, sys, scans, "measurement_events", "uq_measurement_events_idem", "raw bucketed read")
	for _, s := range scansOf(t, sys, scans, "measurement_events") {
		assert.Contains(t, s.indexCond, "occurred_time", "the raw bucketed range must be sought")
	}

	// Negative control: with events_pkey gone (in the rolled-back transaction) the tenant
	// page names something else, and the check says so.
	scans = explainScans(t, sys, []string{`ALTER TABLE "event-management".events DROP CONSTRAINT events_pkey`}, tenantData)
	own := scansOf(t, sys, scans, "events")
	require.NotEmptyf(t, own, "%v", scans)
	for _, s := range own {
		assert.False(t, chunkIndexIs(s.index, "events_pkey"), s.index)
	}

	// Unchanged: the device read, its COUNT and the device + rare-type read.
	counter.Reset()
	res, err := api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device})
	require.NoError(t, err)
	require.Len(t, res.Results, 5)
	assert.EqualValues(t, perDevice, res.Pagination.TotalRecords)
	deviceData := capture(t, counter, "ORDER BY")
	deviceCount := capture(t, counter, "count(*)")
	requireServedBy(t, sys, explainScans(t, sys, nil, deviceData), "events",
		"events_device_token_occurred_time_idx", "device read")
	requireServedBy(t, sys, explainScans(t, sys, nil, deviceCount), "events",
		"events_device_token_occurred_time_idx", "device read COUNT")
	counter.Reset()
	res, err = api.Events(ctx, EventSearchCriteria{Pagination: page, DeviceToken: &device,
		EventTypes: []esmodel.EventType{esmodel.Alert}})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	requireServedBy(t, sys, explainScans(t, sys, nil, capture(t, counter, "ORDER BY")), "events",
		"events_device_token_occurred_time_idx", "device + type read")

	generic := connectInstance(t, inst)
	for _, s := range []string{`SET enable_seqscan = off`, `SET enable_bitmapscan = off`} {
		_, err := generic.Exec(context.Background(), s)
		require.NoError(t, err)
	}
	for what, sql := range map[string]string{"device read (generic)": deviceData, "device COUNT (generic)": deviceCount} {
		param := strings.ReplaceAll(strings.ReplaceAll(sql, "'"+device+"'", "$1"), "'acme'", "$2")
		require.Contains(t, param, "$1")
		require.Contains(t, param, "$2")
		results, err := generic.PgConn().Exec(context.Background(), `EXPLAIN (FORMAT JSON, GENERIC_PLAN) `+param).ReadAll()
		require.NoError(t, err, what)
		require.Len(t, results, 1)
		require.Len(t, results[0].Rows, 1)
		scans, err := planIndexScans(results[0].Rows[0][0])
		require.NoError(t, err)
		requireServedBy(t, sys, scans, "events", "events_device_token_occurred_time_idx", what)
	}

	// Unchanged: the measurement_rollups refresh's cross-tenant range.
	requireServedBy(t, sys, explainScans(t, sys, nil,
		`SELECT count(*) FROM "event-management".measurement_events
		 WHERE occurred_time >= '2026-08-10T00:00:00Z' AND occurred_time < '2026-08-10T06:00:00Z'`),
		"measurement_events", "measurement_events_occurred_time_idx", "rollup refresh range")

	// AnchorsForEvent: a full equality seek on the anchor key.
	var one Event
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", device).Order("occurred_time").First(&one).Error)
	anchors, err := api.AnchorsForEvent(ctx, one.EventId, one.OccurredTime)
	require.NoError(t, err)
	require.Len(t, anchors, 1)
	assert.Equal(t, "area-3", anchors[0].AnchorToken)
	scans = explainScans(t, sys, nil,
		`SELECT * FROM "event-management".event_anchors WHERE event_id = ? AND occurred_time = ? AND tenant_id = 'acme'`,
		one.EventId, one.OccurredTime)
	requireServedBy(t, sys, scans, "event_anchors", "uq_event_anchors_idem", "anchors of one event")
	for _, s := range scansOf(t, sys, scans, "event_anchors") {
		assert.Contains(t, s.indexCond, "occurred_time")
		assert.Contains(t, s.indexCond, "event_id")
	}

	// Unchanged: the tenant purge's delete on the one table with no tenant-leading key.
	scans = explainScans(t, sys, nil, `DELETE FROM "event-management".state_change_events WHERE tenant_id = 'acme'`)
	requireServedBy(t, sys, scans, "state_change_events", "uq_state_change_events_idem", "tenant purge")
}

func lenOr[T any](r *T, err error, n func() int) (int, error) {
	if err != nil {
		return 0, err
	}
	return n(), nil
}

// TestIntegrationAnchorFilteredEventsSeekTheKey: the anchor-filtered base-event read must
// still be able to probe each anchored event by key. Under (tenant_id, occurred_time,
// event_id) an event_id alone is not a seekable prefix, so the read asks for the pair, which
// an anchor shares with its event. The COUNT that every page issues has no order, so a key
// probe is the plan that matters: with hash and merge joins off, some events scan must seek
// on BOTH columns. On main the read asks for event_id alone and none does.
func TestIntegrationAnchorFilteredEventsSeekTheKey(t *testing.T) {
	inst := freshInstance(t, "itrekeyanchor")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	const perDevice = 50
	seedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	seedEventStore(t, sys, []string{"acme", "globex"}, 40, perDevice, seedStart, 19*24*time.Hour/perDevice)
	// 50 rare anchors on acme's dev-7, each at its event's own instant.
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".event_anchors
		(tenant_id, event_id, device_token, event_type, occurred_time, anchor_type, anchor_token)
		SELECT tenant_id, event_id, device_token, event_type, occurred_time, 'customer', 'cust-rare'
		FROM "event-management".events WHERE tenant_id = 'acme' AND device_token = 'dev-7'`).Error)
	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`ANALYZE "event-management".`+table).Error)
	}

	counter := rdbtest.NewStatementCounter(`"event-management"`)
	counter.Record(true)
	mgr.Database = mgr.Database.Session(&gorm.Session{Logger: counter})
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	anchorType, anchorToken := "customer", "cust-rare"
	res, err := api.Events(ctx, EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 5},
		AnchorType: &anchorType, AnchorToken: &anchorToken})
	require.NoError(t, err)
	require.Len(t, res.Results, 5)
	assert.EqualValues(t, perDevice, res.Pagination.TotalRecords)
	for i, ev := range res.Results {
		assert.Equal(t, "dev-7", ev.DeviceToken)
		if i > 0 {
			assert.True(t, ev.OccurredTime.Before(res.Results[i-1].OccurredTime), "newest first")
		}
	}
	countSQL := capture(t, counter, "count(*)")
	nestLoop := []string{`SET LOCAL enable_hashjoin = off`, `SET LOCAL enable_mergejoin = off`}
	seeksBoth := func(sql string) bool {
		for _, s := range scansOf(t, sys, explainScans(t, sys, nestLoop, sql), "events") {
			if chunkIndexIs(s.index, "events_pkey") && strings.Contains(s.indexCond, "occurred_time") &&
				strings.Contains(s.indexCond, "event_id") {
				return true
			}
		}
		return false
	}
	assert.True(t, seeksBoth(countSQL), "the anchor-filtered COUNT must be able to probe events_pkey: %s", countSQL)

	// Negative control: the same statement in the old shape cannot seek on the key, and the
	// check says so.
	old := strings.Replace(countSQL, "(occurred_time, event_id) IN (SELECT occurred_time, event_id",
		"event_id IN (SELECT event_id", 1)
	require.NotEqual(t, countSQL, old, "the rewrite must apply: %s", countSQL)
	assert.False(t, seeksBoth(old), "the event_id-only shape must not seek the time-leading key")
}
