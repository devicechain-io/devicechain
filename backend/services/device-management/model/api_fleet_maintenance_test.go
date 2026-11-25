// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func assertFleetErasure(t *testing.T, api *Api, ctx context.Context, dev *Device, erase func() error) {
	t.Helper()
	// Match the real partial unique index, and erase one device across operations.
	require.NoError(t, api.RDB.DB(core.WithSystemContext(ctx)).Exec(`CREATE UNIQUE INDEX IF NOT EXISTS test_erasure_target
ON fleet_operation_lines (operation_id, target_id) WHERE target_id IS NOT NULL`).Error)
	var ids []uint
	for _, token := range []string{"erase-planned", "erase-posted", "erase-resolved", "erase-ambiguous"} {
		op := seedExpiryOperation(t, api, ctx, token, FleetDispatchRunning, time.Now())
		ids = append(ids, op.ID)
		require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).Delete(&FleetOperationLine{}).Error)
	}
	resolvedAt := time.Now().UTC()
	note := FleetOperationEvent{OperationId: int64(ids[2]), Kind: "RESOLUTION", At: resolvedAt, Note: sql.NullString{String: dev.Token, Valid: true}}
	require.NoError(t, api.RDB.DB(ctx).Create(&note).Error)
	for i, posting := range []string{"PLANNED", "POSTED", "POSTED", "POSTED"} {
		line := FleetOperationLine{OperationId: int64(ids[i]), Ordinal: 1, TargetId: sql.NullInt64{Int64: int64(dev.ID), Valid: true}, TargetKey: dev.Token, Posting: posting, Detail: dev.Token, PostedValue: sql.NullString{String: dev.Token, Valid: true}}
		if posting == "POSTED" {
			line.Prior = sql.NullString{String: dev.Token, Valid: true}
		}
		if i == 2 {
			line.Settlement = sql.NullString{String: "RESOLVED_CONFIRMED", Valid: true}
			line.ResolutionId = sql.NullInt64{Int64: int64(note.ID), Valid: true}
			line.ResolvedBy = sql.NullString{String: "alice", Valid: true}
			line.ResolvedAt = sql.NullTime{Time: resolvedAt, Valid: true}
		}
		if i == 3 {
			line.Settlement = sql.NullString{String: "AMBIGUOUS", Valid: true}
		}
		require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
	}
	survivor := &Device{DeviceTypeId: dev.DeviceTypeId}
	survivor.Token = "erasure-survivor"
	require.NoError(t, api.RDB.DB(ctx).Create(survivor).Error)
	shared := FleetOperationLine{OperationId: int64(ids[2]), Ordinal: 2, Posting: "POSTED",
		TargetId: sql.NullInt64{Int64: int64(survivor.ID), Valid: true}, TargetKey: survivor.Token,
		Settlement:   sql.NullString{String: "RESOLVED_CONFIRMED", Valid: true},
		ResolutionId: sql.NullInt64{Int64: int64(note.ID), Valid: true}, Prior: sql.NullString{String: "keep", Valid: true}}
	require.NoError(t, api.RDB.DB(ctx).Create(&shared).Error)
	require.NoError(t, erase())
	var lines []FleetOperationLine
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id IN ? AND target_id = ?", ids, dev.ID).Order("operation_id").Find(&lines).Error)
	require.Len(t, lines, 4)
	for _, l := range lines {
		require.Empty(t, l.TargetKey)
		require.Empty(t, l.Detail)
		require.False(t, l.Prior.Valid)
		require.False(t, l.PostedValue.Valid)
		require.True(t, l.RedactedAt.Valid)
	}
	require.Equal(t, "SKIPPED", lines[0].Posting)
	require.Equal(t, "TARGET_ERASED", lines[0].Code)
	for _, i := range []int{1, 3} {
		require.Equal(t, "AMBIGUOUS", lines[i].Settlement.String)
		require.Equal(t, "TARGET_ERASED", lines[i].Code)
	}
	require.Equal(t, "RESOLVED_CONFIRMED", lines[2].Settlement.String)
	require.Equal(t, "alice", lines[2].ResolvedBy.String)
	require.True(t, lines[2].ResolvedAt.Time.Equal(resolvedAt))
	require.NoError(t, api.RDB.DB(ctx).First(&note, note.ID).Error)
	require.Empty(t, note.Note.String)
	require.True(t, note.NoteRedactedAt.Valid)
	// A fresh struct: gorm would leave the created Prior in place if the column
	// had been wrongly cleared to NULL.
	var kept FleetOperationLine
	require.NoError(t, api.RDB.DB(ctx).First(&kept, shared.ID).Error)
	require.Equal(t, survivor.Token, kept.TargetKey)
	require.Equal(t, "keep", kept.Prior.String)
	require.False(t, kept.RedactedAt.Valid)
	require.Equal(t, int64(note.ID), kept.ResolutionId.Int64)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationEvent{}).Where("operation_id IN ? AND kind = 'REDACTED'", ids).Count(&count).Error)
	require.Equal(t, int64(4), count)
	require.NoError(t, api.RepairFleetErasure(ctx))
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationEvent{}).Where("operation_id IN ? AND kind = 'REDACTED'", ids).Count(&count).Error)
	require.Equal(t, int64(4), count, "repair is idempotent")
}

func TestFleetDeleteRedactsInTransaction(t *testing.T) {
	e := postFixture(t, "v1", false)
	assertFleetErasure(t, e.api, e.ctx, e.dev, func() error { _, err := e.api.DeleteDevice(e.ctx, e.dev.Token); return err })
}

func TestFleetRepairRedactsOldPodDeleteIncludingNotes(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	var dev Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = 'd1'").First(&dev).Error)
	// Observe the actual generated repair query: NOT IN has bad large-table plans.
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("repair:anti-join", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "devices") {
			require.NotContains(t, strings.ToUpper(tx.Statement.SQL.String()), "NOT IN")
		}
	}))
	assertFleetErasure(t, api, ctx, &dev, func() error {
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&dev).Error)
		return api.RepairFleetErasure(ctx)
	})
}

func TestFleetRetentionWindowsAndTombstone(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now().UTC()
	for _, tc := range []struct {
		token, dispatch, evidence string
		age                       int
		posted                    bool
		purge, warning            bool
	}{
		{"never-posted", "ABORTED", "CLOSED", 0, false, true, false},
		{"closed89", "DISPATCHED", "CLOSED", 89, true, false, false},
		{"closed90", "DISPATCHED", "CLOSED", 90, true, true, false},
		{"open165", "DISPATCHED", "OPEN", 165, true, false, false},
		{"open166", "DISPATCHED", "OPEN", 166, true, false, true},
		{"open180", "DISPATCHED", "OPEN", 180, true, true, true},
		{"running179", "RUNNING", "OPEN", 179, true, false, true},
		{"running180", "RUNNING", "OPEN", 180, true, true, true},
	} {
		t.Run(tc.token, func(t *testing.T) {
			op := seedExpiryOperation(t, api, ctx, tc.token, tc.dispatch, now)
			// seedExpiryOperation already creates the runtime row, as the real create path does.
			changes := map[string]any{"evidence": tc.evidence, "created_at": now.Add(-time.Duration(tc.age) * 24 * time.Hour), "closed_at": now.Add(-time.Duration(tc.age) * 24 * time.Hour)}
			if tc.posted {
				changes["posted_line_count"] = 1
				require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Update("posting", "POSTED").Error)
			}
			require.NoError(t, api.RDB.DB(ctx).Model(op).Updates(changes).Error)
			require.NoError(t, api.RetainFleetOperations(ctx, now))
			var got FleetOperation
			require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
			require.Equal(t, tc.purge, got.LinesPurgedAt.Valid)
			if tc.dispatch == "RUNNING" {
				if tc.purge {
					require.Equal(t, "ABORTED", got.Dispatch)
					require.Equal(t, int64(1), got.ExecutionEpoch)
					require.True(t, got.StateChangedAt.Valid)
				} else {
					require.Equal(t, "RUNNING", got.Dispatch)
					require.Zero(t, got.ExecutionEpoch)
				}
			}
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationEvent{}).Where("operation_id = ? AND kind = 'RETENTION_WARNING'", op.ID).Count(&n).Error)
			require.Equal(t, tc.warning, n == 1)
			if tc.purge {
				require.NoError(t, api.RetainFleetOperations(ctx, now.Add(399*24*time.Hour)))
				require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
				var runtime int64
				require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", op.ID).Count(&runtime).Error)
				require.Equal(t, int64(1), runtime)
				require.NoError(t, api.RetainFleetOperations(ctx, now.Add(400*24*time.Hour)))
				require.ErrorIs(t, api.RDB.DB(ctx).First(&FleetOperation{}, op.ID).Error, gorm.ErrRecordNotFound)
				require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", op.ID).Count(&runtime).Error)
				require.Zero(t, runtime)
			}
		})
	}
}

func TestFleetMaintenanceProtocolAndTenantIsolation(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	op := seedExpiryOperation(t, api, ctx, "newer", "ABORTED", time.Now())
	require.NoError(t, api.RDB.DB(ctx).Model(op).Update("driver_protocol", 2).Error)
	other := core.WithTenant(context.Background(), "globex")
	seedExpiryOperation(t, api, other, "other", "ABORTED", time.Now())
	require.NoError(t, api.RetainFleetOperations(ctx, time.Now()))
	var n int64
	require.NoError(t, api.RDB.DB(core.WithSystemContext(ctx)).Model(&FleetOperationLine{}).Count(&n).Error)
	require.Equal(t, int64(2), n)
	require.Error(t, api.RetainFleetOperations(core.WithSystemContext(ctx), time.Now()))
	require.Error(t, api.RepairFleetErasure(core.WithSystemContext(ctx)))
}

func TestFleetMaintenanceVisitsTenantContexts(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	op := seedExpiryOperation(t, api, ctx, "acme-abort", "ABORTED", time.Now())
	other := core.WithTenant(context.Background(), "globex")
	otherOp := seedExpiryOperation(t, api, other, "globex-abort", "ABORTED", time.Now())
	require.NoError(t, api.MaintainFleetOperations(context.Background()))
	for _, tc := range []struct {
		ctx context.Context
		op  *FleetOperation
	}{{ctx, op}, {other, otherOp}} {
		var got FleetOperation
		require.NoError(t, api.RDB.DB(tc.ctx).First(&got, tc.op.ID).Error)
		require.True(t, got.LinesPurgedAt.Valid)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, api.MaintainFleetOperations(cancelled), context.Canceled)
}
