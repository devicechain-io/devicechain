// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Bulk changes are served: every replica builds the driver, both settlement tasks and the
// maintenance pass, and config.set is the one kind this area registers.
func TestBulkChangeTasksAreRegisteredWhenServed(t *testing.T) {
	require.True(t, ledger.Served)
	require.Equal(t, []string{"config.set"}, ledger.ExecutableKinds())

	prevMs, prevDriver, prevSettle, prevDeadline, prevMaint := Microservice, FleetDriver, FleetSettlement, FleetSettlementDeadline, FleetMaintenance
	t.Cleanup(func() {
		Microservice, FleetDriver, FleetSettlement, FleetSettlementDeadline, FleetMaintenance = prevMs, prevDriver, prevSettle, prevDeadline, prevMaint
	})
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice = ms
	FleetDriver, FleetSettlement, FleetSettlementDeadline, FleetMaintenance = nil, nil, nil, nil

	require.NoError(t, initFleetDriver())
	require.NoError(t, initFleetSettlement())
	require.NoError(t, initFleetMaintenance())
	for name, task := range map[string]*core.PeriodicTask{
		"driver": FleetDriver, "settlement": FleetSettlement, "settlement deadline": FleetSettlementDeadline, "maintenance": FleetMaintenance,
	} {
		require.NotNil(t, task, "%s is not registered", name)
	}
}
