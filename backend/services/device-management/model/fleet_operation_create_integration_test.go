// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func fleetPGFixture(t *testing.T, devices int) (*Api, context.Context, string, []string) {
	t.Helper()
	openTheEngine(t)
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("fleetcreate%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 2*time.Minute)
	t.Cleanup(cancel)
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
	require.NoError(t, err)
	tokens := make([]string, devices)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("dev-%d", i)
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tokens[i], DeviceTypeToken: "sensor"})
		require.NoError(t, err)
	}
	return api, ctx, tenant, tokens
}

func TestConcurrentCreatesOfOneRequestYieldOneOperationAndOneSetOfLines(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 25)
	const callers = 8
	var wg sync.WaitGroup
	ids := make([]uint, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op, err := api.CreateFleetOperation(ctx, setReq("race", tokens...), "alice")
			errs[i] = err
			if op != nil {
				ids[i] = op.ID
			}
		}(i)
	}
	wg.Wait()
	for i := range errs {
		require.NoError(t, errs[i], "caller %d", i)
		require.Equal(t, ids[0], ids[i])
	}
	var ops, lines, contracts, events int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&ops).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationContract{}).Count(&contracts).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationEvent{}).Count(&events).Error)
	require.Equal(t, int64(1), ops)
	require.Equal(t, int64(len(tokens)), lines, "no line is written twice")
	require.Equal(t, int64(1), contracts)
	require.Equal(t, int64(1), events)
	op, err := api.FleetOperationByToken(ctx, "race")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, op.Dispatch)
}

func TestConcurrentCreatesOfDifferentRequestsUnderOneTokenAdmitExactlyOne(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 6)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = api.CreateFleetOperation(ctx, setReq("clash", tokens[:2+i*2]...), "alice")
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		requireFleetCode(t, err, FleetCodeConflict)
	}
	require.Equal(t, 1, won, "%v", errs)
	var ops int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&ops).Error)
	require.Equal(t, int64(1), ops)
}

func TestCreateAnswersAdmissionBusyWhenTheAdmissionLockIsHeldAndWritesNothing(t *testing.T) {
	api, ctx, tenant, tokens := fleetPGFixture(t, 3)
	holder := api.RDB.DB(ctx).Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	require.NoError(t, fleetLock(holder, fleetAdmissionTenantKey(tenant), false))

	started := time.Now()
	_, err := api.CreateFleetOperation(ctx, setReq("busy", tokens...), "alice")
	requireFleetCode(t, err, FleetCodeAdmissionBusy)
	require.Less(t, time.Since(started), 10*time.Second, "the wait is bounded by the 2s lock timeout")
	var ops int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Count(&ops).Error)
	require.Zero(t, ops, "a refused create leaves no header behind")

	// Negative control: with the lock free the same create is admitted.
	require.NoError(t, holder.Rollback().Error)
	_, err = api.CreateFleetOperation(ctx, setReq("busy", tokens...), "alice")
	require.NoError(t, err)
}

func TestPlanningStepWaitsForTheFenceAndNeverHoldsTheAdmissionLock(t *testing.T) {
	api, ctx, tenant, tokens := fleetPGFixture(t, 4)
	req := setReq("fenced", tokens...)
	n, err := normalizeFleetRequest(req)
	require.NoError(t, err)
	plan, err := api.planNormalized(ctx, n)
	require.NoError(t, err)
	stuck, err := api.fleetCreateStepOne(ctx, tenant, req, n, plan, "alice")
	require.NoError(t, err)

	// A poster holds the fence shared: step 2 (exclusive) must wait for it.
	poster := api.RDB.DB(ctx).Begin()
	require.NoError(t, poster.Error)
	defer poster.Rollback()
	require.NoError(t, fleetLock(poster, fleetOpKey(tenant, stuck.ID), true))

	done := make(chan error, 1)
	go func() {
		_, err := api.CreateFleetOperation(ctx, req, "alice")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("step 2 did not wait for the fence: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	// While it waits, it holds no admission lock: another tenant-wide create is not held up.
	other := setReq("while-waiting", tokens[:1]...)
	started := time.Now()
	_, err = api.CreateFleetOperation(ctx, other, "alice")
	require.NoError(t, err)
	require.Less(t, time.Since(started), 5*time.Second, "step 2 holds no admission lock while it waits for the fence")

	require.NoError(t, poster.Rollback().Error)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("step 2 did not finish after the fence was released")
	}
	op, err := api.FleetOperationByToken(ctx, "fenced")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, op.Dispatch)
	var lines int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Count(&lines).Error)
	require.Equal(t, int64(len(tokens)), lines)
}

func TestTwoFinishersOfOnePlanningHeaderWriteTheLinesOnce(t *testing.T) {
	api, ctx, tenant, tokens := fleetPGFixture(t, 30)
	req := setReq("finishers", tokens...)
	n, err := normalizeFleetRequest(req)
	require.NoError(t, err)
	plan, err := api.planNormalized(ctx, n)
	require.NoError(t, err)
	stuck, err := api.fleetCreateStepOne(ctx, tenant, req, n, plan, "alice")
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = api.fleetCreateStepTwo(ctx, tenant, stuck, plan, "alice")
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	var lines, contracts int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Count(&lines).Error)
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationContract{}).Count(&contracts).Error)
	require.Equal(t, int64(len(tokens)), lines)
	require.Equal(t, int64(1), contracts)
}

func TestCreateRunsAtReadCommittedBecauseTheLockProtocolDependsOnIt(t *testing.T) {
	api, ctx, _, _ := fleetPGFixture(t, 1)
	var level string
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Raw("SHOW transaction_isolation").Scan(&level).Error
	}))
	require.Equal(t, "read committed", level)
}
