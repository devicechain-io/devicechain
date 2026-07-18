// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// seedQueued creates n QUEUED commands in enqueue order and returns their ids.
func seedQueued(t *testing.T, api *Api, ctx context.Context, n int) []uint {
	t.Helper()
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		cmd := &Command{DeviceToken: "dev-1", Name: "reboot", Status: CommandQueued.String()}
		cmd.Token = fmt.Sprintf("cmd-%04d", i)
		if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
			t.Fatalf("seed command %d: %v", i, err)
		}
		ids = append(ids, cmd.ID)
	}
	return ids
}

// PendingCommands feeds the redelivery sweep. Both of its guarantees exist to keep a
// backlog from turning a delivery problem into a worse one.
func TestPendingCommands(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")

	// An unbounded read of every QUEUED command across every tenant is fine on a
	// healthy instance and a memory hazard on an unhealthy one: a fleet that goes
	// offline queues without limit, and the sweep would load the whole backlog at once.
	t.Run("caps a batch at PendingCommandBatch", func(t *testing.T) {
		api := newTestApi(t)
		seedQueued(t, api, ctx, PendingCommandBatch+25)

		pending, err := api.PendingCommands(core.WithSystemContext(ctx))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != PendingCommandBatch {
			t.Fatalf("expected a batch of %d, got %d", PendingCommandBatch, len(pending))
		}
	})

	// Without an explicit order the batch is whatever the planner returns, so under a
	// backlog larger than one batch a command could be starved indefinitely while newer
	// ones ship. Oldest-first makes the queue a queue.
	t.Run("returns the oldest commands first", func(t *testing.T) {
		api := newTestApi(t)
		ids := seedQueued(t, api, ctx, PendingCommandBatch+10)

		pending, err := api.PendingCommands(core.WithSystemContext(ctx))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for i, cmd := range pending {
			if cmd.ID != ids[i] {
				t.Fatalf("position %d: expected command %d (enqueue order), got %d",
					i, ids[i], cmd.ID)
			}
		}
		// The newest commands are the ones deferred to the next pass — not the oldest.
		last := pending[len(pending)-1].ID
		if last != ids[PendingCommandBatch-1] {
			t.Fatalf("batch should end at the %dth enqueued command, got id %d",
				PendingCommandBatch, last)
		}
	})

	t.Run("returns only queued commands", func(t *testing.T) {
		api := newTestApi(t)
		seedQueued(t, api, ctx, 3)
		sent := &Command{DeviceToken: "dev-1", Name: "reboot", Status: CommandSent.String()}
		sent.Token = "already-sent"
		if err := api.RDB.DB(ctx).Create(sent).Error; err != nil {
			t.Fatalf("seed sent command: %v", err)
		}

		pending, err := api.PendingCommands(core.WithSystemContext(ctx))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 3 {
			t.Fatalf("expected only the 3 queued commands, got %d", len(pending))
		}
		for _, cmd := range pending {
			if cmd.Status != CommandQueued.String() {
				t.Fatalf("a %s command must not be redelivered", cmd.Status)
			}
		}
	})
}
