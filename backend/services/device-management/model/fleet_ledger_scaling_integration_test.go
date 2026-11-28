// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// The background work and admission checks of bulk changes must cost what they are asked about,
// not what the instance has retained. Each test below measures, with EXPLAIN (ANALYZE, BUFFERS) on
// the very statements issued, how many fleet_operation_lines rows and index entries a pass or a
// check reads, against a retained ledger several times larger than its answer. Every test runs on
// an instance database of its own (driverPGManager), so the plans see only the fixture.

// linesRead captures the statements call issues on a session of api's database and sums the
// fleet_operation_lines rows their plans read.
func linesRead(t *testing.T, api *Api, call func(*Api) error) (int64, []string) {
	t.Helper()
	session, capture := capturing(api.RDB.Database)
	require.NoError(t, call(NewApi(&rdb.RdbManager{Database: session})))
	var total int64
	var touched []string
	for _, statement := range capture.statements {
		if !strings.Contains(statement, "fleet_operation_lines") || !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(statement)), "SELECT") {
			continue
		}
		total += measure(t, api.RDB.Database, statement).LineRows
		touched = append(touched, statement)
	}
	return total, touched
}

func vacuumLedger(t *testing.T, f *costFixture) {
	t.Helper()
	for _, table := range []string{"fleet_operations", "fleet_operation_lines", "fleet_operation_runtime"} {
		require.NoError(t, f.db.Exec(`VACUUM (ANALYZE) "device-management"."`+table+`"`).Error)
	}
}

// One erasure repair pass reads a window of the tenant's lines, not all of them.
func TestFleetErasureRepairPassReadsOneWindowOnPostgres(t *testing.T) {
	api := NewApi(driverPGManager(t))
	f := newCostFixture(t, api)
	f.addOperations(t, f.active, "noise", 10, 1)
	// Every target is a live device: a pass with nothing to repair, the case that read everything.
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management".devices (id, tenant_id, token, created_at, updated_at)
 SELECT DISTINCT target_id, tenant_id, 'bcs-' || target_id, now(), now() FROM "device-management".fleet_operation_lines WHERE tenant_id = ?`, f.active).Error)
	t.Cleanup(func() {
		require.NoError(t, f.db.Exec(`DELETE FROM "device-management".devices WHERE tenant_id LIKE ?`, f.prefix+"%").Error)
	})
	vacuumLedger(t, f)
	require.NoError(t, f.db.Exec(`VACUUM (ANALYZE) "device-management".devices`).Error)
	prev := fleetErasureRepairWindow
	fleetErasureRepairWindow = 1000
	t.Cleanup(func() { fleetErasureRepairWindow = prev })

	tenant := core.WithTenant(context.Background(), f.active)
	const total = 10 * costLinesPerOp
	read, statements := linesRead(t, api, func(a *Api) error { return a.RepairFleetErasure(tenant) })
	t.Logf("erasure repair pass: %d line rows read over %d statements; tenant holds %d lines, window %d", read, len(statements), total, fleetErasureRepairWindow)
	require.Len(t, statements, 2, "the window's bound and its candidates")
	require.LessOrEqual(t, read, int64(3*fleetErasureRepairWindow), "a pass reads its window, not the tenant's %d lines", total)

	// And the walk covers every line: an orphan anywhere is repaired within one cycle of passes.
	var last FleetOperationLine
	require.NoError(t, api.RDB.DB(tenant).Order("target_id DESC").First(&last).Error)
	require.NoError(t, f.db.Exec(`DELETE FROM "device-management".devices WHERE id = ?`, last.TargetId.Int64).Error)
	for i := 0; i < total/fleetErasureRepairWindow+1; i++ {
		require.NoError(t, api.RepairFleetErasure(tenant))
	}
	require.NoError(t, api.RDB.DB(tenant).First(&last, last.ID).Error)
	require.True(t, last.RedactedAt.Valid, "the last device's orphan is repaired within one cycle")
}
