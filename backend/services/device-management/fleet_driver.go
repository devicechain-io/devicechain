// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
)

// A service constant: rolling upgrades need no new configuration key.
const fleetDriverInterval = time.Second

var FleetDriver *core.PeriodicTask

func newFleetDriver(ms *core.Microservice, interval time.Duration, pass func(context.Context) error) *core.PeriodicTask {
	return core.NewPeriodicTask(ms.FunctionalArea, "bulk-change-driver", interval, pass,
		core.NewNoOpLifecycleCallbacks(), ms.NewPeriodicTaskMetrics("bulk_change_driver"),
		core.WithImmediateFirstPass())
}

func initFleetDriver() error {
	if !ledger.Served {
		return nil
	}
	// Every replica runs this task; posting claims, device try-locks and the DB pacer arbitrate overlap.
	FleetDriver = newFleetDriver(Microservice, fleetDriverInterval, Api.RunFleetDriver)
	return FleetDriver.Initialize(context.Background())
}

// initBulkChangeMetrics registers the bulk-change series, only on a build that serves bulk changes.
// main.go calls it before any bulk-change task or the API can write them.
func initBulkChangeMetrics() {
	if !ledger.Served {
		return
	}
	model.UseBulkChangeMetrics(model.NewBulkChangeMetrics(Microservice))
}
