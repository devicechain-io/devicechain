// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the order-complete per-device index (NewDeviceOrderIndexSchema), against
// a real TimescaleDB. Run as postgres_integration_test.go describes.
package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const oldDeviceTimeIndexDef = `CREATE INDEX idx_events_tenant_device_time ON "event-management".events USING btree (tenant_id, device_token, occurred_time DESC)`

// beforeDeviceOrder is a fresh database at the schema the chain leaves just before the
// device-order index: every earlier migration applied, this one not.
func beforeDeviceOrder(t *testing.T, prefix string) (string, *rdb.RdbManager, *gorm.DB) {
	t.Helper()
	inst := freshInstance(t, prefix)
	mgr := newPostgresManagerWith(t, inst, migrationsBefore(t, NewDeviceOrderIndexSchema().ID))
	return inst, mgr, systemDB(mgr)
}

// invalidateDeviceOrder marks the new index INVALID, the state an interrupted per-chunk build leaves.
func invalidateDeviceOrder(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`UPDATE pg_index SET indisvalid = false
		WHERE indexrelid = '"event-management".idx_events_tenant_device_order'::regclass`).Error)
}

// The migration replaces the three-column device index with the one carrying event_id, on the
// table and on every chunk, keeps every stored row, and is a no-op when run again.
func TestIntegrationDeviceOrderIndexReplacesThePerDeviceIndex(t *testing.T) {
	_, _, sys := beforeDeviceOrder(t, "itdevord")
	twoChunkSeed(t, sys)
	assert.Equal(t, oldDeviceTimeIndexDef, hypertableIndexes(t, sys, "events")["idx_events_tenant_device_time"])
	var before int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&before).Error)

	require.NoError(t, NewDeviceOrderIndexSchema().Migrate(sys))
	idx := hypertableIndexes(t, sys, "events")
	assert.Equal(t, deviceOrderIndex.def, idx[deviceOrderIndex.name])
	assert.NotContains(t, idx, deviceOrderIndex.replaces)
	assertFinalIndexes(t, sys, 2)

	var after int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&after).Error)
	assert.Equal(t, before, after, "stored rows survive")

	oid := deviceIndexOID(t, sys, deviceOrderIndex.name)
	require.NoError(t, NewDeviceOrderIndexSchema().Migrate(sys), "re-run")
	assert.Equal(t, oid, deviceIndexOID(t, sys, deviceOrderIndex.name), "a re-run rebuilds nothing")
}

// A gate refusal changes nothing and takes no lock: with a writer holding the table (which a
// build would queue behind), a row or chunk bound the store is over ends the migration in well
// under a second, names the hand-run statements, and leaves the old index and no new one. A
// start exactly at both bounds builds. A migration that skipped its gates would try the build
// and wait on the writer instead.
func TestIntegrationDeviceOrderIndexGatesRefuseBeforeLocking(t *testing.T) {
	inst, _, sys := beforeDeviceOrder(t, "itdevordgate")
	twoChunkSeed(t, sys)
	release := holdWriter(t, inst)

	timing := testRekeyTiming
	timing.maxRows = 3
	start := time.Now()
	err := newDeviceOrderIndexSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refusal takes no lock")
	for _, want := range []string{"more than 3 rows", deviceOrderIndexManualDrop, deviceOrderIndexManualBuild} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "dcctl destroy")
	assert.True(t, hasIndex(t, sys, deviceOrderIndex.replaces))
	assert.False(t, hasIndex(t, sys, deviceOrderIndex.name))

	timing = testRekeyTiming
	timing.maxChunks = 1
	start = time.Now()
	err = newDeviceOrderIndexSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refusal takes no lock")
	for _, want := range []string{"events (2) has", "more than 1 chunks", deviceOrderIndexManualBuild} {
		assert.Contains(t, err.Error(), want)
	}
	assert.True(t, hasIndex(t, sys, deviceOrderIndex.replaces))
	assert.False(t, hasIndex(t, sys, deviceOrderIndex.name))
	release()

	timing = testRekeyTiming
	timing.maxRows, timing.maxChunks = 4, 2
	require.NoError(t, newDeviceOrderIndexSchema(timing).Migrate(sys), "exactly at both bounds builds")
	assertFinalIndexes(t, sys, 2)
}

// An INVALID index of the new name, which an interrupted per-chunk build leaves, is dropped and
// rebuilt. When a gate refuses first the leftover is still there and the refusal hands over the
// DROP; the manual statements, run verbatim, let the next start finish.
func TestIntegrationDeviceOrderIndexRebuildsAnInvalidLeftover(t *testing.T) {
	_, _, sys := beforeDeviceOrder(t, "itdevordleft")
	twoChunkSeed(t, sys)
	require.NoError(t, sys.Exec(deviceOrderIndexManualBuild).Error, "the hand-run build is accepted")
	invalidateDeviceOrder(t, sys)
	leftover := deviceIndexOID(t, sys, deviceOrderIndex.name)

	timing := testRekeyTiming
	timing.maxRows = 0
	err := newDeviceOrderIndexSchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), deviceOrderIndexManualDrop)
	assert.Equal(t, leftover, deviceIndexOID(t, sys, deviceOrderIndex.name), "a refusal leaves the leftover")
	assert.True(t, hasIndex(t, sys, deviceOrderIndex.replaces), "a refusal leaves the old index")
	assert.Error(t, sys.Exec(deviceOrderIndexManualBuild).Error, "the build statement fails loudly over a leftover")

	require.NoError(t, sys.Exec(deviceOrderIndexManualDrop).Error)
	require.NoError(t, sys.Exec(deviceOrderIndexManualBuild).Error)
	require.NoError(t, newDeviceOrderIndexSchema(timing).Migrate(sys), "a valid hand-built index needs no gate")
	assertFinalIndexes(t, sys, 2)

	_, _, sys2 := beforeDeviceOrder(t, "itdevordleft2")
	twoChunkSeed(t, sys2)
	require.NoError(t, sys2.Exec(deviceOrderIndexManualBuild).Error)
	invalidateDeviceOrder(t, sys2)
	leftover = deviceIndexOID(t, sys2, deviceOrderIndex.name)
	require.NoError(t, NewDeviceOrderIndexSchema().Migrate(sys2))
	assert.NotEqual(t, leftover, deviceIndexOID(t, sys2, deviceOrderIndex.name), "the leftover was rebuilt")
	assertFinalIndexes(t, sys2, 2)
}

// The new index valid and the old still present (built by hand before upgrading) is a
// drop-only start: it ignores both bounds, rebuilds nothing, and removes the old index.
func TestIntegrationDeviceOrderIndexDropsTheOldIndexWhenTheNewOneIsBuilt(t *testing.T) {
	_, _, sys := beforeDeviceOrder(t, "itdevorddrop")
	twoChunkSeed(t, sys)
	require.NoError(t, sys.Exec(deviceOrderIndexManualBuild).Error)
	built := deviceIndexOID(t, sys, deviceOrderIndex.name)
	require.True(t, hasIndex(t, sys, deviceOrderIndex.replaces), "premise: the old index is still there")

	timing := testRekeyTiming
	timing.maxRows, timing.maxChunks = 0, 1
	require.NoError(t, newDeviceOrderIndexSchema(timing).Migrate(sys), "drop-only: no gate applies")
	assert.Equal(t, built, deviceIndexOID(t, sys, deviceOrderIndex.name), "the new index is not rebuilt")
	assert.False(t, hasIndex(t, sys, deviceOrderIndex.replaces))
	assertFinalIndexes(t, sys, 2)
}

// The build over compressed chunks, the shape every long-lived instance has: older chunks
// compressed before the migration runs. The index lands on the table and on every chunk, and
// stored rows survive.
func TestIntegrationDeviceOrderIndexBuildsOverCompressedChunks(t *testing.T) {
	_, _, sys := beforeDeviceOrder(t, "itdevordzip")
	twoChunkSeed(t, sys)
	oldChunk := compressOldest(t, sys)
	var before int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&before).Error)

	require.NoError(t, NewDeviceOrderIndexSchema().Migrate(sys))
	assertFinalIndexes(t, sys, 2)
	var after int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&after).Error)
	assert.Equal(t, before, after, "stored rows survive")

	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, oldChunk[table]).Error)
	}
	assertFinalIndexes(t, sys, 2)
}

// An index of the new name that is not the one this migration builds is refused before any DDL,
// and left alone, and the old index stays. A unique index of the right columns, or the right
// columns without the event_id tiebreak, accepted as built would let the old index go and leave
// the page without its order.
func TestIntegrationDeviceOrderIndexRefusesAShapeItDidNotWrite(t *testing.T) {
	_, _, sys := beforeDeviceOrder(t, "itdevordshape")
	twoChunkSeed(t, sys)
	for _, ddl := range []string{
		`CREATE UNIQUE INDEX idx_events_tenant_device_order ON "event-management".events (tenant_id, device_token, occurred_time DESC, event_id DESC)`,
		`CREATE INDEX idx_events_tenant_device_order ON "event-management".events (tenant_id, device_token, occurred_time DESC)`,
		`CREATE INDEX idx_events_tenant_device_order ON "event-management".events (tenant_id, device_token, occurred_time, event_id)`,
		`CREATE INDEX idx_events_tenant_device_order ON "event-management".events (tenant_id, device_token, occurred_time DESC, event_id DESC) WHERE event_type = 1`,
	} {
		require.NoError(t, sys.Exec(ddl).Error)
		foreign := hypertableIndexes(t, sys, "events")[deviceOrderIndex.name]
		err := NewDeviceOrderIndexSchema().Migrate(sys)
		require.Error(t, err, ddl)
		assert.Contains(t, err.Error(), "does not replace an index it did not write")
		assert.True(t, hasIndex(t, sys, deviceOrderIndex.replaces), "the old index stays: %s", ddl)
		assert.Equal(t, foreign, hypertableIndexes(t, sys, "events")[deviceOrderIndex.name], "the foreign index is untouched")
		require.NoError(t, sys.Exec(`DROP INDEX "event-management".idx_events_tenant_device_order`).Error)
	}
}

// A re-run on the migration's own output takes no lock and rebuilds nothing.
func TestIntegrationDeviceOrderIndexRerunTakesNoLock(t *testing.T) {
	inst := freshInstance(t, "itdevordrerun")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	built := deviceIndexOID(t, sys, deviceOrderIndex.name)

	holder := connectInstance(t, inst)
	_, err := holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	// Released on every exit, so a failure here cannot leave the lock held for the rest of the run.
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_, rerr := holder.Exec(context.Background(), `ROLLBACK`)
		require.NoError(t, rerr)
	}
	t.Cleanup(release)

	// A migration that queued behind the lock would wait for as long as it is held; the deadline
	// turns that into a failed assertion instead of a hung package.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, NewDeviceOrderIndexSchema().Migrate(sys.WithContext(ctx)))
	assert.Less(t, time.Since(start), time.Second, "a re-run takes no lock")
	release()
	assert.Equal(t, built, deviceIndexOID(t, sys, deviceOrderIndex.name), "a re-run rebuilds nothing")
	assertFinalIndexes(t, sys, 2)
}

// A build that runs out of its whole allowance is rolled back, decided on the first attempt
// (never retried within the start), and leaves a marker on the OLD index; the next start
// refuses at once without locking; the manual statements the message publishes, run verbatim,
// let the next start finish; and clearing the marker lets a start try again.
func TestIntegrationDeviceOrderIndexTooSlowIsRolledBackAndSticks(t *testing.T) {
	inst, _, sys := beforeDeviceOrder(t, "itdevordslow")
	// 200,000 events: indexing them takes far more than 50 ms.
	seedEventStore(t, sys, []string{"acme"}, 200, 1000, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Minute)

	timing := testRekeyTiming
	timing.tableBuild = 50 * time.Millisecond
	timing.pause = 2 * time.Second
	timing.lockAttempt = 2 * time.Second
	timing.maxRows = 10_000_000
	start := time.Now()
	err := newDeviceOrderIndexSchema(timing).Migrate(sys)
	elapsed := time.Since(start)
	require.Error(t, err)
	for _, want := range []string{"did not finish within 50ms", deviceOrderIndexManualDrop, deviceOrderIndexManualBuild,
		`COMMENT ON INDEX "event-management".idx_events_tenant_device_time IS NULL`} {
		assert.Contains(t, err.Error(), want)
	}
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "the server error stays wrapped: %v", err)
	assert.Equal(t, "57014", pgErr.Code)
	assert.Less(t, elapsed, timing.pause, "too slow is decided on the first attempt, never retried")

	assert.False(t, hasIndex(t, sys, deviceOrderIndex.name), "rolled back whole")
	assert.True(t, hasIndex(t, sys, deviceOrderIndex.replaces))
	var note string
	require.NoError(t, sys.Raw(`SELECT COALESCE(obj_description('"event-management".idx_events_tenant_device_time'::regclass, 'pg_class'), '')`).Scan(&note).Error)
	assert.True(t, strings.HasPrefix(note, deviceOrderIndexTooSlowMarker), "the verdict is recorded: %q", note)

	// The next start refuses at once while another session holds events ACCESS EXCLUSIVE.
	holder := connectInstance(t, inst)
	_, err = holder.Exec(context.Background(), `BEGIN; LOCK TABLE "event-management".events IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	start = time.Now()
	err = newDeviceOrderIndexSchema(testRekeyTiming).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refused build is refused without waiting on any lock")
	assert.Contains(t, err.Error(), "an earlier start found it too slow")
	_, err = holder.Exec(context.Background(), `ROLLBACK`)
	require.NoError(t, err)

	// The published remedy, verbatim: the next start then finishes without clearing anything.
	require.NoError(t, sys.Exec(deviceOrderIndexManualDrop).Error)
	require.NoError(t, sys.Exec(deviceOrderIndexManualBuild).Error)
	require.NoError(t, newDeviceOrderIndexSchema(testRekeyTiming).Migrate(sys))
	assertFinalIndexes(t, sys, 1)

	// With the marker and no valid new index, clearing the comment lets a start build.
	_, _, sys2 := beforeDeviceOrder(t, "itdevordslow2")
	twoChunkSeed(t, sys2)
	require.NoError(t, sys2.Exec(`COMMENT ON INDEX "event-management".idx_events_tenant_device_time IS '`+
		deviceOrderIndexTooSlowMarker+`: test'`).Error)
	require.Error(t, NewDeviceOrderIndexSchema().Migrate(sys2))
	require.NoError(t, sys2.Exec(`COMMENT ON INDEX "event-management".idx_events_tenant_device_time IS NULL`).Error)
	require.NoError(t, NewDeviceOrderIndexSchema().Migrate(sys2), "cleared, the build runs again")
	assertFinalIndexes(t, sys2, 2)
}
