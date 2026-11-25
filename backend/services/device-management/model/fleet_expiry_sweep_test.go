// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedExpiryOperation(t *testing.T, api *Api, ctx context.Context, token, dispatch string, now time.Time) *FleetOperation {
	t.Helper()
	op := &FleetOperation{Token: token, Kind: "config.set", Name: token, Dispatch: dispatch,
		RequestDigest: "sha256:x", DriverProtocol: 1, Intent: "{}", TargetKind: "DEVICE_LIST",
		MaxPostsPerSecond: 10, EffPostsPerSecond: 10, SettleDeadlineS: 60, Initiator: "u",
		CreatedAt: now.Add(-time.Hour), ExpiresAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true}}
	require.NoError(t, api.RDB.DB(ctx).Create(op).Error)
	require.NoError(t, api.RDB.DB(ctx).Create(&FleetOperationRuntime{OperationId: int64(op.ID)}).Error)
	require.NoError(t, api.RDB.DB(ctx).Create(&FleetOperationLine{OperationId: int64(op.ID), Ordinal: 1, Posting: FleetPostingPlanned}).Error)
	return op
}

// Both callers must select the SAME due rows before either can transition them. SQLite serializes
// transactions through one connection; the integration variant exercises the real PostgreSQL fence.
func assertConcurrentFleetExpiry(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	now := time.Now()
	ops := []*FleetOperation{
		seedExpiryOperation(t, api, ctx, "ready-expiry", FleetDispatchReady, now),
		seedExpiryOperation(t, api, ctx, "planning-expiry", FleetDispatchPlanning, now),
	}
	var arrivals atomic.Int32
	selected := make(chan struct{})
	const hook = "test:expiry-selected"
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]FleetOperation); !ok {
			return
		}
		if arrivals.Add(1) == 2 {
			close(selected)
		}
		select {
		case <-selected:
		case <-time.After(5 * time.Second):
			tx.AddError(fmt.Errorf("both expiry callers did not select candidates"))
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove(hook) })
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			n, err := api.ExpireFleetOperations(ctx, now)
			done <- result{n, err}
		}()
	}
	total := 0
	for i := 0; i < 2; i++ {
		select {
		case r := <-done:
			require.NoError(t, r.err)
			total += r.n
		case <-time.After(15 * time.Second):
			t.Fatal("concurrent expiry did not finish")
		}
	}
	require.Equal(t, len(ops), total)
	for _, op := range ops {
		got, err := api.fleetOperationByToken(ctx, op.Token)
		require.NoError(t, err)
		require.Equal(t, FleetDispatchAborted, got.Dispatch)
		require.Equal(t, op.ExecutionEpoch+1, got.ExecutionEpoch)
		require.Equal(t, []string{fleetEventExpire}, events(t, api, ctx, op))
		var line FleetOperationLine
		require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&line).Error)
		require.Equal(t, FleetPostingCancelled, line.Posting)
	}
}

func TestConcurrentFleetExpiryTransitionsEachOperationOnce(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	assertConcurrentFleetExpiry(t, api, ctx)
}

func TestExpiryLeavesNewerProtocolsAlone(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now()
	op := seedExpiryOperation(t, api, ctx, "future", FleetDispatchReady, now)
	require.NoError(t, api.RDB.DB(ctx).Model(op).Update("driver_protocol", 2).Error)
	n, err := api.ExpireFleetOperations(ctx, now)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, events(t, api, ctx, op))
}

func TestFleetExpirySweepNewerProtocolsDoNotConsumeTenantBudget(t *testing.T) {
	api, _ := fleetFixture(t, 0)
	now := time.Now()
	for i := 0; i < 10; i++ {
		ctx := core.WithTenant(context.Background(), fmt.Sprintf("future%02d", i))
		op := seedExpiryOperation(t, api, ctx, "future", FleetDispatchReady, now)
		require.NoError(t, api.RDB.DB(ctx).Model(op).Update("driver_protocol", 2).Error)
	}
	ctx := core.WithTenant(context.Background(), "supported")
	op := seedExpiryOperation(t, api, ctx, "due", FleetDispatchReady, now)
	require.NoError(t, api.SweepExpiredFleetOperations(context.Background()))
	got, err := api.fleetOperationByToken(ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchAborted, got.Dispatch, "newer pods' operations must not starve this build's expiry work")
}

func TestFleetExpirySweepVisitsTenantsAndBoundsEachPass(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now()
	for i := 0; i < 101; i++ {
		seedExpiryOperation(t, api, ctx, fmt.Sprint(i), FleetDispatchReady, now)
	}
	other := core.WithTenant(context.Background(), "globex")
	seedExpiryOperation(t, api, other, "other", FleetDispatchPlanning, now)
	require.NoError(t, api.SweepExpiredFleetOperations(context.Background()))
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Where("dispatch = ?", FleetDispatchAborted).Count(&n).Error)
	require.Equal(t, int64(100), n, "one pass must not drain an unbounded backlog")
	require.NoError(t, api.RDB.DB(other).Model(&FleetOperation{}).Where("dispatch = ?", FleetDispatchAborted).Count(&n).Error)
	require.Equal(t, int64(1), n)
	require.NoError(t, api.SweepExpiredFleetOperations(context.Background()))
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Where("dispatch = ?", FleetDispatchReady).Count(&n).Error)
	require.Zero(t, n)
}

func TestFleetExpirySweepBoundsTenantDiscovery(t *testing.T) {
	api, _ := fleetFixture(t, 0)
	now := time.Now()
	for i := 0; i < 11; i++ {
		ctx := core.WithTenant(context.Background(), fmt.Sprintf("tenant%02d", i))
		seedExpiryOperation(t, api, ctx, "expired", FleetDispatchReady, now)
	}
	require.NoError(t, api.SweepExpiredFleetOperations(context.Background()))
	var n int64
	require.NoError(t, api.RDB.DB(core.WithSystemContext(context.Background())).Model(&FleetOperation{}).
		Where("dispatch = ?", FleetDispatchAborted).Count(&n).Error)
	require.Equal(t, int64(10), n)
	require.NoError(t, api.SweepExpiredFleetOperations(context.Background()))
	require.NoError(t, api.RDB.DB(core.WithSystemContext(context.Background())).Model(&FleetOperation{}).
		Where("dispatch = ?", FleetDispatchReady).Count(&n).Error)
	require.Zero(t, n)
}

func TestFleetExpirySweepHonorsCancellationAndReportsFailures(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	op := seedExpiryOperation(t, api, ctx, "due", FleetDispatchReady, time.Now())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, api.SweepExpiredFleetOperations(cancelled), context.Canceled)
	got, err := api.fleetOperationByToken(ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, got.Dispatch)
	// A failure of one tenant is a partial pass, not a successful no-op.
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&FleetOperationEvent{}))
	require.ErrorIs(t, api.SweepExpiredFleetOperations(context.Background()), core.ErrPassPartial)
	got, err = api.fleetOperationByToken(ctx, op.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, got.Dispatch, "a failed event append must roll back the expiry")
}

func TestFleetExpirySweepBoundsItsDeadline(t *testing.T) {
	api, _ := fleetFixture(t, 0)
	var remaining time.Duration
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:expiry-deadline", func(tx *gorm.DB) {
		if deadline, ok := tx.Statement.Context.Deadline(); ok {
			remaining = time.Until(deadline)
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Query().Remove("test:expiry-deadline") })
	require.NoError(t, api.SweepExpiredFleetOperations(context.Background()))
	require.Greater(t, remaining, 40*time.Second)
	require.LessOrEqual(t, remaining, 45*time.Second)
}

// A tenant whose expiry keeps failing must not hold one of the ten places in every pass: discovery
// resumes after the last tenant visited, so the eleventh tenant is reached on the next pass.
func TestFleetExpirySweepRotatesPastFailingTenants(t *testing.T) {
	api, _ := fleetFixture(t, 0)
	now := time.Now()
	for i := 0; i < 11; i++ {
		ctx := core.WithTenant(context.Background(), fmt.Sprintf("tenant%02d", i))
		seedExpiryOperation(t, api, ctx, "expired", FleetDispatchReady, now)
	}
	const hook = "test:expiry-poison"
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if ev, ok := tx.Statement.Dest.(*FleetOperationEvent); ok && ev.Kind == fleetEventExpire {
			if tenant, _ := core.TenantFromContext(tx.Statement.Context); tenant == "tenant10" {
				return
			}
			tx.AddError(fmt.Errorf("poisoned"))
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Create().Remove(hook) })
	require.Error(t, api.SweepExpiredFleetOperations(context.Background()))
	require.Error(t, api.SweepExpiredFleetOperations(context.Background()))
	last := core.WithTenant(context.Background(), "tenant10")
	got, err := api.fleetOperationByToken(last, "expired")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchAborted, got.Dispatch, "failing tenants must not starve the rest of the instance")
}

// One operation that keeps failing must not hold back the tenant's later operations.
func TestFleetExpiryContinuesPastAFailingOperation(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	now := time.Now()
	poison := seedExpiryOperation(t, api, ctx, "a-poison", FleetDispatchReady, now)
	next := seedExpiryOperation(t, api, ctx, "b-next", FleetDispatchReady, now)
	const hook = "test:expiry-poison-op"
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if ev, ok := tx.Statement.Dest.(*FleetOperationEvent); ok && ev.OperationId == int64(poison.ID) {
			tx.AddError(fmt.Errorf("poisoned"))
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Create().Remove(hook) })
	n, err := api.ExpireFleetOperations(ctx, now)
	require.Error(t, err)
	require.Equal(t, 1, n)
	got, err := api.fleetOperationByToken(ctx, next.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchAborted, got.Dispatch)
	got, err = api.fleetOperationByToken(ctx, poison.Token)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, got.Dispatch)
}
