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

const (
	fleetSettlementSweepEvery = 5 * time.Minute
	// A failed sweep is retried sooner than the next full interval, but never on every
	// five-second tick: a sweep that keeps failing must not become a full scan per tick.
	fleetSettlementSweepRetry = 30 * time.Second
)

// settlementTick is one pass of the "bulk-change-settlement" task: the full sweep when it
// is due, then the fast pass. The fast pass ALWAYS runs: a sweep that fails (one tenant's
// bad line, a statement timeout on a large tenant) must not stop every tenant's fast path.
type settlementTick struct {
	nextSweep time.Time
	now       func() time.Time
	sweep     func(context.Context) error
	fast      func(context.Context) error
}

func (t *settlementTick) run(ctx context.Context) error {
	var sweepErr error
	if now := t.now(); !now.Before(t.nextSweep) {
		sweepErr = t.sweep(ctx)
		if errors.Is(sweepErr, core.ErrPassSkipped) {
			// Another replica holds the sweep; it is that replica's interval now.
			sweepErr = nil
		}
		t.nextSweep = now.Add(fleetSettlementSweepEvery)
		if sweepErr != nil {
			t.nextSweep = now.Add(fleetSettlementSweepRetry)
		}
	}
	return errors.Join(t.fast(ctx), sweepErr)
}

func initFleetSettlement() error {
	if !ledger.Served {
		return nil
	}
	tick := &settlementTick{
		now:   time.Now,
		sweep: func(ctx context.Context) error { return Api.SweepFleetSettlement(ctx) },
		fast:  func(ctx context.Context) error { return Api.RunFleetSettlement(ctx, true, false) },
	}
	FleetSettlement = core.NewPeriodicTask(Microservice.FunctionalArea, "bulk-change-settlement", 5*time.Second,
		tick.run, core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("bulk_change_settlement"), core.WithImmediateFirstPass())
	FleetSettlementDeadline = core.NewPeriodicTask(Microservice.FunctionalArea, "bulk-change-settlement-deadline", 30*time.Second,
		func(ctx context.Context) error { return Api.RunFleetSettlement(ctx, false, true) },
		core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("bulk_change_settlement_deadline"), core.WithImmediateFirstPass())
	if err := FleetSettlement.Initialize(context.Background()); err != nil {
		return err
	}
	return FleetSettlementDeadline.Initialize(context.Background())
}
