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

// PendingCommands feeds the redelivery sweep.
func TestPendingCommands(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")

	// Ordering replaces an unordered read, so delivery follows enqueue order rather
	// than whatever the planner returned.
	t.Run("returns the oldest commands first", func(t *testing.T) {
		api := newTestApi(t)
		ids := seedQueued(t, api, ctx, 50)

		pending, err := api.PendingCommands(core.WithSystemContext(ctx))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != len(ids) {
			t.Fatalf("expected all %d queued commands, got %d", len(ids), len(pending))
		}
		for i, cmd := range pending {
			if cmd.ID != ids[i] {
				t.Fatalf("position %d: expected command %d (enqueue order), got %d",
					i, ids[i], cmd.ID)
			}
		}
	})

	// The read is deliberately uncapped. A naive LIMIT combined with oldest-first
	// ordering lets undeliverable commands hold the front of every batch forever, which
	// wedges delivery platform-wide — strictly worse than the memory cost it saves. See
	// the note on PendingCommands for what a correct bound requires.
	t.Run("returns every queued command, not a fixed-size page", func(t *testing.T) {
		api := newTestApi(t)
		seedQueued(t, api, ctx, 1200)

		pending, err := api.PendingCommands(core.WithSystemContext(ctx))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pending) != 1200 {
			t.Fatalf("expected all 1200 queued commands, got %d", len(pending))
		}
	})

	// The sweep's source is the DISPATCHABLE set, not the queued set. A HELD row
	// belongs in it — the sweep is the recurring pass that re-reads presence, so a
	// command withheld while its device was absent is reconsidered once the device
	// returns; a hold the sweep could not see would only ever be drained by its TTL.
	// A row that has already gone out (SENT) or finished must not come back — and
	// neither must a HELD one.
	//
	// 🔑 THIS ASSERTION HAS NOW FLIPPED TWICE, WHICH IS WORTH KNOWING BEFORE FLIPPING IT
	// A THIRD TIME. It began as "returns only QUEUED commands"; the HELD slice changed it
	// to "returns the dispatchable set", because a hold the sweep could not see would
	// never be drained; the presence gate changes it back, because a hold is now released
	// by the device's RETURN rather than by re-reading the whole backlog every 30 seconds.
	//
	// The invariant underneath all three is the one to reason from, not the row list: the
	// sweep selects exactly what it can dispatch WITHOUT new information. QUEUED qualifies.
	// HELD does not — by construction it is waiting for information the sweep does not have.
	t.Run("returns the queued set only; held rows are released by the device returning", func(t *testing.T) {
		api := newTestApi(t)
		seedQueued(t, api, ctx, 3)

		held := &Command{DeviceToken: "dev-1", Name: "reboot", Status: CommandHeld.String()}
		held.Token = "held-for-absent-device"
		if err := api.RDB.DB(ctx).Create(held).Error; err != nil {
			t.Fatalf("seed held command: %v", err)
		}
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
			if cmd.Status == CommandSent.String() {
				t.Fatalf("a %s command must not be redelivered", cmd.Status)
			}
			if cmd.Token == "held-for-absent-device" {
				t.Fatal("the sweep returned a HELD command; the gate is undone and an absent fleet's " +
					"entire backlog is re-read on every tick")
			}
		}
	})
}
