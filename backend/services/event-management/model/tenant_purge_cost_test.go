// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// ADR-077 open measurement 1: what does it COST to delete one tenant's telemetry?
//
// The decision this settles. ADR-077 puts tenant erasure on the GA cut line and keeps
// the shared-hypertable topology, on the strength of one property: compression segments
// by tenant_id (see enableCompressionStmt), so a tenant's rows are grouped into their own
// compressed batches and `DELETE ... WHERE tenant_id` should be the CHEAP case for
// compressed DML rather than the worst. That is a reading of a setting, not a fact. If it
// is wrong, the ADR's rejected schema-per-tenant alternative reopens — and it must reopen
// on this evidence rather than on preference, which is why this file exists at all.
//
// It runs against a REAL operand image (PG 17.10 / TimescaleDB 2.28.3), through the REAL
// migration chain and the REAL lifecycle policies, because every question here is about
// the storage engine: sqlite has no chunks and no columnar compression, and
// hack/migration-diff.sh compares schema only and so holds no rows.
//
//	docker rm -f dc-purgebench 2>/dev/null
//	. deploy/images/timescaledb/standalone.sh
//	PORT=$(dc_operand_start ghcr.io/devicechain-io/postgresql-timescaledb:17.10-ts2.28.3-r1 \
//	         dc-purgebench "127.0.0.1::5432")
//	cd backend/services/event-management
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run PurgeCost -v
//
// Scale knobs: DC_PURGE_TENANTS (default 5), DC_PURGE_ROWS per tenant (default 200000),
// so the default run writes a million measurement rows across five tenants.
package model

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The scenario's shape. Rows are spread over purgeSpanDays ending purgeAgeDays ago, so
// every chunk is older than the 7-day compression window and is legitimately compressible
// — the state a real tenant's history is in when it is purged.
const (
	purgeSpanDays = 14
	purgeAgeDays  = 30
	purgeDevices  = 20
)

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	v := envOr(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	require.NoErrorf(t, err, "%s must be numeric", key)
	return n
}

// walBytes reports the WAL written while fn ran. WAL volume is the half of the cost that
// wall clock hides: a delete that finishes quickly but rewrites every compressed batch
// still lands on every replica and in every base backup.
func walBytes(t *testing.T, db *gorm.DB, fn func()) (time.Duration, int64) {
	t.Helper()
	var before, after string
	require.NoError(t, db.Raw("SELECT pg_current_wal_lsn()::text").Scan(&before).Error)
	start := time.Now()
	fn()
	elapsed := time.Since(start)
	require.NoError(t, db.Raw("SELECT pg_current_wal_lsn()::text").Scan(&after).Error)
	var delta int64
	require.NoError(t, db.Raw("SELECT pg_wal_lsn_diff(?::pg_lsn, ?::pg_lsn)::bigint", after, before).Scan(&delta).Error)
	return elapsed, delta
}

func tenantToken(i int) string { return fmt.Sprintf("purge-tenant-%02d", i) }

// seedMeasurements writes rowsPerTenant rows for each tenant, INTERLEAVED in time so that
// every chunk holds every tenant. That interleaving is the whole point: a seed that gave
// each tenant its own time range would hand the victim its own chunks, and the delete
// would degenerate into a chunk drop — measuring a topology we do not have.
func seedMeasurements(t *testing.T, db *gorm.DB, table string, tenants, rowsPerTenant int) {
	t.Helper()
	start := time.Now().UTC().AddDate(0, 0, -(purgeAgeDays + purgeSpanDays))
	stepSeconds := float64(purgeSpanDays*24*3600) / float64(rowsPerTenant)

	for i := 0; i < tenants; i++ {
		tenant := tenantToken(i)
		err := db.Exec(fmt.Sprintf(`
INSERT INTO "event-management".%q
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, classifier, unit, data_type)
SELECT ?,
       sha256(convert_to(? || ':e:' || g::text, 'UTF8')),
       sha256(convert_to(? || ':p:' || g::text, 'UTF8')),
       'purge-dev-' || (g %% ?)::text,
       1,
       ?::timestamptz + (g * ? * interval '1 second'),
       'temperature',
       20 + (g %% 150) / 10.0,
       NULL, 'C', 'DOUBLE'
FROM generate_series(1, ?) g`, table),
			tenant, tenant, tenant, purgeDevices, start, stepSeconds, rowsPerTenant).Error
		require.NoErrorf(t, err, "seed tenant %s", tenant)
	}
}

// dropAllChunks removes every chunk so the seed below builds them from scratch.
//
// 🔴 The harness's TRUNCATE is not enough, and this cost me two contradictory
// measurements before I noticed. TRUNCATE empties a chunk but LEAVES it, still flagged
// compressed; the next run's INSERTs then land in that chunk's uncompressed portion, and
// `compress_chunk(if_not_compressed => true)` SKIPS it because the flag is already set. So
// a second run against the same server measures a DELETE over UNCOMPRESSED rows while
// every is_compressed check reports green — 117ms/15.3 MiB of WAL against a genuinely
// compressed run's 15ms/0.5 MiB, from the same inputs.
func dropAllChunks(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	require.NoError(t, db.Exec(fmt.Sprintf(
		`SELECT drop_chunks('"event-management".%q', newer_than => '-infinity'::timestamptz)`, table)).Error,
		"drop every existing chunk of %s so the seed starts from nothing", table)
}

func compressAllChunks(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	require.NoError(t, db.Exec(
		fmt.Sprintf(`SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('"event-management".%q') c`, table),
	).Error, "compress every chunk of %s", table)
}

// uncompressedRows counts rows still sitting in the RAW portion of each chunk — the rows
// that compression has not swallowed.
//
// This is the guard that `is_compressed` cannot be: the flag is a property of the chunk,
// while what the measurement needs is a property of the ROWS. A fully compressed chunk
// scans as empty through `ONLY`, because its data now lives in the internal compressed
// table that residualInCompressedStore reads.
func uncompressedRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var chunks []string
	require.NoError(t, db.Raw(`
		SELECT format('%I.%I', c.schema_name, c.table_name)
		FROM _timescaledb_catalog.chunk c
		JOIN _timescaledb_catalog.hypertable h ON c.hypertable_id = h.id
		WHERE h.table_name = ? AND h.schema_name = 'event-management'`, table).Scan(&chunks).Error)
	require.NotEmpty(t, chunks, "no chunks found — this guard would be vacuous")

	var total int64
	for _, chunk := range chunks {
		var n int64
		require.NoError(t, db.Raw(fmt.Sprintf(`SELECT count(*) FROM ONLY %s`, chunk)).Scan(&n).Error)
		total += n
	}
	return total
}

func chunkStats(t *testing.T, db *gorm.DB, table string) (total, compressed int) {
	t.Helper()
	require.NoError(t, db.Raw(`SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = ?`, table).Scan(&total).Error)
	require.NoError(t, db.Raw(`SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = ? AND is_compressed`, table).Scan(&compressed).Error)
	return total, compressed
}

func countFor(t *testing.T, db *gorm.DB, tenant string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM "event-management".measurement_events WHERE tenant_id = ?`, tenant).Scan(&n).Error)
	return n
}

// residualInCompressedStore counts the victim's rows still sitting in the INTERNAL
// compressed chunks backing the hypertable.
//
// 🔴 This is the difference between a delete and an ERASURE, and an ordinary SELECT
// cannot tell them apart. Compressed rows live in a separate `_timescaledb_internal`
// table; the segmentby column (tenant_id) is stored there as a plain column. If
// TimescaleDB satisfied the DELETE by recording a filter rather than removing the
// batches, `count(*) WHERE tenant_id = victim` through the hypertable would read zero
// while the tenant's data was still on disk — which is exactly the claim ADR-077 must
// not make wrongly. So look in the store itself.
func residualInCompressedStore(t *testing.T, db *gorm.DB, table, tenant string) int64 {
	t.Helper()
	var chunks []string
	require.NoError(t, db.Raw(`
		SELECT format('%I.%I', cc.schema_name, cc.table_name)
		FROM _timescaledb_catalog.chunk c
		JOIN _timescaledb_catalog.chunk cc ON c.compressed_chunk_id = cc.id
		JOIN _timescaledb_catalog.hypertable h ON c.hypertable_id = h.id
		WHERE h.table_name = ? AND h.schema_name = 'event-management'`, table).Scan(&chunks).Error)
	require.NotEmpty(t, chunks, "found no compressed chunks to inspect — this check would be vacuous")

	var total int64
	for _, chunk := range chunks {
		var n int64
		require.NoError(t, db.Raw(
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = ?`, chunk), tenant).Scan(&n).Error)
		total += n
	}
	return total
}

func tableBytes(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(fmt.Sprintf(
		`SELECT hypertable_size('"event-management".%q')::bigint`, table)).Scan(&n).Error)
	return n
}

// TestIntegrationTenantPurgeCostOnCompressedChunks is the measurement itself.
func TestIntegrationTenantPurgeCostOnCompressedChunks(t *testing.T) {
	tenants := envInt(t, "DC_PURGE_TENANTS", 5)
	rowsPerTenant := envInt(t, "DC_PURGE_ROWS", 200000)
	require.GreaterOrEqual(t, tenants, 2, "a single-tenant run cannot show what the delete leaves behind")

	api := newPostgresApi(t, "itpurgecost")
	mgr := api.RDB
	ctx := core.WithSystemContext(context.Background())
	db := mgr.DB(ctx)

	// The REAL policies, at the shipped defaults (24h chunks, compress after 7 days,
	// retention off) — not a hand-rolled ALTER, so segmentby/orderby are whatever the
	// platform actually deploys.
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, DataLifecyclePolicy{ChunkIntervalHours: 24, CompressAfterDays: 7}),
		"apply the real ADR-026 lifecycle policies")

	dropAllChunks(t, db, "measurement_events")
	seedStart := time.Now()
	seedMeasurements(t, db, "measurement_events", tenants, rowsPerTenant)
	t.Logf("seeded %d tenants x %d rows in %s", tenants, rowsPerTenant, time.Since(seedStart).Round(time.Millisecond))

	compressStart := time.Now()
	compressAllChunks(t, db, "measurement_events")
	total, compressed := chunkStats(t, db, "measurement_events")
	t.Logf("compressed %d/%d chunks in %s", compressed, total, time.Since(compressStart).Round(time.Millisecond))
	require.Equal(t, total, compressed, "every chunk must be flagged compressed")
	require.Greater(t, total, 1, "the seed must span multiple chunks")
	// The guard that matters: no rows left in the raw portion of any chunk. is_compressed
	// is a flag on the chunk; this is a fact about the data.
	require.Zero(t, uncompressedRows(t, db, "measurement_events"),
		"rows remain UNCOMPRESSED in chunks flagged compressed — the delete below would measure uncompressed DML")

	victim := tenantToken(0)
	survivor := tenantToken(1)
	victimBefore := countFor(t, db, victim)
	survivorBefore := countFor(t, db, survivor)
	sizeBefore := tableBytes(t, db, "measurement_events")
	require.EqualValues(t, rowsPerTenant, victimBefore, "seed did not write what it claimed")

	// ── the measurement ──
	var deleted int64
	elapsed, wal := walBytes(t, db, func() {
		res := db.Exec(`DELETE FROM "event-management".measurement_events WHERE tenant_id = ?`, victim)
		require.NoError(t, res.Error, "DELETE on compressed chunks")
		deleted = res.RowsAffected
	})

	// 🔴 THE VACUITY GUARD. A delete that removed nothing would post a magnificent time,
	// and "DELETE ... WHERE tenant_id" against a compressed hypertable is exactly the
	// statement that might silently affect zero rows. Assert the victim is gone AND that
	// a bystander is untouched before believing any number above.
	require.Zero(t, countFor(t, db, victim), "the victim's rows must be gone")
	require.EqualValues(t, survivorBefore, countFor(t, db, survivor),
		"a bystander tenant lost rows — the delete is not tenant-scoped")

	// And the stronger claim: gone from the compressed store, not merely filtered out of
	// reads. An erasure that only hides rows is not an erasure.
	residual := residualInCompressedStore(t, db, "measurement_events", victim)
	survivorResidual := residualInCompressedStore(t, db, "measurement_events", survivor)
	require.Zero(t, residual,
		"the victim's rows are still physically present in the compressed chunks — the DELETE hid them rather than removing them")
	require.NotZero(t, survivorResidual,
		"no bystander rows found in the compressed store either, so the residual probe proves nothing")

	totalAfter, compressedAfter := chunkStats(t, db, "measurement_events")
	sizeAfter := tableBytes(t, db, "measurement_events")

	t.Logf("")
	t.Logf("── ADR-077 measurement 1: DELETE WHERE tenant_id on COMPRESSED chunks ──")
	t.Logf("  scale            %d tenants x %d rows = %d total, %d chunks",
		tenants, rowsPerTenant, int64(tenants)*int64(rowsPerTenant), total)
	t.Logf("  rows deleted     %d (reported %d)", victimBefore, deleted)
	t.Logf("  WALL CLOCK       %s", elapsed.Round(time.Millisecond))
	t.Logf("  per 1M rows      %s", (elapsed * time.Duration(1_000_000/max64(victimBefore, 1))).Round(time.Millisecond))
	t.Logf("  WAL WRITTEN      %.1f MiB (%.2f KiB per 1k rows)",
		float64(wal)/(1<<20), float64(wal)/1024/(float64(victimBefore)/1000))
	t.Logf("  chunks after     %d total, %d still compressed", totalAfter, compressedAfter)
	t.Logf("  hypertable size  %.1f MiB -> %.1f MiB", float64(sizeBefore)/(1<<20), float64(sizeAfter)/(1<<20))
	if compressedAfter < compressed {
		t.Logf("  🔴 %d chunk(s) were DECOMPRESSED by the delete and are not automatically recompressed —",
			compressed-compressedAfter)
		t.Logf("     a purge would leave the tenant's neighbours on uncompressed storage until the next policy run.")
	}
	t.Logf("")
}

// TestIntegrationTenantPurgeCostDropSchemaComparison is the other arm: what the rejected
// schema-per-tenant topology would have cost for the same rows.
//
// The comparison is deliberately generous to the rejected option — one tenant alone in its
// own hypertable, dropped whole — because the point is not to win the argument but to know
// the size of the gap the ADR is trading away.
//
// 🔴 It drops a DISPOSABLE PROBE table, never the real one. The first version dropped
// `measurement_events` itself, which worked once and then failed on every subsequent run
// against the same server: the migration chain was already recorded as applied, so nothing
// recreated the table, and the harness's own pre-run TRUNCATE hit a table that no longer
// existed. A measurement that can only be taken once is not a measurement.
func TestIntegrationTenantPurgeCostDropSchemaComparison(t *testing.T) {
	rowsPerTenant := envInt(t, "DC_PURGE_ROWS", 200000)
	const probe = "purge_drop_probe"

	api := newPostgresApi(t, "itpurgedrop")
	mgr := api.RDB
	ctx := core.WithSystemContext(context.Background())
	db := mgr.DB(ctx)

	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, DataLifecyclePolicy{ChunkIntervalHours: 24, CompressAfterDays: 7}))

	// Same shape as the real table (INCLUDING ALL carries the indexes), made a hypertable
	// and compressed with the same segmentby the platform deploys — so the only difference
	// from the arm above is that this tenant is alone in it.
	require.NoError(t, db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS "event-management".%q CASCADE`, probe)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(
		`CREATE TABLE "event-management".%q (LIKE "event-management".measurement_events INCLUDING ALL)`, probe)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(
		`SELECT create_hypertable('"event-management".%q', 'occurred_time', chunk_time_interval => interval '24 hours')`, probe)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(
		`ALTER TABLE "event-management".%q SET (timescaledb.compress, `+
			`timescaledb.compress_segmentby = 'tenant_id', timescaledb.compress_orderby = 'occurred_time DESC')`, probe)).Error)

	seedMeasurements(t, db, probe, 1, rowsPerTenant)
	compressAllChunks(t, db, probe)
	total, compressed := chunkStats(t, db, probe)
	require.Equal(t, total, compressed)
	require.Greater(t, total, 1, "the probe must span multiple chunks")
	require.Zero(t, uncompressedRows(t, db, probe), "the probe's rows must actually be compressed")

	elapsed, wal := walBytes(t, db, func() {
		require.NoError(t, db.Exec(fmt.Sprintf(`DROP TABLE "event-management".%q CASCADE`, probe)).Error)
	})

	// The guard here is that the table is actually gone — a DROP that silently no-oped
	// would post an even better time than one that worked.
	var stillThere int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'event-management' AND table_name = ?`, probe).Scan(&stillThere).Error)
	require.Zero(t, stillThere, "the probe table must be gone")

	t.Logf("")
	t.Logf("── comparison: DROP of a hypertable holding ONE tenant's %d rows (%d chunks) ──", rowsPerTenant, total)
	t.Logf("  WALL CLOCK       %s", elapsed.Round(time.Millisecond))
	t.Logf("  WAL WRITTEN      %.1f MiB", float64(wal)/(1<<20))
	t.Logf("")
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
