// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the device-scoped alternate-id key (NewDeviceAltIdKeySchema), against
// a real TimescaleDB. Run as postgres_integration_test.go describes.
package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	oldAltIdIndexDef = `CREATE UNIQUE INDEX idx_events_tenant_alt_id ON "event-management".events USING btree (tenant_id, alt_id, occurred_time) WHERE (alt_id IS NOT NULL)`
	newAltIdIndexDef = `CREATE UNIQUE INDEX idx_events_tenant_device_alt_id ON "event-management".events USING btree (tenant_id, device_token, alt_id, occurred_time) WHERE (alt_id IS NOT NULL)`
)

func insertAltEvent(db *gorm.DB, tenant, device, alt string, id byte, at time.Time) error {
	return db.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source, alt_id, processed_time)
		VALUES (?, ?, ?, 1, ?, 'it', ?, ?)`, tenant, []byte{id}, device, at, alt, at).Error
}

// beforeAltKey is a fresh database at the schema the per-device index migration leaves, which
// the device-scoped alternate-id key follows.
func beforeAltKey(t *testing.T, prefix string) (string, *rdb.RdbManager, *gorm.DB) {
	t.Helper()
	inst := freshInstance(t, prefix)
	mgr := newPostgresManagerWith(t, inst, migrationsThrough(t, NewTenantDeviceIndexSchema().ID))
	return inst, mgr, systemDB(mgr)
}

// invalidateAltKey marks the new index INVALID, the state an interrupted per-chunk build leaves.
func invalidateAltKey(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`UPDATE pg_index SET indisvalid = false
		WHERE indexrelid = '"event-management".idx_events_tenant_device_alt_id'::regclass`).Error)
}

// The migration replaces the tenant-wide key with the device-scoped one, keeps every stored
// row, and is a no-op when run again. Before it, two devices of one tenant cannot share an
// alternate id at one instant; after it they can, while one device still cannot repeat it.
func TestIntegrationDeviceAltIdKeyReplacesTheTenantWideKey(t *testing.T) {
	_, _, sys := beforeAltKey(t, "italtid")
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	require.NoError(t, insertAltEvent(sys, "acme", "dev-a", "m1", 1, at))
	require.Error(t, insertAltEvent(sys, "acme", "dev-b", "m1", 2, at), "tenant-wide key: the second device collides")
	assert.Equal(t, oldAltIdIndexDef, hypertableIndexes(t, sys, "events")["idx_events_tenant_alt_id"])

	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys))
	idx := hypertableIndexes(t, sys, "events")
	assert.Equal(t, newAltIdIndexDef, idx["idx_events_tenant_device_alt_id"])
	assert.NotContains(t, idx, "idx_events_tenant_alt_id")

	var n int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&n).Error)
	assert.EqualValues(t, 1, n, "stored rows survive")
	require.NoError(t, insertAltEvent(sys, "acme", "dev-b", "m1", 2, at), "another device may reuse the alternate id")
	require.Error(t, insertAltEvent(sys, "acme", "dev-a", "m1", 3, at), "one device may not repeat it at one instant")

	oid := deviceIndexOID(t, sys, "idx_events_tenant_device_alt_id")
	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys), "re-run")
	assert.Equal(t, oid, deviceIndexOID(t, sys, "idx_events_tenant_device_alt_id"), "a re-run rebuilds nothing")
}

// A gate refusal changes nothing and takes no lock: with a writer holding the table (which a
// build would queue behind), a row or chunk bound the store is over ends the migration in well
// under a second, names the hand-run statements, and leaves the old key and no new one. A
// start exactly at both bounds builds. A migration that skipped its gates would try the build
// and wait on the writer instead.
func TestIntegrationDeviceAltIdKeyGatesRefuseBeforeLocking(t *testing.T) {
	inst, _, sys := beforeAltKey(t, "italtidgate")
	twoChunkSeed(t, sys)
	release := holdWriter(t, inst)

	timing := testRekeyTiming
	timing.maxRows = 3
	start := time.Now()
	err := newDeviceAltIdKeySchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refusal takes no lock")
	for _, want := range []string{"more than 3 rows", deviceAltIdKeyManualDrop, deviceAltIdKeyManualBuild} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "dcctl destroy")
	assert.True(t, hasIndex(t, sys, deviceAltIdKey.replaces))
	assert.False(t, hasIndex(t, sys, deviceAltIdKey.name))

	timing = testRekeyTiming
	timing.maxChunks = 1
	start = time.Now()
	err = newDeviceAltIdKeySchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "a refusal takes no lock")
	for _, want := range []string{"events (2) has", "more than 1 chunks", deviceAltIdKeyManualBuild} {
		assert.Contains(t, err.Error(), want)
	}
	assert.True(t, hasIndex(t, sys, deviceAltIdKey.replaces))
	assert.False(t, hasIndex(t, sys, deviceAltIdKey.name))
	release()

	timing = testRekeyTiming
	timing.maxRows, timing.maxChunks = 4, 2
	require.NoError(t, newDeviceAltIdKeySchema(timing).Migrate(sys), "exactly at both bounds builds")
	assertFinalIndexes(t, sys, 2)
}

// An INVALID index of the new name, which an interrupted per-chunk build leaves, is dropped and
// rebuilt. When a gate refuses first the leftover is still there and the refusal hands over the
// DROP; the manual statements, run verbatim, let the next start finish.
func TestIntegrationDeviceAltIdKeyRebuildsAnInvalidLeftover(t *testing.T) {
	_, _, sys := beforeAltKey(t, "italtidleft")
	twoChunkSeed(t, sys)
	require.NoError(t, sys.Exec(deviceAltIdKeyManualBuild).Error, "the hand-run build is accepted for a unique index")
	invalidateAltKey(t, sys)
	leftover := deviceIndexOID(t, sys, deviceAltIdKey.name)

	timing := testRekeyTiming
	timing.maxRows = 0
	err := newDeviceAltIdKeySchema(timing).Migrate(sys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), deviceAltIdKeyManualDrop)
	assert.Equal(t, leftover, deviceIndexOID(t, sys, deviceAltIdKey.name), "a refusal leaves the leftover")
	assert.True(t, hasIndex(t, sys, deviceAltIdKey.replaces), "a refusal leaves the old key")
	assert.Error(t, sys.Exec(deviceAltIdKeyManualBuild).Error, "the build statement fails loudly over a leftover")

	require.NoError(t, sys.Exec(deviceAltIdKeyManualDrop).Error)
	require.NoError(t, sys.Exec(deviceAltIdKeyManualBuild).Error)
	require.NoError(t, newDeviceAltIdKeySchema(timing).Migrate(sys), "a valid hand-built index needs no gate")
	assertFinalIndexes(t, sys, 2)

	_, _, sys2 := beforeAltKey(t, "italtidleft2")
	twoChunkSeed(t, sys2)
	require.NoError(t, sys2.Exec(deviceAltIdKeyManualBuild).Error)
	invalidateAltKey(t, sys2)
	leftover = deviceIndexOID(t, sys2, deviceAltIdKey.name)
	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys2))
	assert.NotEqual(t, leftover, deviceIndexOID(t, sys2, deviceAltIdKey.name), "the leftover was rebuilt")
	assertFinalIndexes(t, sys2, 2)
}

// The new key valid and the old still present (built by hand before upgrading) is a drop-only
// start: it ignores both bounds, rebuilds nothing, and removes the old key.
func TestIntegrationDeviceAltIdKeyDropsTheOldKeyWhenTheNewOneIsBuilt(t *testing.T) {
	_, _, sys := beforeAltKey(t, "italtiddrop")
	twoChunkSeed(t, sys)
	require.NoError(t, sys.Exec(deviceAltIdKeyManualBuild).Error)
	built := deviceIndexOID(t, sys, deviceAltIdKey.name)
	require.True(t, hasIndex(t, sys, deviceAltIdKey.replaces), "premise: the old key is still there")

	timing := testRekeyTiming
	timing.maxRows, timing.maxChunks = 0, 1
	require.NoError(t, newDeviceAltIdKeySchema(timing).Migrate(sys), "drop-only: no gate applies")
	assert.Equal(t, built, deviceIndexOID(t, sys, deviceAltIdKey.name), "the new key is not rebuilt")
	assert.False(t, hasIndex(t, sys, deviceAltIdKey.replaces))
	assertFinalIndexes(t, sys, 2)
}

// The build over compressed chunks, the shape every long-lived instance has: older chunks
// compressed before the migration runs. The key lands on the table and on every chunk, stored
// rows survive, and another device may then reuse an alternate id.
func TestIntegrationDeviceAltIdKeyBuildsOverCompressedChunks(t *testing.T) {
	_, _, sys := beforeAltKey(t, "italtidzip")
	twoChunkSeed(t, sys)
	oldChunk := compressOldest(t, sys)
	var before int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&before).Error)

	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys))
	assertFinalIndexes(t, sys, 2)
	var after int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&after).Error)
	assert.Equal(t, before, after, "stored rows survive")

	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, insertAltEvent(sys, "acme", "dev-a", "m1", 0x51, at))
	require.NoError(t, insertAltEvent(sys, "acme", "dev-b", "m1", 0x52, at))
	require.Error(t, insertAltEvent(sys, "acme", "dev-a", "m1", 0x53, at))

	for _, table := range LifecycleHypertables {
		require.NoError(t, sys.Exec(`SELECT decompress_chunk(?::regclass)`, oldChunk[table]).Error)
	}
	assertFinalIndexes(t, sys, 2)
}

// An index of the new name that is not the unique, partial index this migration builds is
// refused before any DDL, and left alone, and the old key stays. A non-unique index of the
// right columns is the case that matters: accepted as built, it would let the old unique key
// go and leave no dedup guard.
func TestIntegrationDeviceAltIdKeyRefusesAShapeItDidNotWrite(t *testing.T) {
	_, _, sys := beforeAltKey(t, "italtidshape")
	twoChunkSeed(t, sys)
	for _, ddl := range []string{
		`CREATE INDEX idx_events_tenant_device_alt_id ON "event-management".events (tenant_id, device_token, alt_id, occurred_time) WHERE alt_id IS NOT NULL`,
		`CREATE UNIQUE INDEX idx_events_tenant_device_alt_id ON "event-management".events (tenant_id, device_token, alt_id, occurred_time)`,
		`CREATE UNIQUE INDEX idx_events_tenant_device_alt_id ON "event-management".events (tenant_id, alt_id, occurred_time) WHERE alt_id IS NOT NULL`,
	} {
		require.NoError(t, sys.Exec(ddl).Error)
		foreign := hypertableIndexes(t, sys, "events")[deviceAltIdKey.name]
		err := NewDeviceAltIdKeySchema().Migrate(sys)
		require.Error(t, err, ddl)
		assert.Contains(t, err.Error(), "does not replace an index it did not write")
		assert.True(t, hasIndex(t, sys, deviceAltIdKey.replaces), "the old key stays: %s", ddl)
		assert.Equal(t, foreign, hypertableIndexes(t, sys, "events")[deviceAltIdKey.name], "the foreign index is untouched")
		require.NoError(t, sys.Exec(`DROP INDEX "event-management".idx_events_tenant_device_alt_id`).Error)
	}
}

// A re-run on the migration's own output takes no lock and rebuilds nothing.
func TestIntegrationDeviceAltIdKeyRerunTakesNoLock(t *testing.T) {
	inst := freshInstance(t, "italtidrerun")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)
	twoChunkSeed(t, sys)
	built := deviceIndexOID(t, sys, deviceAltIdKey.name)

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
	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys.WithContext(ctx)))
	assert.Less(t, time.Since(start), time.Second, "a re-run takes no lock")
	release()
	assert.Equal(t, built, deviceIndexOID(t, sys, deviceAltIdKey.name), "a re-run rebuilds nothing")
	assertFinalIndexes(t, sys, 2)
}
