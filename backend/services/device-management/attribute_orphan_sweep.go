// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

const attributeOrphanSweepInterval = time.Minute

var AttributeOrphanSweep *core.PeriodicTask

// Match the create-enrollment healer's lifecycle and HA behavior: every replica
// runs an immediate first pass, followed by fixed-interval idempotent passes.
func initAttributeOrphanSweep() error {
	AttributeOrphanSweep = core.NewPeriodicTask(Microservice.FunctionalArea, "device-attribute-orphan-sweep", attributeOrphanSweepInterval,
		func(ctx context.Context) error { return Api.SweepAllDeviceAttributeOrphans(ctx) },
		core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("device_attribute_orphan_sweep"),
		core.WithImmediateFirstPass())
	return AttributeOrphanSweep.Initialize(context.Background())
}
