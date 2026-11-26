// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetRetentionDoesNotRelockOldPodPostedAbort(t *testing.T) {
	for _, evidence := range []string{"OPEN", "CLOSED"} {
		t.Run(evidence, func(t *testing.T) {
			api, ctx := fleetFixture(t, 0)
			assertFleetRetentionPostedAbortHint(t, api, ctx, evidence)
		})
	}
}

func assertFleetRetentionPostedAbortHint(t *testing.T, api *Api, ctx context.Context, evidence string) {
	t.Helper()
	now, err := fleetMaintenanceNow(api.RDB.DB(ctx))
	require.NoError(t, err)
	op := seedExpiryOperation(t, api, ctx, "old-pod-posted-"+evidence, "ABORTED", now)
	require.NoError(t, api.RDB.DB(ctx).Model(op).Updates(map[string]any{
		"evidence": evidence, "created_at": now, "closed_at": now,
	}).Error)
	// A previous-minor pod posts without maintaining the header counter.
	require.Zero(t, op.PostedLineCount)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Update("posting", "POSTED").Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	lockedReads := 0
	const hook = "test:retention-relock"
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if got, ok := tx.Statement.Dest.(*FleetOperation); ok && got.ID == op.ID {
			lockedReads++ // This header read follows the exclusive operation fence.
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove(hook) })
	require.NoError(t, api.RetainFleetOperations(ctx, now.Add(time.Minute)))
	require.Zero(t, lockedReads, "an examined abort with posted evidence must not take the operation fence again")
	require.NoError(t, api.RDB.Database.Callback().Query().Remove(hook))
	var got FleetOperation
	require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
	require.False(t, got.LinesPurgedAt.Valid, "a zero old-pod counter must not permit immediate purge")
	due := fleetClosedRetention
	if evidence == "OPEN" {
		due = fleetOpenRetention
		require.NoError(t, api.RetainFleetOperations(ctx, now.Add(fleetOpenRetention-fleetRetentionWarning)))
		var warnings int64
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationEvent{}).Where("operation_id = ? AND kind = 'RETENTION_WARNING'", op.ID).Count(&warnings).Error)
		require.Equal(t, int64(1), warnings, "narrowing the abort hint must preserve scheduled warnings")
	}
	require.NoError(t, api.RetainFleetOperations(ctx, now.Add(due)))
	require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
	require.True(t, got.LinesPurgedAt.Valid, "posted aborts still expire at their normal deadline")
}

func TestFleetMaintenanceRetentionBacklogCannotStarveRepair(t *testing.T) {
	api, _ := fleetFixture(t, 0)
	assertFleetMaintenanceBacklogFairness(t, api, "000-backlog-a", "000-repair-b")
	// The cursor now points at B, the last tenant. The next pass must wrap
	// and process A rather than treating the end of the list as no work.
	require.NoError(t, api.MaintainFleetOperations(context.Background()))
	a := core.WithTenant(context.Background(), "000-backlog-a")
	got, err := api.fleetOperationByToken(a, "backlog-1")
	require.NoError(t, err)
	require.True(t, got.LinesPurgedAt.Valid, "the tenant cursor must wrap")
}

// Cancel at the retention hint to model a tenant consuming the pass budget,
// without sleeping 45 seconds. Every pass has enough budget to start one tenant.
// The next pass must resume after that attempted tenant, including on failure.
func assertFleetMaintenanceBacklogFairness(t *testing.T, api *Api, tenantA, tenantB string) {
	t.Helper()
	a := core.WithTenant(context.Background(), tenantA)
	b := core.WithTenant(context.Background(), tenantB)
	now, err := fleetMaintenanceNow(api.RDB.DB(a))
	require.NoError(t, err)
	for _, token := range []string{"backlog-1", "backlog-2", "backlog-3"} {
		seedExpiryOperation(t, api, a, token, "ABORTED", now)
	}
	op := seedExpiryOperation(t, api, b, "old-pod-delete", "DISPATCHED", now)
	// Journals intentionally have no target FK: this is a hard-deleted target
	// whose private data a previous-minor pod left in the journal.
	const erasedID = 987654321
	var devices int64
	require.NoError(t, api.RDB.DB(b).Model(&Device{}).Where("id = ?", erasedID).Count(&devices).Error)
	require.Zero(t, devices)
	require.NoError(t, api.RDB.DB(b).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Updates(map[string]any{
		"posting": "POSTED", "target_id": erasedID, "target_key": "private-device", "prior": "private-value",
	}).Error)
	var cancelPass context.CancelFunc
	var visits []string
	const hook = "test:maintenance-budget"
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]FleetOperation); !ok {
			return
		}
		if len(tx.Statement.Selects) == 1 && tx.Statement.Selects[0] == "id" {
			tenant, _ := core.TenantFromContext(tx.Statement.Context)
			visits = append(visits, tenant)
			cancelPass()
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove(hook) })
	for pass := 0; pass < 2; pass++ {
		var ctx context.Context
		ctx, cancelPass = context.WithCancel(context.Background())
		err := api.MaintainFleetOperations(ctx)
		cancelPass()
		require.ErrorIs(t, err, context.Canceled)
		var line FleetOperationLine
		require.NoError(t, api.RDB.DB(b).Where("operation_id = ?", op.ID).First(&line).Error)
		if pass == 0 {
			require.False(t, line.RedactedAt.Valid, "tenant A used the first pass")
		} else {
			require.True(t, line.RedactedAt.Valid, "tenant B's repair must run by the second pass despite A's backlog")
			require.Empty(t, line.TargetKey)
			require.False(t, line.Prior.Valid)
			require.Equal(t, sql.NullString{String: "AMBIGUOUS", Valid: true}, line.Settlement)
		}
	}
	require.Equal(t, []string{tenantA, tenantB}, visits)
	require.NoError(t, api.RDB.Database.Callback().Query().Remove(hook))
}
