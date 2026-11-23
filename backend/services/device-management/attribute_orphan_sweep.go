// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// Every 10 minutes, like the configuration-report maintenance, with an immediate first pass. A
// pass runs on one replica at a time (SweepAllDeviceAttributeOrphans).
const attributeOrphanSweepInterval = 10 * time.Minute

var AttributeOrphanSweep *core.PeriodicTask

// Every replica schedules the task; the pass's advisory lock admits one at a time.
func initAttributeOrphanSweep() error {
	AttributeOrphanSweep = core.NewPeriodicTask(Microservice.FunctionalArea, "device-attribute-orphan-sweep", attributeOrphanSweepInterval,
		func(ctx context.Context) error { return Api.SweepAllDeviceAttributeOrphans(ctx) },
		core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("device_attribute_orphan_sweep"),
		core.WithImmediateFirstPass())
	return AttributeOrphanSweep.Initialize(context.Background())
}
