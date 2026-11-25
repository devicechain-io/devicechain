// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetErasureFailureRollsBackDeviceDelete(t *testing.T) {
	e := postFixture(t, "v1", false)
	failure := errors.New("redaction event unavailable")
	require.NoError(t, e.api.RDB.Database.Callback().Create().Before("gorm:create").Register("erase:fail", func(tx *gorm.DB) {
		switch events := tx.Statement.Dest.(type) {
		case *FleetOperationEvent:
			if events.Kind == "REDACTED" {
				tx.AddError(failure)
			}
		case []FleetOperationEvent:
			for _, event := range events {
				if event.Kind == "REDACTED" {
					tx.AddError(failure)
					break
				}
			}
		case *[]FleetOperationEvent:
			for _, event := range *events {
				if event.Kind == "REDACTED" {
					tx.AddError(failure)
					break
				}
			}
		}
	}))
	deleted, err := e.api.DeleteDevice(e.ctx, e.dev.Token)
	require.ErrorIs(t, err, failure)
	require.False(t, deleted)
	var dev Device
	require.NoError(t, e.api.RDB.DB(e.ctx).First(&dev, e.dev.ID).Error)
	line := e.reload(t)
	require.Equal(t, dev.Token, line.TargetKey)
	require.False(t, line.RedactedAt.Valid)
}

// The live-reversal retention predicate is local to BC-8. BC-9 supplies I-18's
// concurrent reversal creation/posting acceptance test against this fence.
func TestFleetRetentionKeepsEvidenceForLiveReversal(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now()
	original := seedExpiryOperation(t, api, ctx, "original", "ABORTED", now)
	require.NoError(t, api.RDB.DB(ctx).Model(original).Updates(map[string]any{"evidence": "CLOSED", "closed_at": now.Add(-90 * 24 * time.Hour), "posted_line_count": 1}).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", original.ID).Updates(map[string]any{"posting": "POSTED", "prior": `{"present":false}`}).Error)
	reversal := seedExpiryOperation(t, api, ctx, "reversal", "READY", now)
	require.NoError(t, api.RDB.DB(ctx).Model(reversal).Update("reversal_of", original.ID).Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	var got FleetOperation
	require.NoError(t, api.RDB.DB(ctx).First(&got, original.ID).Error)
	require.False(t, got.LinesPurgedAt.Valid)
	// A dispatched reversal can still be waiting for evidence.
	require.NoError(t, api.RDB.DB(ctx).Model(reversal).Update("dispatch", "DISPATCHED").Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	require.NoError(t, api.RDB.DB(ctx).First(&got, original.ID).Error)
	require.False(t, got.LinesPurgedAt.Valid)
	require.NoError(t, api.RDB.DB(ctx).Model(reversal).Updates(map[string]any{"evidence": "CLOSED", "closed_at": sql.NullTime{Time: now, Valid: true}}).Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	require.NoError(t, api.RDB.DB(ctx).First(&got, original.ID).Error)
	require.True(t, got.LinesPurgedAt.Valid)
}

func TestFleetOldPodHookDisabledRepairsWithFullDeletePath(t *testing.T) {
	e := postFixture(t, "v1", false)
	e.api.skipFleetErasureForOldPodTest = true
	assertFleetErasure(t, e.api, e.ctx, e.dev, func() error {
		deleted, err := e.api.DeleteDevice(e.ctx, e.dev.Token)
		require.NoError(t, err)
		require.True(t, deleted)
		require.False(t, e.reload(t).RedactedAt.Valid)
		return e.api.RepairFleetErasure(e.ctx)
	})
}

func TestFleetRetentionChunksEvidenceBeforeStampingTombstone(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now()
	op := seedExpiryOperation(t, api, ctx, "chunks", "ABORTED", now)
	lines := make([]FleetOperationLine, 10000)
	events := make([]FleetOperationEvent, 10001)
	contracts := make([]FleetOperationContract, 10001)
	for i := range events {
		if i < len(lines) {
			lines[i] = FleetOperationLine{OperationId: int64(op.ID), Ordinal: int32(i + 2), Posting: "CANCELLED"}
		}
		events[i] = FleetOperationEvent{OperationId: int64(op.ID), Kind: "RESOLUTION", At: now}
		contracts[i] = FleetOperationContract{OperationId: int64(op.ID), Version: int32(i + 1), Body: "private contract"}
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&lines, 100).Error)
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&events, 100).Error)
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&contracts, 100).Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	for _, model := range []any{&FleetOperationLine{}, &FleetOperationEvent{}, &FleetOperationContract{}} {
		var n int64
		query := api.RDB.DB(ctx).Model(model).Where("operation_id = ?", op.ID)
		if _, ok := model.(*FleetOperationContract); ok {
			query = query.Where("body <> ''")
		}
		require.NoError(t, query.Count(&n).Error)
		require.Equal(t, int64(1), n, "each table mutation is bounded to 10000 rows")
	}
	var got FleetOperation
	require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
	require.False(t, got.LinesPurgedAt.Valid)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
	require.True(t, got.LinesPurgedAt.Valid)
}

func TestFleetOpenRetentionContinuesAfterEvidenceClosure(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now()
	op := seedExpiryOperation(t, api, ctx, "open-chunks", "DISPATCHED", now)
	require.NoError(t, api.RDB.DB(ctx).Model(op).Updates(map[string]any{"created_at": now.Add(-180 * 24 * time.Hour), "posted_line_count": 1}).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Update("posting", "POSTED").Error)
	events := make([]FleetOperationEvent, 10001)
	for i := range events {
		events[i] = FleetOperationEvent{OperationId: int64(op.ID), Kind: "RESOLUTION", At: now}
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&events, 100).Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	var got FleetOperation
	require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
	require.False(t, got.LinesPurgedAt.Valid)
	// The reconciler can close evidence once the first chunk removes all lines.
	// Retention must continue next minute rather than restarting a 90-day wait.
	require.NoError(t, api.RDB.DB(ctx).Model(op).Updates(map[string]any{"evidence": "CLOSED", "closed_at": now}).Error)
	require.NoError(t, api.RetainFleetOperations(ctx, now.Add(time.Minute)))
	require.NoError(t, api.RDB.DB(ctx).First(&got, op.ID).Error)
	require.True(t, got.LinesPurgedAt.Valid)
}
