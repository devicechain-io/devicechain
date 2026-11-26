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
	"gorm.io/gorm/clause"
)

// On PostgreSQL every control stamp and every expiry judgment is the database's clock, read inside
// the transition's own transaction once the operation row is locked. This process's fleet clock is
// poisoned a day ahead and must never be read; each stamp must fall between two readings of the
// database clock taken around the call, and the pause, queued behind another holder of the row,
// must be stamped after that holder let go.
func TestFleetPostgresControlStampsAndJudgesOnTheDatabaseClock(t *testing.T) {
	p := newPostPG(t, "v1")
	api, ctx := p.api, p.ctx
	db := api.RDB.DB(ctx)
	previous := fleetClock
	fleetClock = func() time.Time {
		t.Error("PostgreSQL control transitions must never read the process clock")
		return time.Now().Add(24 * time.Hour)
	}
	t.Cleanup(func() { fleetClock = previous })
	dbNow := func() time.Time {
		t.Helper()
		var now time.Time
		require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&now).Error)
		return now
	}
	reload := func(op *FleetOperation) *FleetOperation {
		t.Helper()
		var cur FleetOperation
		require.NoError(t, db.First(&cur, op.ID).Error)
		return &cur
	}
	within := func(from, to time.Time, got sql.NullTime, what string) {
		t.Helper()
		require.True(t, got.Valid, what)
		require.False(t, got.Time.Before(from), "%s %s is before the call began (%s)", what, got.Time, from)
		require.False(t, got.Time.After(to), "%s %s is after the call returned (%s)", what, got.Time, to)
	}
	event := func(op *FleetOperation, kind string) time.Time {
		t.Helper()
		var ev FleetOperationEvent
		require.NoError(t, db.Where("operation_id = ? AND kind = ?", op.ID, kind).Order("id DESC").First(&ev).Error)
		return ev.At
	}

	// The pause queues behind another transaction holding the operation row.
	holder := db.Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	var held FleetOperation
	require.NoError(t, holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&held, p.op.ID).Error)
	paused := make(chan error, 1)
	go func() {
		_, err := api.PauseFleetOperation(ctx, p.op.Token, alice.Name)
		paused <- err
	}()
	time.Sleep(300 * time.Millisecond)
	released := dbNow()
	require.NoError(t, holder.Commit().Error)
	require.NoError(t, <-paused)
	to := dbNow()
	cur := reload(p.op)
	require.Equal(t, FleetDispatchPaused, cur.Dispatch)
	within(released, to, cur.StateChangedAt, "pause state_changed_at")
	within(released, to, sql.NullTime{Time: event(p.op, fleetEventPause), Valid: true}, "PAUSE event")
	require.True(t, cur.StateChangedAt.Time.Equal(event(p.op, fleetEventPause)), "the event and the header carry one reading")

	from := dbNow()
	bob := FleetApprover{Name: "bob@example.com", Authorities: alice.Authorities}
	_, err := api.ResumeFleetOperation(ctx, p.op.Token, p.op.ContractDigest, nil, bob)
	require.NoError(t, err)
	to = dbNow()
	cur = reload(p.op)
	within(from, to, cur.StateChangedAt, "handover state_changed_at")
	within(from, to, cur.GrantBoundAt, "handover grant_bound_at")

	// A start and the sweep judge expiry on the database clock; created stamps are its too.
	from = dbNow()
	value := "v2"
	stale, err := api.CreateFleetOperation(ctx, &FleetOperationCreateRequest{Token: "stale", Name: "n", Kind: FleetKindConfigSet,
		DeviceTokens: []string{p.devs[0].Token}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &value}}, "creator")
	require.NoError(t, err)
	cleanupFleetTestOperation(t, api, ctx, stale.Token)
	to = dbNow()
	within(from.Add(7*24*time.Hour), to.Add(7*24*time.Hour), reload(stale).ExpiresAt, "expires_at")
	require.NoError(t, db.Model(&FleetOperation{ID: stale.ID}).Update("expires_at", gorm.Expr("clock_timestamp() - interval '1 second'")).Error)
	_, err = api.StartFleetOperation(ctx, "stale", stale.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeExpired)
	from = dbNow()
	n, err := api.ExpireFleetOperations(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	to = dbNow()
	cur = reload(stale)
	require.Equal(t, FleetDispatchAborted, cur.Dispatch)
	within(from, to, cur.StateChangedAt, "expired state_changed_at")
	within(from, to, cur.DispatchedAt, "expired dispatched_at")
	within(from, to, cur.ClosedAt, "expired closed_at")
	require.NoError(t, api.SweepExpiredFleetOperations(ctx))

	from = dbNow()
	_, err = api.AbortFleetOperation(ctx, p.op.Token, bob.Name)
	require.NoError(t, err)
	to = dbNow()
	cur = reload(p.op)
	within(from, to, cur.StateChangedAt, "abort state_changed_at")
	within(from, to, cur.DispatchedAt, "abort dispatched_at")
}
