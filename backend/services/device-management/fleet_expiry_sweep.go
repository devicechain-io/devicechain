// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// Like the proposal reaper, expiry polls once a minute. This is a service constant, so an
// existing instance configuration needs no new key during a rolling upgrade.
const fleetExpirySweepInterval = time.Minute

var FleetExpirySweep *core.PeriodicTask

func newFleetExpirySweep(ms *core.Microservice, interval time.Duration, pass func(context.Context) error) *core.PeriodicTask {
	return core.NewPeriodicTask(ms.FunctionalArea, "fleet-expiry-sweep", interval, pass,
		core.NewNoOpLifecycleCallbacks(), ms.NewPeriodicTaskMetrics("fleet_expiry_sweep"),
		core.WithImmediateFirstPass())
}

// Only expiry: scheduling this task must never activate or start the fleet driver.
func initFleetExpirySweep() error {
	FleetExpirySweep = newFleetExpirySweep(Microservice, fleetExpirySweepInterval, Api.SweepExpiredFleetOperations)
	return FleetExpirySweep.Initialize(context.Background())
}
