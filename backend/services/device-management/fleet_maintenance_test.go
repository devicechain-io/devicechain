// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
)

func TestFleetMaintenanceServedGateAndLifecycle(t *testing.T) {
	require.Equal(t, time.Minute, fleetMaintenanceInterval)
	if !ledger.Served {
		previous := FleetMaintenance
		FleetMaintenance = nil
		t.Cleanup(func() { FleetMaintenance = previous })
		require.NoError(t, initFleetMaintenance(), "unserved initialization needs no API or microservice")
		require.Nil(t, FleetMaintenance)
	}
	source, err := os.ReadFile("main.go")
	require.NoError(t, err)
	for _, call := range []string{"initFleetMaintenance()", "FleetMaintenance.Start(ctx)", "FleetMaintenance.Stop(ctx)", "FleetMaintenance.Terminate(ctx)"} {
		require.Contains(t, string(source), call)
	}
	source, err = os.ReadFile("fleet_maintenance.go")
	require.NoError(t, err)
	require.Contains(t, string(source), "core.WithImmediateFirstPass()")
	source, err = os.ReadFile("model/api_fleet_retention.go")
	require.NoError(t, err)
	require.Contains(t, string(source), "api.RDB.SingletonPass(ctx,")
}
