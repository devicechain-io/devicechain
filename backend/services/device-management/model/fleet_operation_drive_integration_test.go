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
	now := time.Now()
	previousClock := fleetClock
	fleetClock = func() time.Time { return now }
	t.Cleanup(func() { fleetClock = previousClock })
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

	now = line.NextAttemptAt.Time.Add(-time.Millisecond)
	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Zero(t, r.Posted)
	require.Equal(t, int32(1), attempts.Load())
	attributeBeforeCommitHook = nil
	now = line.NextAttemptAt.Time.Add(time.Second)
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
}
