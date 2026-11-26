// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFleetReportCaptureHistoryOnPostgres(t *testing.T) {
	for _, history := range []string{"new device", "reports before capture", "gap after capture"} {
		t.Run(history, func(t *testing.T) {
			p := postedSettlementPG(t)
			db := p.api.RDB.DB(p.ctx)
			// Capture the other target first so the tail assertion below can
			// only be made incomplete by this device's uncaptured report.
			for i := len(p.lines) - 1; i >= 0; i-- {
				line := &p.lines[i]
				var rev DeviceConfigurationRevision
				require.NoError(t, db.Where("device_id = ? AND revision = ?", line.TargetId.Int64, line.PostedRevision.Int64).First(&rev).Error)
				if i == 0 && history == "reports before capture" {
					require.NoError(t, p.api.applyV18ConfigurationReport(p.ctx, p.devs[i].Token, rev.Revision, rev.Digest, ConfigurationReceived, nil))
				}
				require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, p.devs[i].Token, rev.Revision, rev.Digest, ConfigurationApplied, nil))
				if i == 0 && history == "gap after capture" {
					require.NoError(t, p.api.applyV18ConfigurationReport(p.ctx, p.devs[i].Token, rev.Revision, rev.Digest, ConfigurationRejected, nil))
					complete, err := p.api.FleetReportCaptureComplete(p.ctx, p.op)
					require.NoError(t, err)
					require.False(t, complete, "an uncaptured tail is incomplete")
					require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, p.devs[i].Token, rev.Revision, rev.Digest, ConfigurationApplied, nil))
				}
				var state DeviceConfigurationState
				require.NoError(t, db.Where("device_id = ?", line.TargetId.Int64).First(&state).Error)
				require.True(t, state.CaptureCompleteSince.Time.After(line.PostedAt.Time))
				require.Equal(t, i == 0 && history != "new device", state.CaptureGapThrough.Valid)
			}
			_, err := p.api.ReconcileFleetLines(p.ctx, false)
			require.NoError(t, err)
			if history == "new device" {
				for i := range p.lines {
					require.NoError(t, db.First(&p.lines[i], p.lines[i].ID).Error)
					require.Equal(t, "CONFIRMED", p.lines[i].Settlement.String)
					require.Equal(t, "REPORT_LOG", p.lines[i].EvidenceBasis.String)
				}
			}
			complete, err := p.api.FleetReportCaptureComplete(p.ctx, p.op)
			require.NoError(t, err)
			require.Equal(t, history == "new device", complete)
		})
	}
}
