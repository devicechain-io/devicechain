// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestFleetExpirySweepServiceLifecycle(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	sweep, err := os.ReadFile("fleet_expiry_sweep.go")
	require.NoError(t, err)
	require.Contains(t, string(sweep), "Api.SweepExpiredFleetOperations")
	for _, path := range []string{"main.go", "fleet_expiry_sweep.go", "model/api_fleet_expiry_sweep.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					require.NotEqual(t, "DriveFleetOperation", selector.Sel.Name, "expiry must not start the driver")
				}
			}
			return true
		})
	}
	for _, call := range []string{"initFleetExpirySweep()", "FleetExpirySweep.Start(ctx)", "FleetExpirySweep.Stop(ctx)", "FleetExpirySweep.Terminate(ctx)"} {
		if !strings.Contains(string(source), call) {
			t.Errorf("service lifecycle is missing %s", call)
		}
	}
	// The database primitive's contention/release semantics are tested in core and in the
	// integration suite. Pin its use here so a SQLite run can also detect losing the HA wiring.
	sweep, err = os.ReadFile("model/api_fleet_expiry_sweep.go")
	require.NoError(t, err)
	require.Contains(t, string(sweep), "api.RDB.SingletonPass(ctx, fleetExpirySweepLockName, pass)")
}

func TestFleetExpirySweepIntervalAndShutdown(t *testing.T) {
	require.Equal(t, time.Minute, fleetExpirySweepInterval)
	ms := &core.Microservice{FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	var calls atomic.Int32
	entered := make(chan struct{}, 10)
	cancelled := make(chan struct{})
	task := newFleetExpirySweep(ms, 20*time.Millisecond, func(ctx context.Context) error {
		n := calls.Add(1)
		entered <- struct{}{}
		if n < 3 {
			return errors.New("expiry failed")
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = task.Stop(context.Background())
		}
	})
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("expiry did not run on its interval")
		}
	}
	require.NoError(t, task.Stop(context.Background()))
	stopped = true
	select {
	case <-cancelled:
	default:
		t.Fatal("shutdown did not cancel and await the in-flight expiry pass")
	}
	select {
	case <-entered:
		t.Fatal("expiry ran after shutdown")
	case <-time.After(80 * time.Millisecond):
	}
	require.Equal(t, int32(3), calls.Load())
	require.NoError(t, task.Terminate(context.Background()))
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP devicechain_devicemanagement_fleet_expiry_sweep_passes_total Passes made by the fleet_expiry_sweep task, by outcome.
# TYPE devicechain_devicemanagement_fleet_expiry_sweep_passes_total counter
devicechain_devicemanagement_fleet_expiry_sweep_passes_total{outcome="cancelled"} 1
devicechain_devicemanagement_fleet_expiry_sweep_passes_total{outcome="complete"} 0
devicechain_devicemanagement_fleet_expiry_sweep_passes_total{outcome="failed"} 2
devicechain_devicemanagement_fleet_expiry_sweep_passes_total{outcome="partial"} 0
devicechain_devicemanagement_fleet_expiry_sweep_passes_total{outcome="skipped"} 0
`), "devicechain_devicemanagement_fleet_expiry_sweep_passes_total"))
}

func TestFleetExpiryTaskCallsRealExpiryRepeatedly(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.FleetOperation{}, &model.FleetOperationRuntime{}, &model.FleetOperationLine{}, &model.FleetOperationEvent{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "expiry")
	seed := func(token string) uint {
		op := &model.FleetOperation{Token: token, Kind: "config.set", Name: token,
			Dispatch: model.FleetDispatchReady, DriverProtocol: 1,
			ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true}}
		require.NoError(t, db.WithContext(ctx).Create(op).Error)
		require.NoError(t, db.WithContext(ctx).Create(&model.FleetOperationRuntime{OperationId: int64(op.ID)}).Error)
		return op.ID
	}
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	first := seed("first")
	task := newFleetExpirySweep(ms, 20*time.Millisecond, api.SweepExpiredFleetOperations)
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = task.Stop(context.Background())
		}
	})
	waitExpired := func(id uint) {
		require.Eventually(t, func() bool {
			var op model.FleetOperation
			return db.WithContext(ctx).First(&op, id).Error == nil && op.Dispatch == model.FleetDispatchAborted
		}, 2*time.Second, 5*time.Millisecond)
	}
	waitExpired(first)
	waitExpired(seed("next-interval"))
	require.NoError(t, task.Stop(context.Background()))
	stopped = true
	last := seed("after-stop")
	time.Sleep(80 * time.Millisecond)
	var op model.FleetOperation
	require.NoError(t, db.WithContext(ctx).First(&op, last).Error)
	require.Equal(t, model.FleetDispatchReady, op.Dispatch)
	require.NoError(t, task.Terminate(context.Background()))
}

func TestFleetExpirySweepRunsImmediately(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ran := make(chan struct{}, 1)
	task := newFleetExpirySweep(ms, time.Hour, func(context.Context) error {
		ran <- struct{}{}
		return nil
	})
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	t.Cleanup(func() { _ = task.Stop(context.Background()) })
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("expiry must also run at startup")
	}
}
