// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// seedHeld creates n HELD commands in enqueue order and returns their ids.
func seedHeld(t *testing.T, api *Api, ctx context.Context, n int) []uint {
	t.Helper()
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		cmd := &Command{DeviceToken: "dev-1", Name: "reboot", Status: CommandHeld.String()}
		cmd.Token = fmt.Sprintf("held-%04d", i)
		if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
			t.Fatalf("seed command %d: %v", i, err)
		}
		ids = append(ids, cmd.ID)
	}
	return ids
}

// TestTheTwoSweepLocksDoNotCollide.
//
// 🔑 A COLLISION HERE WOULD BE INVISIBLE. Both locks are Postgres advisory locks keyed by
// a HASH of their name, so two names that happened to hash alike — or a copy-paste that
// reused one name — would silently serialize the minutes-cadence reconcile walk against
// the 30-second delivery sweep. Nothing would error; delivery would just intermittently
// skip a tick while a reconcile pass ran, which reads as ordinary lock contention. The
// whole point of the second lock is that the slow pass cannot starve the fast one.
func TestTheTwoSweepLocksDoNotCollide(t *testing.T) {
	if sweepLockName == reconcileLockName {
		t.Fatal("the delivery sweep and the hold reconciler must not share a lock name")
	}
	if rdb.AdvisoryLockKey(sweepLockName) == rdb.AdvisoryLockKey(reconcileLockName) {
		t.Fatalf("the two lock names hash to the same advisory key (%d); the reconcile walk would "+
			"serialize against the delivery sweep and neither would report anything wrong",
			rdb.AdvisoryLockKey(sweepLockName))
	}
}

// TestReleaseHoldReturnsACommandToTheDispatchQueue is the happy path.
func TestReleaseHoldReturnsACommandToTheDispatchQueue(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	created := enqueued(t, api, ctx, "cmd-released")
	if _, err := api.HoldCommand(ctx, created.ID); err != nil {
		t.Fatalf("HoldCommand failed: %v", err)
	}

	released, err := api.ReleaseHold(ctx, created.ID)
	if err != nil {
		t.Fatalf("ReleaseHold failed: %v", err)
	}
	if !released {
		t.Fatal("ReleaseHold did not report the transition it performed")
	}
	if got := loadOrFail(t, api, ctx, created.ID); got.Status != CommandQueued.String() {
		t.Fatalf("status = %s, want QUEUED", got.Status)
	}
}

// TestReleaseHoldCannotReviveAnAnsweredCommand: a row that left HELD between the
// reconciler's page read and this write must be left alone, whichever way it left.
//
// The terminal states are the dangerous half — a release that reopened one would put a
// finished command back in front of the dispatcher, and the device would be actuated for
// a command it had already answered. SENT is the same race one step earlier: another
// dispatcher claimed the row, and a release would steal it back and cause a second
// publish.
func TestReleaseHoldCannotReviveAnAnsweredCommand(t *testing.T) {
	states := append([]CommandStatus{CommandSent}, terminalStatuses...)
	for _, state := range states {
		t.Run(state.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")
			created := enqueued(t, api, ctx, "cmd-"+state.String())
			if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", created.ID).
				Update("status", state.String()).Error; err != nil {
				t.Fatalf("staging %s failed: %v", state, err)
			}

			released, err := api.ReleaseHold(ctx, created.ID)
			if err != nil {
				t.Fatalf("a lost race must be benign, not an error: %v", err)
			}
			if released {
				t.Fatalf("ReleaseHold released a %s command", state)
			}
			if got := loadOrFail(t, api, ctx, created.ID); got.Status != state.String() {
				t.Fatalf("a %s command became %s", state, got.Status)
			}
		})
	}
}

// TestHeldCommandsWalksTheWholeSetAcrossPasses is the fairness property, and it is the
// reason this read takes a cursor rather than a plain LIMIT.
//
// 🔑 THE WEDGE A PLAIN `LIMIT n` PRODUCES IS SILENT. With oldest-first ordering, commands
// held for a device that is never coming back keep the smallest ids and would therefore
// fill EVERY page — so the devices that DID return, holding higher ids, would never be
// examined. The reconciler would read a full page every pass, report no releases, and be
// indistinguishable from a fleet that is simply still offline.
func TestHeldCommandsWalksTheWholeSetAcrossPasses(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(core.WithTenant(context.Background(), "acme"))
	ids := seedHeld(t, api, ctx, 25)

	seen := make([]uint, 0, len(ids))
	cursor := uint(0)
	for pass := 0; pass < 5; pass++ {
		page, next, err := api.HeldCommands(ctx, cursor, 10)
		if err != nil {
			t.Fatalf("HeldCommands failed: %v", err)
		}
		for _, cmd := range page {
			seen = append(seen, cmd.ID)
		}
		cursor = next
		if next == 0 {
			break
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("the walk must visit every withheld command; saw %d of %d", len(seen), len(ids))
	}
	for i, id := range ids {
		if seen[i] != id {
			t.Fatalf("position %d: expected %d, got %d — the walk must not repeat or skip", i, id, seen[i])
		}
	}
	// 🔴 And it must RESTART. A cursor left parked past the end means every subsequent
	// pass reads nothing at all — a reconciler that quietly stops reconciling, which
	// looks exactly like one with nothing to do.
	if cursor != 0 {
		t.Fatalf("the exhausted walk must reset its cursor, got %d", cursor)
	}
}

// TestHeldCommandsIsBounded: unlike PendingCommands, this read MUST be capped. QUEUED is
// transient — every row leaves it at the next tick — while HELD is where an absent
// fleet's backlog accumulates and can sit for days.
func TestHeldCommandsIsBounded(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(core.WithTenant(context.Background(), "acme"))
	seedHeld(t, api, ctx, 120)

	page, next, err := api.HeldCommands(ctx, 0, 50)
	if err != nil {
		t.Fatalf("HeldCommands failed: %v", err)
	}
	if len(page) != 50 {
		t.Fatalf("expected a bounded page of 50, got %d", len(page))
	}
	if next != page[49].ID {
		t.Fatalf("a full page must resume from its last id, got %d", next)
	}
}

// TestHeldCommandsSelectsOnlyHeldRows. The walk feeds a path whose only action is
// HELD -> QUEUED, so a QUEUED or SENT row appearing here would be a release decision
// taken about a command that is already in flight.
func TestHeldCommandsSelectsOnlyHeldRows(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithSystemContext(core.WithTenant(context.Background(), "acme"))
	seedQueued(t, api, ctx, 5)
	held := seedHeld(t, api, ctx, 3)

	page, _, err := api.HeldCommands(ctx, 0, 100)
	if err != nil {
		t.Fatalf("HeldCommands failed: %v", err)
	}
	if len(page) != len(held) {
		t.Fatalf("expected only the %d withheld commands, got %d", len(held), len(page))
	}
	for _, cmd := range page {
		if cmd.Status != CommandHeld.String() {
			t.Fatalf("a %s command reached the reconcile walk", cmd.Status)
		}
	}
}
