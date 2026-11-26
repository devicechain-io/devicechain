// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFleetReportCaptureFromFirstReportAfterPost(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	db := api.RDB.DB(ctx)
	var op FleetOperation
	require.NoError(t, db.First(&op, line.OperationId).Error)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "d1", 5, "digest", ConfigurationApplied, nil))
	var state DeviceConfigurationState
	require.NoError(t, db.Where("device_id = ?", line.TargetId.Int64).First(&state).Error)
	require.NoError(t, db.First(line, line.ID).Error)
	require.True(t, state.CaptureCompleteSince.Time.After(line.PostedAt.Time))
	require.False(t, state.CaptureGapThrough.Valid, "the first report leaves no missed history")
	n, err := api.ReconcileFleetLines(ctx, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "CONFIRMED", line.Settlement.String)
	require.Equal(t, "REPORT_LOG", line.EvidenceBasis.String)
	complete, err := api.FleetReportCaptureComplete(ctx, &op)
	require.NoError(t, err)
	require.True(t, complete, "continuous capture from the device's first report is complete")
}

func TestFleetReportCaptureHistoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gap      sql.NullInt64
		since    time.Duration
		tail     bool
		missing  bool
		complete bool
	}{
		{name: "first capture after post", since: time.Minute, complete: true},
		{name: "first capture before post", since: -time.Minute, complete: true},
		{name: "missed earlier reports and late capture", gap: sql.NullInt64{Int64: 4, Valid: true}, since: time.Minute},
		{name: "gap before post then continuous capture", gap: sql.NullInt64{Int64: 4, Valid: true}, since: -time.Minute, complete: true},
		{name: "gap at posted revision", gap: sql.NullInt64{Int64: 5, Valid: true}, since: -time.Minute},
		{name: "gap beyond posted revision", gap: sql.NullInt64{Int64: 6, Valid: true}, since: -time.Minute},
		{name: "uncaptured tail", since: time.Minute, tail: true},
		{name: "missing capture start", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, line := settlementFixture(t)
			db := api.RDB.DB(ctx)
			var op FleetOperation
			require.NoError(t, db.First(&op, line.OperationId).Error)
			require.NoError(t, db.First(line, line.ID).Error)
			now := time.Now().UTC()
			state := DeviceConfigurationState{
				DeviceId:             uint(line.TargetId.Int64),
				CaptureGapThrough:    tc.gap,
				CaptureCompleteSince: sql.NullTime{Time: line.PostedAt.Time.Add(tc.since), Valid: !tc.missing},
				ReportedRevision:     sql.NullInt64{Int64: 7, Valid: true},
				ReportedStatus:       sql.NullString{String: "APPLIED", Valid: true},
				ReportedAt:           sql.NullTime{Time: now, Valid: true},
			}
			state.CapturedRevision, state.CapturedStatus, state.CapturedReportedAt = state.ReportedRevision, state.ReportedStatus, state.ReportedAt
			if tc.tail {
				state.ReportedAt.Time = now.Add(time.Second)
			}
			require.NoError(t, db.Create(&state).Error)
			complete, err := api.FleetReportCaptureComplete(ctx, &op)
			require.NoError(t, err)
			require.Equal(t, tc.complete, complete)
		})
	}
}
