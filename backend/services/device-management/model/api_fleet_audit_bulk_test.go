// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The pass that posts an operation's last line may not be the pass that finishes it: its replica can
// stop, or its finish can fail, between the last post and DISPATCHED. The next pass must then finish
// the operation whatever its failure and ambiguity counts, because a limit holds back lines still to
// be posted and none are. Paused instead, it could never be resumed (the counts never fall) and its
// only way out would be an abort that records a fully applied change as ABORTED.
func TestAFinishedOperationAtItsLimitIsDispatchedByTheNextPass(t *testing.T) {
	two := int32(2)
	one := int32(1)
	for name, c := range map[string]struct {
		safety      FleetSafetyRequest
		settlement  ledger.Settlement
		interrupted bool
		wantPaused  string
	}{
		// The boundary: the last line's verdict brings the count exactly to the limit.
		"failure limit reached exactly, finish interrupted":   {FleetSafetyRequest{FailureThreshold: &two}, ledger.SettlementFailed, true, ""},
		"failure limit exceeded, finish interrupted":          {FleetSafetyRequest{FailureThreshold: &one}, ledger.SettlementFailed, true, ""},
		"ambiguity limit reached exactly, finish interrupted": {FleetSafetyRequest{AmbiguityThreshold: &two}, ledger.SettlementAmbiguous, true, ""},
		// The normal path: the posting pass finishes the operation itself.
		"failure limit, finish not interrupted": {FleetSafetyRequest{FailureThreshold: &one}, ledger.SettlementFailed, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := driveFixture(t, 2, c.safety)
			db := e.api.RDB.DB(e.ctx)
			stopped := errors.New("the replica stopped before its finish committed")
			interrupt := c.interrupted
			const hook = "test:interrupt-finish"
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
				if ev, ok := tx.Statement.Dest.(*FleetOperationEvent); ok && interrupt && ev.Kind == fleetEventDispatched {
					_ = tx.AddError(stopped)
				}
			}))
			t.Cleanup(func() { require.NoError(t, db.Callback().Create().Remove(hook)) })

			r, err := e.api.DriveFleetOperation(e.ctx, "drv", DefaultFleetCaps)
			if c.interrupted {
				require.ErrorIs(t, err, stopped)
				require.Equal(t, 2, r.Posted)
				require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch, "the posting pass did not finish")
			} else {
				require.NoError(t, err)
				require.True(t, r.Dispatched)
			}
			require.Zero(t, e.count(t, FleetPostingPlanned))
			interrupt = false

			// The devices' verdicts arrive: the limit is reached with nothing left to post.
			require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", e.op.ID).
				Update("settlement", string(c.settlement)).Error)
			op := e.current(t)
			failures, err := fleetFailureCount(db, op)
			require.NoError(t, err)
			ambiguities, err := fleetAmbiguityCount(db, op)
			require.NoError(t, err)
			require.True(t, op.EffFailureThreshold.Valid && failures >= int64(op.EffFailureThreshold.Int32) ||
				op.EffAmbiguityThreshold.Valid && ambiguities >= int64(op.EffAmbiguityThreshold.Int32), "the fixture reaches its limit")

			e.now = e.now.Add(time.Second)
			r = e.drive(t)
			require.Empty(t, r.Paused, "a finished operation is never paused by a limit")
			op = e.current(t)
			require.Equal(t, FleetDispatchDispatched, op.Dispatch)
			require.Empty(t, op.PauseReason)
		})
	}
}

// The other side of the boundary: a limit reached while a line is still PLANNED pauses the operation
// before that line is posted, on a pass that finds it so.
func TestALimitReachedWithALineStillPlannedPauses(t *testing.T) {
	two := int32(2)
	e := driveFixture(t, 3, FleetSafetyRequest{FailureThreshold: &two})
	db := e.api.RDB.DB(e.ctx)
	var lines []FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", e.op.ID).Order("ordinal").Find(&lines).Error)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("id IN ?", []uint{lines[0].ID, lines[1].ID}).
		Updates(map[string]any{"posting": FleetPostingPosted, "settlement": string(ledger.SettlementFailed)}).Error)
	r := e.drive(t)
	require.Equal(t, FleetPauseFailureThreshold, r.Paused)
	require.Zero(t, r.Posted)
	require.Equal(t, int64(1), e.count(t, FleetPostingPlanned))
	require.Equal(t, FleetDispatchPaused, e.current(t).Dispatch)
}

// A hold an earlier epoch left never starts the resumed epoch's dependency clock. The resume clears it
// after its commit, but nothing that happens after the commit (the process stopping, a statement
// failing, a pass on another replica) may leave the resumed operation to be re-paused with
// DEPENDENCY_UNAVAILABLE by the old hold: the clock runs from the later of the hold and the resume.
func TestAResumeThatStopsAfterItsCommitDoesNotCarryTheOldHold(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	db := e.api.RDB.DB(e.ctx)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", e.op.ID).
		Updates(map[string]any{"attempts": 10, "last_error_code": "CONNECTION_LOST", "next_attempt_at": e.now.Add(96 * time.Hour)}).Error)
	require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", e.op.ID).
		Updates(map[string]any{"hold_code": FleetHoldPostError, "hold_since": e.now}).Error)
	e.now = e.now.Add(23 * time.Hour)
	require.Equal(t, FleetHoldPostError, e.drive(t).Hold)
	op, err := e.api.PauseFleetOperation(e.ctx, "drv", "operator")
	require.NoError(t, err)
	e.now = e.now.Add(2 * time.Hour) // the hold is now older than the 24 hours that pause an operation

	// Every runtime statement outside a transaction fails once the marker is published: the resume
	// commits and then nothing after its commit reaches the database.
	stopped := errors.New("the process stopped after the commit")
	stopping := true
	const hook = "test:stop-after-commit"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
		_, publish := tx.Statement.Clauses["RETURNING"]
		if stopping && !inTx && !publish && tx.Statement.Table == "fleet_operation_runtime" {
			_ = tx.AddError(stopped)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(hook) })
	resumedAt := e.now
	_, resumeErr := e.api.ResumeFleetOperation(e.ctx, op.Token, op.ContractDigest, nil, alice)
	stopping = false
	require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch, "the resume committed")
	held, err := e.api.FleetOperationRuntimeOf(e.ctx, e.current(t))
	require.NoError(t, err)
	require.Equal(t, FleetHoldPostError, held.HoldCode, "the fixture lost the resume's clear of the old hold")

	// No pass ran while the marker was fresh; it lapses, and the first pass sees the resumed epoch.
	e.now = e.now.Add(31 * time.Second)
	r := e.drive(t)
	require.Empty(t, r.Paused, "the resumed epoch gets its own 24 hours")
	require.Equal(t, FleetHoldPostError, r.Hold, "its lines are still failing")
	require.Equal(t, FleetDispatchRunning, e.current(t).Dispatch)
	require.NoError(t, resumeErr, "a committed resume is reported as done; a retry would be refused as illegal")

	// Its own 24 hours, and no longer: the dependency clock still runs for the resumed epoch.
	e.now = resumedAt.Add(24*time.Hour - time.Second)
	require.Empty(t, e.drive(t).Paused)
	e.now = resumedAt.Add(24 * time.Hour)
	require.Equal(t, FleetPauseDependency, e.drive(t).Paused)
	require.Equal(t, FleetDispatchPaused, e.current(t).Dispatch)
}
