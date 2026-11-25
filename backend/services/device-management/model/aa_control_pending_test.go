// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetControlPendingExpiresAndClearsHold(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	db := e.api.RDB.DB(e.ctx)
	requested := e.now
	require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", e.op.ID).
		Update("control_requested_at", requested).Error)
	for _, elapsed := range []time.Duration{0, 29*time.Second + 999*time.Millisecond} {
		e.now = requested.Add(elapsed)
		r := e.drive(t)
		require.Zero(t, r.Posted)
		require.Equal(t, "CONTROL_PENDING", r.Hold)
		require.Zero(t, e.standing.asked, "control takes priority over another authority request")
		rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
		require.NoError(t, err)
		require.Equal(t, "CONTROL_PENDING", rt.HoldCode)
		require.Equal(t, requested, rt.HoldSince.Time, "the same hold keeps its start")
		require.Zero(t, rt.PaceIssued, "a control hold spends no permits")
		require.Equal(t, int64(2), e.count(t, FleetPostingPlanned))
	}
	e.now = requested.Add(30 * time.Second)
	r := e.drive(t)
	require.Equal(t, 2, r.Posted, "a lost caller cannot hold the driver forever")
	rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Empty(t, rt.HoldCode)
	require.False(t, rt.HoldSince.Valid)
	require.False(t, rt.HoldSeenAt.Valid)
}

// I-5b: a timed-out control leaves a bounded opportunity for its caller's retry.
func TestFleetFencePendingLeavesControlHoldUntilExpiry(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	requested := e.now
	_, err := e.api.fleetControl(e.ctx, "drv", fleetEventPause, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		return nil, nil, &pgconn.PgError{Code: "55P03"}
	})
	requireFleetCode(t, err, FleetCodeFencePending)
	rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Equal(t, sql.NullTime{Time: requested, Valid: true}, rt.ControlRequestedAt)
	for _, elapsed := range []time.Duration{0, 29*time.Second + 999*time.Millisecond} {
		e.now = requested.Add(elapsed)
		r := e.drive(t)
		require.Zero(t, r.Posted)
		require.Equal(t, "CONTROL_PENDING", r.Hold)
		require.Zero(t, e.standing.asked)
		require.Equal(t, int64(2), e.count(t, FleetPostingPlanned))
	}
	e.now = requested.Add(30 * time.Second)
	r := e.drive(t)
	require.Equal(t, 2, r.Posted)
	rt, err = e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.Empty(t, rt.HoldCode)
	require.False(t, rt.HoldSince.Valid)
	require.False(t, rt.HoldSeenAt.Valid)
}

func TestFleetControlPublishesAndConditionallyClearsItsRequest(t *testing.T) {
	for _, outcome := range []string{"commit", "refusal", "newer request"} {
		t.Run(outcome, func(t *testing.T) {
			e := driveFixture(t, 1, FleetSafetyRequest{})
			refusal := fleetErr(FleetCodeIllegalTransition, "control refused")
			newer := e.now.Add(time.Second)
			_, err := e.api.fleetControl(e.ctx, "drv", fleetEventPause, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
				var rt FleetOperationRuntime
				require.NoError(t, tx.Where("operation_id = ?", op.ID).First(&rt).Error)
				require.Equal(t, sql.NullTime{Time: e.now, Valid: true}, rt.ControlRequestedAt,
					"the request must be visible before the fenced transition")
				if outcome == "refusal" {
					return nil, nil, refusal
				}
				if outcome == "newer request" {
					// Stand in for another caller publishing after this caller's timestamp.
					require.NoError(t, tx.Model(&FleetOperationRuntime{}).Where("operation_id = ?", op.ID).
						Update("control_requested_at", newer).Error)
				}
				return map[string]any{"dispatch": FleetDispatchPaused}, nil, nil
			})
			if outcome == "refusal" {
				require.ErrorIs(t, err, refusal)
				ext, ok := err.(interface{ Extensions() map[string]any })
				require.True(t, ok, "a control refusal must retain its GraphQL extensions")
				require.Equal(t, FleetCodeIllegalTransition, ext.Extensions()["code"])
			} else {
				require.NoError(t, err)
			}
			rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
			require.NoError(t, err)
			if outcome == "newer request" {
				require.Equal(t, sql.NullTime{Time: newer, Valid: true}, rt.ControlRequestedAt,
					"an older caller must never clear a newer request")
			} else {
				require.False(t, rt.ControlRequestedAt.Valid, "completed control cleans up outside its transaction")
			}
		})
	}
}

func TestFleetControlCancellationClearsItsPublishedRequest(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	db := e.api.RDB.DB(e.ctx)
	const hook = "test:cancel-control-after-publication"
	published := false
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "fleet_operation_runtime" && strings.Contains(tx.Statement.SQL.String(), "RETURNING") {
			published = true
			cancel() // The autocommit marker is durable, but BEGIN must now fail.
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(hook)) })
	_, err := e.api.fleetControl(ctx, "drv", fleetEventPause, func(tx *gorm.DB, op *FleetOperation) (map[string]any, *FleetOperationEvent, error) {
		t.Error("a canceled request must not enter the fenced transition")
		return nil, nil, errors.New("unexpected transition")
	})
	require.True(t, published)
	require.ErrorIs(t, err, context.Canceled)
	rt, err := e.api.FleetOperationRuntimeOf(e.ctx, e.op)
	require.NoError(t, err)
	require.False(t, rt.ControlRequestedAt.Valid, "non-FENCE_PENDING cleanup outlives the caller context")
}
