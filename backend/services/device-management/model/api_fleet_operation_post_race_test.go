// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// cancelFromInside plays a competing session that cancelled the line: inside the posting transaction, at
// the named stage, the line becomes CANCELLED.
func cancelFromInside(e *postEnv, stage string) {
	fleetPostHook = func(s string, tx *gorm.DB) {
		if s == stage {
			tx.Model(&FleetOperationLine{}).Where("id = ?", e.line.ID).Update("posting", FleetPostingCancelled)
		}
	}
}

func TestALineCancelledAfterTheUnlockedReadIsCaughtBeforeAnyEffect(t *testing.T) {
	e := postFixture(t, "v1", false)
	effects := 0
	fleetEffectHook = func() { effects++ }
	cancelFromInside(e, "read")
	t.Cleanup(func() { fleetEffectHook, fleetPostHook = nil, nil })
	require.Equal(t, FleetPostLostRace, e.post(t).Outcome)
	require.Zero(t, effects, "the re-read under the device lock stops the post before it writes anything")
	_, ok := attrOf(t, e)
	require.False(t, ok)
}

func TestALineClaimedByAnotherSessionRollsTheWholeEffectBack(t *testing.T) {
	e := postFixture(t, "v1", false)
	cancelFromInside(e, "claim")
	t.Cleanup(func() { fleetPostHook = nil })
	require.Equal(t, FleetPostLostRace, e.post(t).Outcome)
	_, ok := attrOf(t, e)
	require.False(t, ok, "the claim was the last statement and it failed, so the effect did not commit")
	var revs int64
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", e.dev.ID).Count(&revs).Error)
	require.Zero(t, revs, "and no revision was minted for it")
}

func TestALeaseThatLapsesBetweenTheClaimAndTheCommitRollsBack(t *testing.T) {
	e := postFixture(t, "v1", false)
	e.lease.Deadline = time.Now().Add(1200 * time.Millisecond)
	fleetPostHook = func(s string, tx *gorm.DB) {
		if s == "claimed" {
			time.Sleep(1300 * time.Millisecond)
		}
	}
	t.Cleanup(func() { fleetPostHook = nil })
	require.Equal(t, FleetPostLeaseExpired, e.post(t).Outcome)
	_, ok := attrOf(t, e)
	require.False(t, ok)
	require.Equal(t, FleetPostingPlanned, e.reload(t).Posting)
}

func TestARefusalNeverOverwritesALineThatIsNotPlanned(t *testing.T) {
	e := postFixture(t, "v1", false)
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	r, err := e.api.recordFleetRefusal(e.ctx, "acme", e.op.ID, e.line.ID, e.lease, fleetDecision{FleetPostingRefused, "X", "late"})
	require.NoError(t, err)
	require.Equal(t, FleetPostLostRace, r.Outcome)
	l := e.reload(t)
	require.Equal(t, FleetPostingPosted, l.Posting)
	require.Empty(t, l.Code)
}

type sqlStateError string

func (e sqlStateError) Error() string    { return "sql error " + string(e) }
func (e sqlStateError) SQLState() string { return string(e) }

func TestEveryTransientDatabaseFailureIsRetriedAndNothingElseIs(t *testing.T) {
	for state, code := range map[string]string{"40P01": "DEADLOCK", "40001": "SERIALIZATION", "55P03": "LOCK_TIMEOUT", "57014": "STATEMENT_TIMEOUT"} {
		got, ok := fleetRetryableCode(sqlStateError(state))
		require.True(t, ok, state)
		require.Equal(t, code, got)
	}
	for _, state := range []string{"23505", "23503", "42P01", "XX000", ""} {
		_, ok := fleetRetryableCode(sqlStateError(state))
		require.False(t, ok, "SQLSTATE %q is a defect, not a transient", state)
	}
}

func TestALeaseWithNoDeadlineIsRefused(t *testing.T) {
	e := postFixture(t, "v1", false)
	e.lease.Deadline = time.Time{}
	require.Equal(t, FleetPostLeaseExpired, e.post(t).Outcome, "a lease that bounds nothing is not a lease")
	_, ok := attrOf(t, e)
	require.False(t, ok)
}
