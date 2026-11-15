// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
)

type slowDrainReader struct{}

func (slowDrainReader) StatesFor(context.Context, []string) (map[string]presence.State, error) {
	time.Sleep(10 * time.Millisecond)
	return map[string]presence.State{}, nil
}

func drainFleet() []*model.Command {
	rows := make([]*model.Command, 1200)
	for i := range rows {
		rows[i] = heldFor(uint(i+1), "c", "acme")
	}
	return rows
}

func TestHoldReconcileDrainsWhileReleasing(t *testing.T) {
	api := &fakeApi{heldPage: drainFleet()}
	proc := reconcilerWith(api, presence.Empty{})
	if err := proc.reconcileHolds(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.releasedHolds) != 1200 || len(api.heldReads) != 3 || proc.reconcileCursor != 0 {
		t.Fatalf("released %d, pages %v, cursor %d", len(api.releasedHolds), api.heldReads, proc.reconcileCursor)
	}
}

func TestHoldReconcileStopsAtAStuckPage(t *testing.T) {
	api := &fakeApi{heldPage: drainFleet()}
	proc := reconcilerWith(api, &scriptedReader{states: map[string]presence.State{"dev-c": asserted(false, mqttSource)}})
	if err := proc.reconcileHolds(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.releasedHolds) != 0 || len(api.heldReads) != 1 || proc.reconcileCursor != 500 {
		t.Fatalf("released %d, pages %v, cursor %d", len(api.releasedHolds), api.heldReads, proc.reconcileCursor)
	}
}

func TestHoldReconcileHonoursItsBudget(t *testing.T) {
	api := &fakeApi{heldPage: drainFleet()}
	proc := reconcilerWith(api, slowDrainReader{})
	proc.holdInterval = 2 * time.Millisecond
	if err := proc.reconcileHolds(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.heldReads) > 1 || len(api.releasedHolds) != 0 {
		t.Fatalf("budget exhausted: pages %v, releases %d", api.heldReads, len(api.releasedHolds))
	}
}
