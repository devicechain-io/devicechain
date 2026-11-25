// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
)

const fleetMaintenanceInterval = 60 * time.Second

var FleetMaintenance *core.PeriodicTask

func initFleetMaintenance() error {
	if !ledger.Served {
		return nil
	}
	FleetMaintenance = core.NewPeriodicTask(Microservice.FunctionalArea, "bulk-change-maintenance", fleetMaintenanceInterval,
		Api.MaintainFleetOperations, core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("bulk_change_maintenance"),
		core.WithImmediateFirstPass())
	return FleetMaintenance.Initialize(context.Background())
}
