// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
)

var FleetSettlement, FleetSettlementDeadline *core.PeriodicTask

func initFleetSettlement() error {
	if !ledger.Served {
		return nil
	}
	var lastSweep time.Time
	FleetSettlement = core.NewPeriodicTask(Microservice.FunctionalArea, "bulk-change-settlement", 5*time.Second,
		func(ctx context.Context) error {
			if lastSweep.IsZero() || time.Since(lastSweep) >= 5*time.Minute {
				err := Api.SweepFleetSettlement(ctx)
				if err != nil && !errors.Is(err, core.ErrPassSkipped) {
					return err
				}
				lastSweep = time.Now()
			}
			return Api.RunFleetSettlement(ctx, true, false)
		}, core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("bulk_change_settlement"), core.WithImmediateFirstPass())
	FleetSettlementDeadline = core.NewPeriodicTask(Microservice.FunctionalArea, "bulk-change-settlement-deadline", 30*time.Second,
		func(ctx context.Context) error { return Api.RunFleetSettlement(ctx, false, true) },
		core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("bulk_change_settlement_deadline"), core.WithImmediateFirstPass())
	if err := FleetSettlement.Initialize(context.Background()); err != nil {
		return err
	}
	return FleetSettlementDeadline.Initialize(context.Background())
}
