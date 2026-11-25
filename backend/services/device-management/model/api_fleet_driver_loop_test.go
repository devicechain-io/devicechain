// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
)

func TestFleetDriverListingRotatesTenantsAndFiltersProtocol(t *testing.T) {
	e := driveFixture(t, 1, FleetSafetyRequest{})
	db := e.api.RDB.DB(core.WithSystemContext(e.ctx))
	require.NoError(t, db.Model(&FleetOperation{}).Where("id = ?", e.op.ID).Update("dispatch", FleetDispatchPaused).Error)
	for i := range 12 {
		ctx := core.WithTenant(context.Background(), fmt.Sprintf("tenant%02d", i))
		for j := range 12 {
			op := FleetOperation{Token: fmt.Sprintf("op%02d", j), Dispatch: FleetDispatchRunning, DriverProtocol: 1}
			require.NoError(t, e.api.RDB.DB(ctx).Create(&op).Error)
			require.NoError(t, e.api.RDB.DB(ctx).Create(&FleetOperationRuntime{OperationId: int64(op.ID)}).Error)
		}
	}
	for _, protocol := range []int32{2} {
		op := FleetOperation{Token: fmt.Sprintf("newer%d", protocol), Dispatch: FleetDispatchRunning, DriverProtocol: protocol}
		require.NoError(t, e.api.RDB.DB(core.WithTenant(e.ctx, "aaa")).Create(&op).Error)
	}
	first, err := e.api.fleetDriverCandidates(e.ctx)
	require.NoError(t, err)
	require.Len(t, first, 10)
	for i, op := range first {
		require.Equal(t, fmt.Sprintf("tenant%02d", i), op.TenantID)
		require.NoError(t, db.Model(&FleetOperationRuntime{}).
			Where("operation_id IN (?)", db.Model(&FleetOperation{}).Select("id").Where("tenant_id = ? AND token = ?", op.TenantID, op.Token)).
			Update("driver_seen_at", e.now).Error)
	}
	second, err := e.api.fleetDriverCandidates(e.ctx)
	require.NoError(t, err)
	require.Len(t, second, 10)
	require.Equal(t, "tenant10", second[0].TenantID)
	require.Equal(t, "tenant11", second[1].TenantID)
	require.Equal(t, "op01", second[2].Token, "a held operation cannot starve its tenant's other operation")
}

func TestFleetDriverPassUsesTenantContextAndHonoursCancellation(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	require.NoError(t, e.api.RunFleetDriver(context.Background()))
	require.Equal(t, FleetDispatchDispatched, e.current(t).Dispatch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, e.api.RunFleetDriver(ctx), context.Canceled)
}

func TestFleetSkippedHasItsOwnTallyAndDoesNotCountAsFailure(t *testing.T) {
	fail := int32(1)
	e := driveFixture(t, 2, FleetSafetyRequest{FailureThreshold: &fail})
	// Model a previous-release deletion, which has no erasure hook.
	require.NoError(t, e.api.RDB.DB(e.ctx).Where("id = ?", e.devs[0].ID).Delete(&Device{}).Error)
	r := e.drive(t)
	require.Equal(t, 1, r.Skipped)
	require.Zero(t, r.Refused)
	require.Equal(t, 1, r.Posted)
	n, err := fleetFailureCount(e.api.RDB.DB(e.ctx), e.op)
	require.NoError(t, err)
	require.Zero(t, n)
	require.True(t, r.Dispatched)
}

func TestFleetDependencyPauseAfter24HoursOfRetryFailures(t *testing.T) {
	for _, code := range []string{"POST_ERROR", "CONNECTION_LOST", "STATEMENT_TIMEOUT"} {
		t.Run(code, func(t *testing.T) {
			e := driveFixture(t, 2, FleetSafetyRequest{})
			db := e.api.RDB.DB(e.ctx)
			require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", e.op.ID).
				Updates(map[string]any{"attempts": 10, "last_error_code": code, "next_attempt_at": e.now.Add(48 * time.Hour)}).Error)
			require.NoError(t, db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", e.op.ID).
				Updates(map[string]any{"hold_code": "POST_ERROR", "hold_since": e.now}).Error)
			e.now = e.now.Add(24*time.Hour - time.Second)
			r := e.drive(t)
			require.Empty(t, r.Paused)
			require.Equal(t, "POST_ERROR", r.Hold)
			e.now = e.now.Add(time.Second)
			r = e.drive(t)
			require.Equal(t, "DEPENDENCY_UNAVAILABLE", r.Paused)
			require.Equal(t, FleetDispatchPaused, e.current(t).Dispatch)
			require.Equal(t, int64(2), e.count(t, string(ledger.PostingPlanned)))
		})
	}
}

func TestFleetDriverSharesFourPostingSlotsAndCancelsWaiters(t *testing.T) {
	e := driveFixture(t, 2, FleetSafetyRequest{})
	for range 4 {
		release, err := e.api.acquireFleetDriverPost(context.Background())
		require.NoError(t, err)
		defer release()
	}
	ctx, cancel := context.WithTimeout(e.ctx, 100*time.Millisecond)
	defer cancel()
	_, err := e.api.DriveFleetOperation(ctx, e.op.Token, DefaultFleetCaps)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, int64(2), e.count(t, FleetPostingPlanned), "every operation uses the replica's slots")
	require.Zero(t, e.count(t, FleetPostingPosted))
}
