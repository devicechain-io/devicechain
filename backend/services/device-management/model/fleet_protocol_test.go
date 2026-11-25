// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// insertProtocol2OperationForTest copies a fixture header into a newer operation.
// Callers can seed its lines with vocabulary this build understands (or future vocabulary).
func insertProtocol2OperationForTest(t *testing.T, db *gorm.DB, template FleetOperation) *FleetOperation {
	t.Helper()
	template.ID = 0
	template.Token += "-protocol2"
	template.DriverProtocol = 2
	require.NoError(t, db.Create(&template).Error)
	require.NoError(t, db.Create(&FleetOperationRuntime{OperationId: int64(template.ID)}).Error)
	return &template
}

func protocolLinesForTest(t *testing.T, db *gorm.DB, op *FleetOperation) []byte {
	t.Helper()
	var lines []FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", op.ID).Order("ordinal").Find(&lines).Error)
	b, err := json.Marshal(lines)
	require.NoError(t, err)
	return b
}

func TestProtocol2DriveIsEntirelyUntouched(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	db := e.api.RDB.DB(e.ctx)
	op := insertProtocol2OperationForTest(t, db, *e.op)
	var line FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", e.op.ID).First(&line).Error)
	line.ID, line.OperationId = 0, int64(op.ID)
	require.NoError(t, db.Create(&line).Error)
	before := protocolLinesForTest(t, db, op)
	runtimeBefore, err := e.api.FleetOperationRuntimeOf(e.ctx, op)
	require.NoError(t, err)

	r, err := e.api.DriveFleetOperation(e.ctx, op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, &FleetDriveResult{}, r)
	require.Zero(t, e.standing.asked, "a newer operation does not even ask for a lease")
	runtimeAfter, err := e.api.FleetOperationRuntimeOf(e.ctx, op)
	require.NoError(t, err)
	require.Equal(t, runtimeBefore, runtimeAfter, "no heartbeat, hold or permits")
	require.Equal(t, before, protocolLinesForTest(t, db, op))
	got, err := e.api.FleetOperationByToken(e.ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, op.ExecutionEpoch, got.ExecutionEpoch)
	// Negative control: the protocol-1 operation in the same database still runs.
	require.Equal(t, 2, e.drive(t).Posted)
}

func TestProtocol2PostAndRefusalAreFenced(t *testing.T) {
	for _, refusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "post", true: "refusal"}[refusal], func(t *testing.T) {
			e := postFixture(t, "v1", false)
			db := e.api.RDB.DB(e.ctx)
			require.NoError(t, db.Model(e.op).Update("driver_protocol", 2).Error)
			before := protocolLinesForTest(t, db, e.op)
			var r FleetPostResult
			var err error
			if refusal {
				r, err = e.api.recordFleetRefusal(e.ctx, e.op.TenantId, e.op.ID, e.line.ID, e.lease,
					fleetDecision{FleetPostingRefused, FleetLineKeyUndeclared, "undeclared"})
			} else {
				r, err = e.api.PostFleetLine(e.ctx, e.op.ID, e.line.ID, e.lease)
			}
			require.NoError(t, err)
			require.Equal(t, FleetPostFenceRefused, r.Outcome)
			require.Equal(t, before, protocolLinesForTest(t, db, e.op))
			_, present := attrOf(t, e)
			require.False(t, present, "the effect did not run")
		})
	}
}

func TestProtocol2ControlsOnlyAllowPause(t *testing.T) {
	for _, action := range []string{"start", "abort", "resume", "pause"} {
		t.Run(action, func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 2)
			op := readyOp(t, api, ctx, "newer", FleetSafetyRequest{}, "d1", "d2")
			if action != "start" {
				var err error
				op, err = api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
				require.NoError(t, err)
			}
			if action == "resume" {
				var err error
				op, err = api.PauseFleetOperation(ctx, op.Token, alice.Name)
				require.NoError(t, err)
			}
			db := api.RDB.DB(ctx)
			require.NoError(t, db.Model(op).Update("driver_protocol", 2).Error)
			if action != "start" {
				require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ? AND ordinal = ?", op.ID, 2).
					Updates(map[string]any{"posting": FleetPostingPosted, "settlement": "CONFIRMED"}).Error)
			}
			before := protocolLinesForTest(t, db, op)
			beforeEvents := events(t, api, ctx, op)
			var err error
			switch action {
			case "start":
				_, err = api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
			case "abort":
				_, err = api.AbortFleetOperation(ctx, op.Token, alice.Name)
			case "resume":
				_, err = api.ResumeFleetOperation(ctx, op.Token, op.ContractDigest, nil, alice)
			case "pause":
				_, err = api.PauseFleetOperation(ctx, op.Token, alice.Name)
			}
			// Check lines first so allowing abort is killed by the cancelled-line recount.
			require.Equal(t, before, protocolLinesForTest(t, db, op))
			got, readErr := api.FleetOperationByToken(ctx, op.Token)
			require.NoError(t, readErr, "newer headers remain readable")
			if action == "pause" {
				require.NoError(t, err)
				require.Equal(t, FleetDispatchPaused, got.Dispatch)
				require.Equal(t, op.ExecutionEpoch+1, got.ExecutionEpoch)
			} else {
				requireFleetCode(t, err, "OPERATION_NEWER_THAN_SERVER")
				require.Equal(t, "this bulk change was created by a newer release; retry, or pause it", err.Error())
				require.Equal(t, op.ExecutionEpoch, got.ExecutionEpoch)
				require.Equal(t, beforeEvents, events(t, api, ctx, op))
			}
		})
	}
}

func TestProtocolScopeAndExpirySkipNewerOperations(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 1)
	db := api.RDB.DB(ctx)
	op := readyOp(t, api, ctx, "expires", FleetSafetyRequest{}, "d1")
	now := time.Now()
	op.ExpiresAt = sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
	op = insertProtocol2OperationForTest(t, db, *op)
	n, err := api.ExpireFleetOperations(ctx, now)
	require.NoError(t, err)
	require.Zero(t, n)
	got, err := api.FleetOperationByToken(ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, op.ExecutionEpoch, got.ExecutionEpoch)
}
