// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetPostgresUndecidedErrorDoesNotAbortThePass(t *testing.T) {
	p := newPostPG(t, "v1")
	p.api.PrincipalStanding = &fakeStanding{answer: map[string]bool{alice.Name: true}}
	now := fleetPGNow(t, p.api.RDB.DB(p.ctx))
	undecided := errors.New("undecided posting failure")
	var attempts atomic.Int32
	attributeBeforeCommitHook = func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&FleetOperationLine{}).Where("id = ? AND posting = ?", p.lines[0].ID, FleetPostingPosted).Count(&n).Error; err != nil {
			return err
		}
		if n == 1 {
			attempts.Add(1)
			return undecided
		}
		return nil
	}
	t.Cleanup(func() { attributeBeforeCommitHook = nil })

	r, err := p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.ErrorIs(t, err, undecided)
	require.Equal(t, 1, r.Posted)
	require.Equal(t, "POST_ERROR", r.Hold)
	require.Equal(t, FleetPostingPosted, p.posting(t, 1))
	var line FleetOperationLine
	require.NoError(t, p.api.RDB.DB(p.ctx).First(&line, p.lines[0].ID).Error)
	require.Equal(t, FleetPostingPlanned, line.Posting)
	require.Equal(t, "POST_ERROR", line.LastErrorCode)
	require.Equal(t, int32(1), line.Attempts)
	require.True(t, line.NextAttemptAt.Valid)
	require.True(t, line.NextAttemptAt.Time.After(now))
	rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.True(t, rt.DriverSeenAt.Valid)
	require.True(t, rt.LastProgressAt.Valid)
	require.Equal(t, "POST_ERROR", rt.HoldCode)

	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Zero(t, r.Posted)
	require.Equal(t, int32(1), attempts.Load())
	attributeBeforeCommitHook = nil
	require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationLine{}).Where("id = ?", line.ID).
		Update("next_attempt_at", gorm.Expr("clock_timestamp() - interval '1 second'")).Error)
	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, 1, r.Posted)
	require.True(t, r.Dispatched)
}

func TestFleetPostgresConnectionLossRecordsARetry(t *testing.T) {
	p := newPostPG(t, "v1")
	attributeBeforeCommitHook = func(tx *gorm.DB) error {
		// Lose the actual posting connection before commit; the pool must record the retry on another one.
		return tx.Exec("SELECT pg_terminate_backend(pg_backend_pid())").Error
	}
	t.Cleanup(func() { attributeBeforeCommitHook = nil })
	r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPostRetry, r.Outcome)
	require.Equal(t, "CONNECTION_LOST", r.Code)
	var line FleetOperationLine
	require.NoError(t, p.api.RDB.DB(p.ctx).First(&line, p.lines[0].ID).Error)
	require.Equal(t, FleetPostingPlanned, line.Posting)
	require.Equal(t, "CONNECTION_LOST", line.LastErrorCode)
	require.Equal(t, int32(1), line.Attempts)
	require.True(t, line.NextAttemptAt.Valid)
	require.Zero(t, p.attrCount(t, 0))
	// The operation reports it as the last error the engine met, stamped on the database clock.
	rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.Equal(t, "CONNECTION_LOST", rt.LastErrorCode)
	require.True(t, rt.LastErrorAt.Valid)
	require.WithinDuration(t, fleetPGNow(t, p.api.RDB.DB(p.ctx)), rt.LastErrorAt.Time, time.Minute)
}

func TestFleetPostgresABusyHeadDoesNotStarveTheNextDueLine(t *testing.T) {
	for _, code := range []string{"DEVICE_BUSY", "ATTRIBUTE_ROW_BUSY"} {
		t.Run(code, func(t *testing.T) {
			p := newPostPG(t, "v1")
			p.api.PrincipalStanding = &fakeStanding{answer: map[string]bool{alice.Name: true}}
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperation{}).Where("id = ?", p.op.ID).
				Update("eff_posts_per_second", 1).Error)
			// The head has already failed once, so the next generated backoff spans two seconds
			// even with zero jitter: there is a whole fresh permit window before it is due.
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationLine{}).Where("id = ?", p.lines[0].ID).
				Update("attempts", 1).Error)
			if code == "ATTRIBUTE_ROW_BUSY" {
				value := "before"
				_, err := p.api.SetEntityAttribute(p.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: p.devs[0].Token,
					Scope: "SHARED", AttrKey: "k", ValueType: "STRING", Value: &value})
				require.NoError(t, err)
			}
			holder := p.api.RDB.DB(p.ctx).Begin()
			require.NoError(t, holder.Error)
			defer holder.Rollback()
			if code == "DEVICE_BUSY" {
				require.NoError(t, holder.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, p.devs[0].ID).Error)
			} else {
				// A previous-release writer holds only the row, without the device advisory lock.
				require.NoError(t, holder.Exec(`SELECT id FROM entity_attributes
					WHERE entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k' FOR UPDATE`, p.devs[0].ID).Error)
			}
			now := fleetPGNow(t, p.api.RDB.DB(p.ctx))
			r, err := p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
			require.NoError(t, err)
			require.Equal(t, 1, r.Busy+r.Retried)
			require.Zero(t, r.Posted)
			var head FleetOperationLine
			require.NoError(t, p.api.RDB.DB(p.ctx).First(&head, p.lines[0].ID).Error)
			require.Equal(t, FleetPostingPlanned, head.Posting)
			require.Equal(t, int32(2), head.Attempts)
			require.Equal(t, code, head.LastErrorCode)
			require.True(t, head.NextAttemptAt.Valid)
			require.True(t, head.NextAttemptAt.Time.After(now))
			// The generated backoff spans a new permit window. Keep holding the real lock: only
			// selection excluding the head lets the second line use that window's sole permit.
			// Move the stored window, not a process clock: the head is still
			// backing off when the next DB permit window begins.
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).
				Update("pace_window_start", gorm.Expr("clock_timestamp() - interval '1 second'")).Error)
			r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
			require.NoError(t, err)
			require.Equal(t, 1, r.Posted)
			require.Zero(t, r.Busy+r.Retried)
			require.Equal(t, FleetPostingPosted, p.posting(t, 1))
			// With only the backed-off head left, another pass must spend no permit.
			before, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
			require.NoError(t, err)
			r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
			require.NoError(t, err)
			require.Zero(t, r.Posted+r.Busy+r.Retried)
			after, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
			require.NoError(t, err)
			require.Equal(t, before.PaceIssued, after.PaceIssued)
			require.Equal(t, before.PaceWindowStart, after.PaceWindowStart)
			require.NoError(t, holder.Rollback().Error)
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationLine{}).Where("id = ?", head.ID).
				Update("next_attempt_at", gorm.Expr("clock_timestamp() - interval '1 second'")).Error)
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).
				Update("pace_window_start", gorm.Expr("clock_timestamp() - interval '1 second'")).Error)
			r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
			require.NoError(t, err)
			require.Equal(t, 1, r.Posted)
			require.True(t, r.Dispatched)
		})
	}
}
