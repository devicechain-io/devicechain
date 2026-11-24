// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These run the transitions on a real server with the REAL activation check: the chain installs the
// attribute guard, so a start is admitted only because the guard is provably in place.

func TestFleetOperationStartsOnlyWhenTheGuardIsInPlace(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 3)
	op, err := api.CreateFleetOperation(ctx, setReq("go", tokens...), "alice")
	require.NoError(t, err)
	started, err := api.StartFleetOperation(ctx, "go", op.ContractDigest, alice)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchRunning, started.Dispatch)
}

func TestPauseWaitsForAPosterInFlightAndAnswersFencePendingWhenItCannotDrain(t *testing.T) {
	api, ctx, tenant, tokens := fleetPGFixture(t, 3)
	op, err := api.CreateFleetOperation(ctx, setReq("fence", tokens...), "alice")
	require.NoError(t, err)
	op, err = api.StartFleetOperation(ctx, "fence", op.ContractDigest, alice)
	require.NoError(t, err)

	// A poster holds the fence shared, as a posting transaction does from its first statement.
	poster := api.RDB.DB(ctx).Begin()
	require.NoError(t, poster.Error)
	defer poster.Rollback()
	require.NoError(t, fleetLock(poster, fleetOpKey(tenant, op.ID), true))

	// With a short wait, the pause cannot drain: FENCE_PENDING, and nothing has changed.
	restore := ShortenFleetFenceWaitForTest("400ms")
	started := time.Now()
	_, err = api.PauseFleetOperation(ctx, "fence", alice.Name)
	restore()
	requireFleetCode(t, err, FleetCodeFencePending)
	require.Less(t, time.Since(started), 10*time.Second)
	still, err := api.fleetOperationByToken(ctx, "fence")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchRunning, still.Dispatch)
	require.Equal(t, op.ExecutionEpoch, still.ExecutionEpoch)

	// With the normal wait, the pause waits for the poster and then lands, once it has finished.
	done := make(chan error, 1)
	go func() {
		_, err := api.PauseFleetOperation(ctx, "fence", alice.Name)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("pause returned while a poster held the fence: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	require.NoError(t, poster.Rollback().Error)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the pause did not land after the poster finished")
	}
	paused, err := api.fleetOperationByToken(ctx, "fence")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchPaused, paused.Dispatch)
	require.Equal(t, op.ExecutionEpoch+1, paused.ExecutionEpoch)
}

func TestARacingStartAndAbortLeaveOneCoherentState(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 3)
	for round := 0; round < 6; round++ {
		token := fmt.Sprintf("race%d", round)
		op, err := api.CreateFleetOperation(ctx, setReq(token, tokens...), "alice")
		require.NoError(t, err)
		var wg sync.WaitGroup
		var startErr, abortErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, startErr = api.StartFleetOperation(ctx, token, op.ContractDigest, alice) }()
		go func() { defer wg.Done(); _, abortErr = api.AbortFleetOperation(ctx, token, alice.Name) }()
		wg.Wait()
		got, err := api.fleetOperationByToken(ctx, token)
		require.NoError(t, err)
		p, err := api.FleetOperationProgressOf(ctx, got)
		require.NoError(t, err)
		switch got.Dispatch {
		case FleetDispatchAborted:
			// Either the abort came first (the start was refused) or the start then the abort ran.
			require.Zero(t, p.Planned, "an aborted operation has no planned line")
			if startErr != nil {
				requireFleetCode(t, startErr, FleetCodeIllegalTransition)
			}
			require.NoError(t, abortErr)
		case FleetDispatchRunning:
			// The start won and the abort was refused? Never: abort accepts a RUNNING operation, so
			// reaching here means the abort lost to nothing. It must be impossible.
			t.Fatalf("round %d: both returned and the operation is RUNNING: start=%v abort=%v", round, startErr, abortErr)
		default:
			t.Fatalf("round %d: unexpected state %s", round, got.Dispatch)
		}
	}
}

func TestConcurrentStartsAdmitExactlyTheDispatchingBudget(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 2)
	const ops = 5
	digests := make([]string, ops)
	for i := 0; i < ops; i++ {
		op, err := api.CreateFleetOperation(ctx, setReq(fmt.Sprintf("q%d", i), tokens...), "alice")
		require.NoError(t, err)
		digests[i] = op.ContractDigest
	}
	var wg sync.WaitGroup
	errs := make([]error, ops)
	for i := 0; i < ops; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = api.StartFleetOperation(ctx, fmt.Sprintf("q%d", i), digests[i], alice)
		}(i)
	}
	wg.Wait()
	started := 0
	for _, err := range errs {
		if err == nil {
			started++
			continue
		}
		requireFleetCode(t, err, FleetCodeQuotaExceeded)
	}
	require.Equal(t, 2, started, "two replicas cannot both admit the last slot: %v", errs)
}

func TestARacingDoublePauseAndDoubleStartTransitionOnce(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 2)
	op, err := api.CreateFleetOperation(ctx, setReq("twice", tokens...), "alice")
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = api.StartFleetOperation(ctx, "twice", op.ContractDigest, alice)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			requireFleetCode(t, err, FleetCodeIllegalTransition)
		}
	}
	require.Equal(t, 1, won)
	got, err := api.fleetOperationByToken(ctx, "twice")
	require.NoError(t, err)
	require.Equal(t, op.ExecutionEpoch+1, got.ExecutionEpoch, "one transition, one epoch step")
	require.Equal(t, []string{"CREATE", "START"}, events(t, api, ctx, op))
}
