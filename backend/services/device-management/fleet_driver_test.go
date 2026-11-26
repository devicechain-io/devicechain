// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestFleetDriverIsNotRegisteredWhileUnserved(t *testing.T) {
	require.False(t, ledger.Served)
	require.Equal(t, time.Second, fleetDriverInterval)
	require.NoError(t, initFleetDriver())
	require.Nil(t, FleetDriver)
}

func TestFleetDriverLifecycleCancelsAndWaitsForPass(t *testing.T) {
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	for _, call := range []string{"initFleetDriver()", "FleetDriver.Start(ctx)", "FleetDriver.Stop(ctx)", "FleetDriver.Terminate(ctx)"} {
		require.Contains(t, string(source), call)
	}
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	entered, exited := make(chan struct{}), make(chan struct{})
	task := newFleetDriver(ms, fleetDriverInterval, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	})
	require.NoError(t, task.Initialize(context.Background()))
	require.NoError(t, task.Start(context.Background()))
	t.Cleanup(func() { _ = task.Stop(context.Background()) })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("driver did not start immediately")
	}
	require.NoError(t, task.Stop(context.Background()))
	select {
	case <-exited:
	default:
		t.Fatal("Stop must cancel and await the pass")
	}
	require.NoError(t, task.Terminate(context.Background()))
}

// The bulk-change series are registered only on a build that serves bulk changes, under the
// service's own namespace and subsystem, before any task can write them.
func TestBulkChangeMetricsRegisterOnlyWhenServed(t *testing.T) {
	require.False(t, ledger.Served)
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	init, expiry := strings.Index(string(source), "initBulkChangeMetrics()"), strings.Index(string(source), "initFleetExpirySweep()")
	require.True(t, init >= 0 && init < expiry, "metrics must be installed before the first bulk-change task is built")

	previous := Microservice
	t.Cleanup(func() { Microservice = previous })
	ms := &core.Microservice{FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	Microservice = ms
	initBulkChangeMetrics()
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Empty(t, families, "an unserved build exports no bulk-change series")

	model.NewBulkChangeMetrics(ms)
	families, err = reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 18)
	for _, f := range families {
		require.True(t, strings.HasPrefix(f.GetName(), "devicechain_devicemanagement_bulk_change_"), f.GetName())
	}
}
