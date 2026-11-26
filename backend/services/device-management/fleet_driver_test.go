// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

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
