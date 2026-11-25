// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFleetRepairFiltersEachOperationProtocol(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	for _, token := range []string{"supported", "future"} {
		op := seedExpiryOperation(t, api, ctx, token, "RUNNING", time.Now())
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).
			Updates(map[string]any{"target_id": 999, "target_key": "gone"}).Error)
		if token == "future" {
			require.NoError(t, api.RDB.DB(ctx).Model(op).Update("driver_protocol", 2).Error)
		}
	}
	require.NoError(t, api.RepairFleetErasure(ctx))
	for _, token := range []string{"supported", "future"} {
		op, err := api.fleetOperationByToken(ctx, token)
		require.NoError(t, err)
		var line FleetOperationLine
		require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&line).Error)
		require.Equal(t, token == "supported", line.RedactedAt.Valid)
	}
	// An unresolved input has no device id and must never be mistaken for an orphan.
	var line FleetOperationLine
	line.OperationId = 1
	line.Ordinal = 2
	line.Posting = "REFUSED"
	line.TargetId = sql.NullInt64{}
	require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
	require.NoError(t, api.RepairFleetErasure(ctx))
	require.NoError(t, api.RDB.DB(ctx).First(&line, line.ID).Error)
	require.False(t, line.RedactedAt.Valid)
}

func TestFleetRepairRemovesOrphanCaptureRows(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	var dev Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = 'd1'").First(&dev).Error)
	for _, id := range []uint{dev.ID, 999} {
		require.NoError(t, api.RDB.DB(ctx).Create(&DeviceConfigurationReport{DeviceId: id, Revision: 1, Status: "APPLIED", Source: "REPORT"}).Error)
	}
	require.NoError(t, api.RepairFleetErasure(ctx))
	var reports []DeviceConfigurationReport
	require.NoError(t, api.RDB.DB(ctx).Find(&reports).Error)
	require.Len(t, reports, 1)
	require.Equal(t, dev.ID, reports[0].DeviceId)
}
