// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetPostgresControlRequestIsAutocommittedAndPreservesNewerRequest(t *testing.T) {
	p := newPostPG(t, "v1")
	db := p.api.RDB.DB(p.ctx)
	var newer sql.NullTime
	_, err := p.api.fleetControl(p.ctx, p.op.Token, fleetEventPause, func(tx *gorm.DB, op *FleetOperation, now time.Time) (map[string]any, *FleetOperationEvent, error) {
		// Another connection sees the marker while the fenced transaction is still open.
		// It must also be able to replace it: control must never lock the runtime row.
		err := db.Transaction(func(witness *gorm.DB) error {
			if err := fleetSetLockTimeout(witness, "500ms"); err != nil {
				return err
			}
			var requested sql.NullTime
			if err := witness.Model(&FleetOperationRuntime{}).Select("control_requested_at").
				Where("operation_id = ?", op.ID).Row().Scan(&requested); err != nil {
				return err
			}
			require.True(t, requested.Valid, "the marker must commit before BEGIN")
			// Explicitly distinct even if the database clock has coarse resolution.
			return witness.Raw(`UPDATE fleet_operation_runtime
				SET control_requested_at = clock_timestamp() + interval '1 second'
				WHERE operation_id = ? AND tenant_id = ? RETURNING control_requested_at`, op.ID, p.tenant).
				Row().Scan(&newer)
		})
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"dispatch": FleetDispatchPaused}, nil, nil
	})
	require.NoError(t, err)
	rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.Equal(t, newer, rt.ControlRequestedAt, "the older completion cannot clear the newer caller's marker")
	_, err = p.api.AbortFleetOperation(p.ctx, p.op.Token, alice.Name)
	require.NoError(t, err)
	rt, err = p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.False(t, rt.ControlRequestedAt.Valid, "a committed control clears its own marker")
}

func TestFleetPostgresAbandonedControlRequestExpiresOnDatabaseClock(t *testing.T) {
	p := newPostPG(t, "v1")
	p.api.PrincipalStanding = &fakeStanding{answer: map[string]bool{alice.Name: true}}
	db := p.api.RDB.DB(p.ctx)
	poster := db.Begin()
	require.NoError(t, poster.Error)
	defer poster.Rollback()
	require.NoError(t, fleetLock(poster, fleetOpKey(p.tenant, p.op.ID), true))
	restore := ShortenFleetFenceWaitForTest("100ms")
	t.Cleanup(restore)
	_, err := p.api.PauseFleetOperation(p.ctx, p.op.Token, alice.Name)
	requireFleetCode(t, err, FleetCodeFencePending)
	restore()
	require.NoError(t, poster.Rollback().Error)
	rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.True(t, rt.ControlRequestedAt.Valid, "FENCE_PENDING leaves the marker for the retry")
	oldClock := fleetClock
	fleetClock = func() time.Time {
		t.Error("PostgreSQL control freshness must never read the process clock")
		return time.Time{}
	}
	t.Cleanup(func() { fleetClock = oldClock })
	r, err := p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Zero(t, r.Posted)
	require.Equal(t, "CONTROL_PENDING", r.Hold)
	require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).
		Update("control_requested_at", gorm.Expr("clock_timestamp() - interval '29 seconds'")).Error)
	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Zero(t, r.Posted)
	require.Equal(t, "CONTROL_PENDING", r.Hold)
	// Advance marker age on the database clock, without a 30-second wall-clock sleep.
	require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).
		Update("control_requested_at", gorm.Expr("clock_timestamp() - interval '30 seconds'")).Error)
	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, 2, r.Posted)
	rt, err = p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.Empty(t, rt.HoldCode)
	require.False(t, rt.HoldSince.Valid)
}
