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

// Bulk changes are served: every replica builds the driver, both settlement tasks, the
// reconciler lag measurement and the maintenance pass, and config.set is the one kind this area registers.
func TestBulkChangeTasksAreRegisteredWhenServed(t *testing.T) {
	require.True(t, ledger.Served)
	require.Equal(t, []string{"config.set"}, ledger.ExecutableKinds())

	prevMs, prevDriver, prevSettle, prevDeadline, prevMaint, prevLag := Microservice, FleetDriver, FleetSettlement, FleetSettlementDeadline, FleetMaintenance, FleetReconcilerLag
	t.Cleanup(func() {
		Microservice, FleetDriver, FleetSettlement, FleetSettlementDeadline, FleetMaintenance, FleetReconcilerLag = prevMs, prevDriver, prevSettle, prevDeadline, prevMaint, prevLag
	})
	ms := &core.Microservice{FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice = ms
	FleetDriver, FleetSettlement, FleetSettlementDeadline, FleetMaintenance, FleetReconcilerLag = nil, nil, nil, nil, nil

	require.NoError(t, initFleetDriver())
	require.NoError(t, initFleetSettlement())
	require.NoError(t, initFleetMaintenance())
	for name, task := range map[string]*core.PeriodicTask{
		"driver": FleetDriver, "settlement": FleetSettlement, "settlement deadline": FleetSettlementDeadline, "maintenance": FleetMaintenance,
		"reconciler lag": FleetReconcilerLag,
	} {
		require.NotNil(t, task, "%s is not registered", name)
	}
}
