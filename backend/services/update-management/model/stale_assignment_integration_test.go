// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Uses the migration-backed PostgreSQL harness (DC_IT_PGPORT / DC_IT_PGHOST).
func TestAssignArtifactRefusesPausedOlderIncarnationOnPostgres(t *testing.T) {
	e := newPGRollouts(t, 1)
	api := e.replica()
	err := exerciseStaleAssignment(t, api, e.ctx(), "dev-0", "fw-2", func(ctx context.Context) error {
		_, err := api.AssignArtifact(ctx, "dev-0", "fw-2")
		return err
	})
	requireRefusal(t, err, "DEVICE_REPLACED")
}

func TestRolloutAdoptionWaitsForConcurrentSupersedeOnPostgres(t *testing.T) {
	e := newPGRollouts(t, 1)
	a := e.replica()
	ctx, cancel := context.WithTimeout(e.ctx(), 30*time.Second)
	defer cancel()
	staleFacts := DeviceFacts{Incarnation: 17, Profile: "gateway"}
	a.Devices = deviceLookupFunc(func(context.Context, string, string) (DeviceFacts, bool, error) {
		return staleFacts, true, nil
	})
	old, err := a.AssignArtifact(ctx, "dev-0", "fw-2")
	require.NoError(t, err)
	_, err = a.CreateRollout(ctx, RolloutCreateRequest{Token: "r1", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 1})
	require.NoError(t, err)
	a.Devices = &syncDevices{err: errors.New("hold inline dispatch")}
	r, err := a.StartRollout(ctx, "r1")
	require.NoError(t, err)
	a.Devices = deviceLookupFunc(func(context.Context, string, string) (DeviceFacts, bool, error) {
		return staleFacts, true, nil
	})

	// Hold the hand assignment's supersede uncommitted: dispatch must wait on the old row,
	// then recheck currentness instead of binding the rollout to the superseded assignment.
	db := a.RDB.DB(ctx)
	hand := db.Begin()
	require.NoError(t, hand.Error)
	defer hand.Rollback()
	var handPID int
	require.NoError(t, hand.Raw("SELECT pg_backend_pid()").Scan(&handPID).Error)
	replacement, err := a.assignInTx(ctx, hand, "dev-0", &old.Artifact, DeviceFacts{Incarnation: 23, Profile: "gateway"})
	require.NoError(t, err)
	type outcome struct {
		result RolloutStepResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := a.StepRollout(ctx, r.Rollout.ID)
		done <- outcome{res, err}
	}()
	// Observe the actual server lock wait, avoiding a scheduler-dependent sleep assertion.
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	waiting := false
	for !waiting {
		select {
		case got := <-done:
			t.Fatalf("dispatch returned before supersede committed: %+v, %v", got.result, got.err)
		case <-deadline:
			t.Fatal("dispatch never waited for the superseding transaction")
		case <-tick.C:
			var blockers int64
			// Server lock metadata only; no tenant data is read by this diagnostic query.
			require.NoError(t, db.Raw("SELECT count(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))", handPID).
				Scan(&blockers).Error)
			waiting = blockers > 0
		}
	}
	require.NoError(t, hand.Commit().Error)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Zero(t, got.result.Dispatched)
		require.Equal(t, 1, got.result.Skipped)
	case <-ctx.Done():
		t.Fatal("dispatch did not resume after supersede committed")
	}
	targets, err := a.RolloutTargets(ctx, "r1", RolloutTargetCriteria{Pagination: paginationAll()})
	require.NoError(t, err)
	require.Len(t, targets.Results, 1)
	target := targets.Results[0].Target
	require.Equal(t, TargetSkipped, target.Status)
	require.Equal(t, CodeDeviceReplaced, target.SkipCode)
	require.False(t, target.AssignmentId.Valid, "never bind the superseded assignment")
	current, err := a.AssignmentByToken(ctx, replacement.Assignment.Token)
	require.NoError(t, err)
	require.False(t, current.Assignment.SupersededAt.Valid)
}
