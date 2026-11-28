// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Admission's retained-lines budget is read from the operation headers, and its evidence-open
// budget along the partial index of exactly the lines it counts: neither reads the settled history.
func TestFleetAdmissionCountsDoNotReadTheRetainedLedgerOnPostgres(t *testing.T) {
	api := NewApi(driverPGManager(t))
	f := newCostFixture(t, api)
	// The tenant's AMBIGUOUS backlog (6 x 1480 open lines), one RUNNING change (740 open, 1260
	// planned), and settled history across noise tenants, many times larger than either.
	f.addOperations(t, f.active, "history", 6, 1)
	f.addOperations(t, f.active, "running", 1, 1)
	for i := 0; i < 12; i++ {
		f.addOperations(t, f.prefix+"noise"+string(rune('a'+i)), "noise", 2, 1)
	}
	// Every fixture change went through create step two.
	require.NoError(t, f.db.Exec(`UPDATE "device-management".fleet_operations SET plan_digest = 'sha256:p' WHERE tenant_id LIKE ?`, f.prefix+"%").Error)
	vacuumLedger(t, f)
	var retainedAll, open, planned int64
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM "device-management".fleet_operation_lines`).Scan(&retainedAll).Error)
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM "device-management".fleet_operation_lines WHERE posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS') AND code <> 'TARGET_ERASED'`).Scan(&open).Error)
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM "device-management".fleet_operation_lines WHERE posting = 'PLANNED'`).Scan(&planned).Error)
	require.Greater(t, retainedAll, 3*(open+planned), "the fixture's settled history dominates the ledger")

	tenant := core.WithTenant(context.Background(), f.active)
	// The answers first: exactly the counts of the lines.
	for _, c := range []context.Context{tenant, core.WithSystemContext(tenant)} {
		got, err := fleetRetainedLines(api.RDB.DB(c))
		require.NoError(t, err)
		require.Equal(t, retainedLinesOracleForTest(t, api.RDB.DB(c)), got)
	}

	// Create admission, tenant and instance scopes, on one transaction as create runs it.
	admit, statements := linesRead(t, api, func(a *Api) error {
		return a.RDB.DB(tenant).Transaction(func(tx *gorm.DB) error { return a.fleetAdmit(tenant, tx, 1) })
	})
	t.Logf("create admission: %d line rows read over %d statements; ledger %d lines, %d planned", admit, len(statements), retainedAll, planned)
	// The two planned-line counts (tenant, instance) are all it reads of the lines table.
	require.LessOrEqual(t, admit, 2*planned+64, "admission reads the planned lines, never the retained ledger")

	// Start/resume admission's evidence-open count, tenant and instance.
	var running FleetOperation
	require.NoError(t, api.RDB.DB(tenant).Where("dispatch = 'RUNNING'").First(&running).Error)
	var tenantOpen int64
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM "device-management".fleet_operation_lines WHERE tenant_id = ? AND posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS') AND code <> 'TARGET_ERASED'`, f.active).Scan(&tenantOpen).Error)
	read, _ := linesRead(t, api, func(a *Api) error {
		for _, c := range []context.Context{tenant, core.WithSystemContext(tenant)} {
			if _, err := fleetEvidenceOpenCount(a.RDB.DB(c), &running); err != nil {
				return err
			}
		}
		return nil
	})
	t.Logf("evidence-open counts: %d line rows read; %d open (%d the tenant's), %d planned, ledger %d", read, open, tenantOpen, planned, retainedAll)
	// Each count reads its own lines: the open lines (twice: tenant, instance) and, at most, the
	// candidate's own lines for its plans.
	require.LessOrEqual(t, read, tenantOpen+open+2*costLinesPerOp, "the evidence-open counts read the open lines, not the ledger")
}
