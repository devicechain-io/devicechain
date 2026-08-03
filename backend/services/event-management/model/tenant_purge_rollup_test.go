// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// ADR-077 open measurement 2: does deleting a tenant's raw telemetry erase it from the
// ROLLUP, and if not, what do the two remedies cost?
//
// The claim under test is the ADR's, and it is a claim about a hole:
// `measurement_rollups` materializes per-tenant, per-device aggregates into its own
// hypertable, and its refresh policy covers only a trailing 30-day window
// (baselineRollupWindowDays), so a tenant's older buckets should survive a
// `DELETE ... WHERE tenant_id` on the underlying hypertable — indefinitely. Sum, min, max
// and count per metric per device is not anonymous residue; an erasure claim that leaves
// it behind is false.
//
// 🔴 THE TRAP THIS FILE HAD TO AVOID, and the codebase had already paid for it once: the
// view is created with `materialized_only = false`, so reading the VIEW recomputes
// anything above the watermark live and would report the victim's buckets as gone the
// moment its raw rows were deleted — a clean bill of health from the one query that cannot
// see the problem. Every count here reads the MATERIALIZATION HYPERTABLE directly. See the
// note in baseline.go, written after the same distinction bit the ADR-028 drill.
//
// Run it exactly like tenant_purge_cost_test.go (same operand container, same env), with
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run PurgeRollup -v
package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const rollupView = "event-management.measurement_rollups"

// materializationHypertable returns the internal hypertable the continuous aggregate
// materializes into — the only place that can answer "is this tenant's history still
// stored", as opposed to "can the view still produce it".
func materializationHypertable(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var name string
	require.NoError(t, db.Raw(`
		SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)
		FROM timescaledb_information.continuous_aggregates
		WHERE view_schema = 'event-management' AND view_name = 'measurement_rollups'`).Scan(&name).Error)
	require.NotEmpty(t, name, "no continuous aggregate found — the measurement would be vacuous")
	return name
}

func materializedRowsFor(t *testing.T, db *gorm.DB, matTable, tenant string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = ?`, matTable), tenant).Scan(&n).Error)
	return n
}

// TestIntegrationTenantPurgeRollupResidue demonstrates the hole and prices remedy A
// (a full-range refresh of the continuous aggregate).
func TestIntegrationTenantPurgeRollupResidue(t *testing.T) {
	rowsPerTenant := envInt(t, "DC_PURGE_ROWS", 200000)

	api := newPostgresApi(t, "itpurgerollup")
	mgr := api.RDB
	ctx := core.WithSystemContext(context.Background())
	db := mgr.DB(ctx)

	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, 24, 7, 0))
	dropAllChunks(t, db, "measurement_events")
	seedMeasurements(t, db, "measurement_events", 2, rowsPerTenant)

	// Materialize the seeded history. The seed lands 30–44 days back, i.e. OUTSIDE the
	// policy's trailing 30-day window — which is the whole point: this is the history the
	// scheduled refresh will never revisit.
	require.NoError(t, db.Exec(
		fmt.Sprintf(`CALL refresh_continuous_aggregate('%s', NULL, NULL)`, rollupView)).Error,
		"materialize the seeded history")

	mat := materializationHypertable(t, db)
	victim, survivor := tenantToken(0), tenantToken(1)
	victimBuckets := materializedRowsFor(t, db, mat, victim)
	survivorBuckets := materializedRowsFor(t, db, mat, survivor)
	require.NotZero(t, victimBuckets, "nothing was materialized for the victim, so this proves nothing")
	require.NotZero(t, survivorBuckets, "nothing was materialized for the bystander either")

	// The erasure, exactly as slice 2 would perform it on the raw hypertable.
	res := db.Exec(`DELETE FROM "event-management".measurement_events WHERE tenant_id = ?`, victim)
	require.NoError(t, res.Error)
	require.EqualValues(t, rowsPerTenant, res.RowsAffected)
	require.Zero(t, countFor(t, db, victim), "raw rows must be gone before asking about the rollup")

	residue := materializedRowsFor(t, db, mat, victim)

	t.Logf("")
	t.Logf("── ADR-077 measurement 2: does the raw DELETE reach the rollup? ──")
	t.Logf("  materialized buckets for the victim BEFORE   %d", victimBuckets)
	t.Logf("  materialized buckets for the victim AFTER    %d", residue)
	if residue > 0 {
		t.Logf("  🔴 CONFIRMED: %d aggregate rows (sum/min/max/count per device per metric) survive", residue)
		t.Logf("     the raw delete. The rollup is a SEPARATE purge target — the ADR is right.")
	} else {
		t.Logf("  ⚠️  the rollup emptied itself; ADR-077 decision 9 overstates the hole and must be corrected")
	}

	// Remedy A: refresh the aggregate over the tenant's full range. Note what this costs
	// conceptually as well as in milliseconds — it recomputes EVERY tenant's buckets in
	// that range, not just the victim's.
	elapsed, wal := walBytes(t, db, func() {
		require.NoError(t, db.Exec(
			fmt.Sprintf(`CALL refresh_continuous_aggregate('%s', NULL, NULL)`, rollupView)).Error)
	})
	afterRefresh := materializedRowsFor(t, db, mat, victim)
	survivorAfter := materializedRowsFor(t, db, mat, survivor)

	t.Logf("")
	t.Logf("  remedy A — full-range refresh_continuous_aggregate")
	t.Logf("    WALL CLOCK     %s", elapsed.Round(time.Millisecond))
	t.Logf("    WAL WRITTEN    %.1f MiB", float64(wal)/(1<<20))
	t.Logf("    victim buckets %d -> %d", residue, afterRefresh)
	t.Logf("    it recomputes every OTHER tenant's buckets in the same range as a side effect")
	t.Logf("")

	require.Zero(t, afterRefresh, "a full-range refresh must clear the victim's materialized buckets")
	require.NotZero(t, survivorAfter, "the refresh destroyed a bystander's buckets")
}

// TestIntegrationTenantPurgeRollupDirectDelete prices remedy B — deleting the tenant's
// rows straight out of the materialization hypertable.
//
// It is internal-catalog surgery, which is exactly why it needs measuring rather than
// assuming: it is cheap and surgical if it works, and the reason to know the number is to
// judge it against remedy A's collateral recomputation.
func TestIntegrationTenantPurgeRollupDirectDelete(t *testing.T) {
	rowsPerTenant := envInt(t, "DC_PURGE_ROWS", 200000)

	api := newPostgresApi(t, "itpurgerollupdirect")
	mgr := api.RDB
	ctx := core.WithSystemContext(context.Background())
	db := mgr.DB(ctx)

	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, 24, 7, 0))
	dropAllChunks(t, db, "measurement_events")
	seedMeasurements(t, db, "measurement_events", 2, rowsPerTenant)
	require.NoError(t, db.Exec(
		fmt.Sprintf(`CALL refresh_continuous_aggregate('%s', NULL, NULL)`, rollupView)).Error)

	mat := materializationHypertable(t, db)
	victim, survivor := tenantToken(0), tenantToken(1)
	before := materializedRowsFor(t, db, mat, victim)
	survivorBefore := materializedRowsFor(t, db, mat, survivor)
	require.NotZero(t, before)

	require.NoError(t, db.Exec(`DELETE FROM "event-management".measurement_events WHERE tenant_id = ?`, victim).Error)

	var deleted int64
	elapsed, wal := walBytes(t, db, func() {
		res := db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE tenant_id = ?`, mat), victim)
		require.NoError(t, res.Error, "direct delete on the materialization hypertable")
		deleted = res.RowsAffected
	})

	require.Zero(t, materializedRowsFor(t, db, mat, victim), "the victim's buckets must be gone")
	require.EqualValues(t, survivorBefore, materializedRowsFor(t, db, mat, survivor),
		"a bystander's buckets were destroyed — the direct delete is not tenant-scoped")

	t.Logf("")
	t.Logf("  remedy B — DELETE straight from the materialization hypertable")
	t.Logf("    buckets deleted %d (of %d)", deleted, before)
	t.Logf("    WALL CLOCK      %s", elapsed.Round(time.Millisecond))
	t.Logf("    WAL WRITTEN     %.1f MiB", float64(wal)/(1<<20))
	t.Logf("    touches only the victim's rows — no collateral recomputation")
	t.Logf("")
}
