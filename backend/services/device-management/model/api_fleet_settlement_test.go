// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func settlementFixture(t *testing.T) (*Api, context.Context, *FleetOperationLine) {
	t.Helper()
	openTheEngine(t)
	api, ctx := fleetFixture(t, 1)
	require.NoError(t, api.RDB.Database.AutoMigrate(&DeviceConfigurationState{}, &DeviceConfigurationReport{}, &DeviceConfigurationRevision{}))
	op := readyOp(t, api, ctx, "settle", FleetSafetyRequest{}, "d1")
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Model(op).Update("dispatch", "DISPATCHED").Error)
	var line FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", op.ID).First(&line).Error)
	require.NoError(t, db.Model(&line).Updates(map[string]any{"posting": "POSTED", "posted_revision": 5, "posted_at": time.Now().Add(-time.Minute), "settle_by": time.Now().Add(-time.Second), "posted_value": `{"present":true,"valueType":"DOUBLE","value":"22.0"}`}).Error)
	require.NoError(t, db.Create(&DeviceConfigurationRevision{DeviceId: uint(line.TargetId.Int64), Revision: 5, Document: `{"site":22}`, Digest: "digest"}).Error)
	return api, ctx, &line
}

func TestSettlementErasedAndUnknown(t *testing.T) {
	for _, mode := range []string{"erased", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			api, ctx, line := settlementFixture(t)
			db := api.RDB.DB(ctx)
			if mode == "erased" {
				require.NoError(t, db.Model(line).Updates(map[string]any{"settlement": "AMBIGUOUS", "code": "TARGET_ERASED", "redacted_at": time.Now()}).Error)
			} else {
				require.NoError(t, db.Exec("PRAGMA ignore_check_constraints=ON").Error)
				require.NoError(t, db.Model(line).Update("settlement", "FUTURE").Error)
				require.NoError(t, db.Exec("PRAGMA ignore_check_constraints=OFF").Error)
			}
			require.NoError(t, db.First(line, line.ID).Error)
			before := *line
			counter := testutil.ToFloat64(fleetSettlementUnknown)
			n, err := api.ReconcileFleetLines(ctx, false)
			require.NoError(t, err)
			require.Zero(t, n)
			n, err = api.DeadlineFleetLines(ctx)
			require.NoError(t, err)
			require.Zero(t, n)
			require.NoError(t, db.First(line, line.ID).Error)
			require.Equal(t, before, *line)
			if mode == "unknown" {
				require.Greater(t, testutil.ToFloat64(fleetSettlementUnknown), counter)
			} else {
				var op FleetOperation
				require.NoError(t, db.First(&op, line.OperationId).Error)
				require.Equal(t, "CLOSED", op.Evidence)
			}
		})
	}
}

func settlementReport(t *testing.T, db *gorm.DB, line *FleetOperationLine, revision int64, status, source string) {
	t.Helper()
	require.NoError(t, db.Create(&DeviceConfigurationReport{DeviceId: uint(line.TargetId.Int64), Revision: revision, Status: status, Source: source, Digest: "digest", CreatedAt: time.Now()}).Error)
}

func TestSettlementLogFirstWinsAndDeadlinePrecedence(t *testing.T) {
	for _, first := range []string{"APPLIED", "REJECTED"} {
		t.Run(first, func(t *testing.T) {
			api, ctx, line := settlementFixture(t)
			db := api.RDB.DB(ctx)
			settlementReport(t, db, line, 5, first, "REPORT")
			second := "APPLIED"
			if first == second {
				second = "REJECTED"
			}
			settlementReport(t, db, line, 5, second, "REPORT")
			// The state row has advanced to RECEIVED; the log's r5 decider wins.
			require.NoError(t, db.Create(&DeviceConfigurationState{DeviceId: uint(line.TargetId.Int64), ReportedRevision: sql.NullInt64{Int64: 6, Valid: true}, ReportedStatus: sql.NullString{String: "RECEIVED", Valid: true}}).Error)
			require.NoError(t, db.Create(&DeviceConfigurationRevision{DeviceId: uint(line.TargetId.Int64), Revision: 6, Document: `{"site":99}`, Digest: "later"}).Error)
			n, err := api.DeadlineFleetLines(ctx)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			require.NoError(t, db.First(line, line.ID).Error)
			want := ledger.SettlementConfirmed
			if first == "REJECTED" {
				want = ledger.SettlementFailed
			}
			require.Equal(t, string(want), line.Settlement.String)
			require.Equal(t, "REPORT_LOG", line.EvidenceBasis.String)
			require.EqualValues(t, 5, line.EvidenceRevision.Int64)
			var op FleetOperation
			require.NoError(t, db.First(&op, line.OperationId).Error)
			require.Equal(t, "CLOSED", op.Evidence)
			n, err = api.ReconcileFleetLines(ctx, false)
			require.NoError(t, err)
			require.Zero(t, n)
		})
	}
}

func TestSettlementUntargetedAndMissingRevision(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	db := api.RDB.DB(ctx)
	other := *line
	other.TargetId.Int64 += 10000
	settlementReport(t, db, &other, 5, "APPLIED", "REPORT")
	n, err := api.ReconcileFleetLines(ctx, false)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, db.Model(line).Update("posted_revision", nil).Error)
	n, err = api.DeadlineFleetLines(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "AMBIGUOUS", line.Settlement.String)
	require.Equal(t, "NO_REVISION", line.Code)
}

func TestSettlementFastWindowAndSweep(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	db := api.RDB.DB(ctx)
	settlementReport(t, db, line, 5, "APPLIED", "REPORT")
	require.NoError(t, db.Model(&DeviceConfigurationReport{}).Where("device_id = ?", line.TargetId.Int64).Update("created_at", time.Now().Add(-time.Minute)).Error)
	n, err := api.ReconcileFleetLines(ctx, true)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, api.SweepFleetSettlement(ctx))
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "CONFIRMED", line.Settlement.String)
}

func TestSettlementCaptureCapabilityIsTargeted(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	db := api.RDB.DB(ctx)
	var op FleetOperation
	require.NoError(t, db.First(&op, line.OperationId).Error)
	complete, err := api.FleetReportCaptureComplete(ctx, &op)
	require.NoError(t, err)
	require.False(t, complete)
	state := DeviceConfigurationState{DeviceId: uint(line.TargetId.Int64), CaptureCompleteSince: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}}
	require.NoError(t, db.Create(&state).Error)
	complete, err = api.FleetReportCaptureComplete(ctx, &op)
	require.NoError(t, err)
	require.True(t, complete)
	// Another device's absent capture does not degrade this operation.
	require.NoError(t, db.Create(&DeviceConfigurationState{DeviceId: state.DeviceId + 10000}).Error)
	complete, err = api.FleetReportCaptureComplete(ctx, &op)
	require.NoError(t, err)
	require.True(t, complete)
	require.NoError(t, db.Model(&state).Update("capture_gap_through", 5).Error)
	complete, err = api.FleetReportCaptureComplete(ctx, &op)
	require.NoError(t, err)
	require.False(t, complete)
}

func TestSettlementDeadlineAmbiguousThenEvidenceAndClosed(t *testing.T) {
	api, ctx, line := settlementFixture(t)
	db := api.RDB.DB(ctx)
	n, err := api.DeadlineFleetLines(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "AMBIGUOUS", line.Settlement.String)
	var op FleetOperation
	require.NoError(t, db.First(&op, line.OperationId).Error)
	require.Equal(t, "OPEN", op.Evidence)
	// Even CLOSED headers remain eligible for evidence (operator resolution may have closed them).
	require.NoError(t, db.Model(&op).Update("evidence", "CLOSED").Error)
	settlementReport(t, db, line, 5, "APPLIED", "REPORT")
	n, err = api.ReconcileFleetLines(ctx, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.First(line, line.ID).Error)
	require.Equal(t, "CONFIRMED", line.Settlement.String)
}

func TestSettlementProtocolAndResolvedAndTail(t *testing.T) {
	for _, mode := range []string{"protocol", "tail", "gap", "resolved"} {
		t.Run(mode, func(t *testing.T) {
			api, ctx, line := settlementFixture(t)
			db := api.RDB.DB(ctx)
			if mode == "protocol" {
				require.NoError(t, db.Model(&FleetOperation{}).Where("id = ?", line.OperationId).Update("driver_protocol", 2).Error)
			}
			if mode == "resolved" {
				require.NoError(t, db.Model(line).Update("settlement", "RESOLVED_FAILED").Error)
			}
			now := time.Now()
			state := DeviceConfigurationState{DeviceId: uint(line.TargetId.Int64), ReportedRevision: sql.NullInt64{Int64: 5, Valid: true}, ReportedStatus: sql.NullString{String: "APPLIED", Valid: true}, ReportedAt: sql.NullTime{Time: now, Valid: true}}
			if mode == "gap" {
				state.CaptureGapThrough = sql.NullInt64{Int64: 5, Valid: true}
			}
			require.NoError(t, db.Create(&state).Error)
			if mode != "tail" {
				settlementReport(t, db, line, 5, "APPLIED", "REPORT")
			}
			_, err := api.ReconcileFleetLines(ctx, false)
			require.NoError(t, err)
			_, err = api.DeadlineFleetLines(ctx)
			require.NoError(t, err)
			require.NoError(t, db.First(line, line.ID).Error)
			switch mode {
			case "protocol":
				require.False(t, line.Settlement.Valid)
				require.False(t, line.EvidenceRevision.Valid)
			case "resolved":
				require.Equal(t, "RESOLVED_FAILED", line.Settlement.String)
				require.Equal(t, "EVIDENCE_CONTRADICTS_VERDICT", line.EvidenceFlag)
				var count int64
				require.NoError(t, db.Model(&FleetOperationEvent{}).Where("line_id=? AND kind='CONTRADICTING_EVIDENCE'", line.ID).Count(&count).Error)
				require.EqualValues(t, 1, count)
				n, err := api.ReconcileFleetLines(ctx, false)
				require.NoError(t, err)
				require.Zero(t, n)
				require.NoError(t, db.Model(&FleetOperationEvent{}).Where("line_id=? AND kind='CONTRADICTING_EVIDENCE'", line.ID).Count(&count).Error)
				require.EqualValues(t, 1, count)
			case "tail", "gap":
				require.Equal(t, "LATEST_STATE", line.EvidenceBasis.String)
			}
		})
	}
}
