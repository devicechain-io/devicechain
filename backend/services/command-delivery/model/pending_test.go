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
