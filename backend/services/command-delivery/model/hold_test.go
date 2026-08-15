// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// enqueued creates one QUEUED command and returns it.
func enqueued(t *testing.T, api *Api, ctx context.Context, token string) *Command {
	t.Helper()
	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token:       token,
		DeviceToken: "device-1",
		Name:        "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}
	return created
}

// TestHoldCommandWithholdsAQueuedCommand is the happy path: the gate observed an
// authoritatively absent device and the row moves QUEUED -> HELD.
func TestHoldCommandWithholdsAQueuedCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	created := enqueued(t, api, ctx, "cmd-hold")

	held, err := api.HoldCommand(ctx, created.ID)
	if err != nil {
		t.Fatalf("HoldCommand failed: %v", err)
	}
	if !held {
		t.Fatal("HoldCommand did not report the transition it performed")
	}
	if got := loadOrFail(t, api, ctx, created.ID); got.Status != CommandHeld.String() {
		t.Fatalf("status = %s, want HELD", got.Status)
	}
}

// 🔴 TestHoldCommandCannotRecallADispatchedCommand IS THE WHOLE REASON THE WRITE IS
// CONDITIONAL, and the failure it prevents is a SECOND PHYSICAL ACTUATION.
//
// The sweep SELECTs its batch and then walks it, so a row it observed as QUEUED can be
// claimed SENT by another dispatcher — the LwM2M wake drain — while the walk is still in
// progress. An unconditional hold would then stamp HELD over a command that was physically
// dispatched microseconds earlier. HELD is claimable, so the next tick or the next drain
// sends it again, and a device dispenses, unlocks or reboots twice.
//
// Deleting the `AND status = 'QUEUED'` predicate makes this test fail and nothing else in
// the suite notice, which is precisely why it is written from the racing dispatcher's side
// rather than as a status-table check.
func TestHoldCommandCannotRecallADispatchedCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	created := enqueued(t, api, ctx, "cmd-raced")

	// Another dispatcher claims it between the sweep's read and its hold.
	_, claimed, err := api.MarkSent(ctx, created.ID)
	if err != nil || !claimed {
		t.Fatalf("staging the race failed: claimed=%v err=%v", claimed, err)
	}

	held, err := api.HoldCommand(ctx, created.ID)
	if err != nil {
		t.Fatalf("a lost race must be benign, not an error: %v", err)
	}
	if held {
		t.Fatal("HoldCommand claimed to have withheld a command another dispatcher already sent")
	}
	got := loadOrFail(t, api, ctx, created.ID)
	if got.Status != CommandSent.String() {
		t.Fatalf("a dispatched command was dragged back to %s; it is now claimable again and the "+
			"device will be actuated a SECOND time", got.Status)
	}
	if !got.SentTime.Valid {
		t.Fatal("the dispatch record was wiped")
	}
}

// TestHoldCommandCannotReviveAnAnsweredCommand walks every terminal state, because each
// is a different way for the row to have finished between the sweep's read and this write
// — a device response, a TTL, an operator cancelling — and a hold that reopened any of
// them would put a finished command back in the dispatchable set.
func TestHoldCommandCannotReviveAnAnsweredCommand(t *testing.T) {
	for _, terminal := range terminalStatuses {
		t.Run(terminal.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")
			created := enqueued(t, api, ctx, "cmd-"+terminal.String())

			// Drive it terminal behind the gate's back, exactly as a racing writer would.
			if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", created.ID).
				Update("status", terminal.String()).Error; err != nil {
				t.Fatalf("staging %s failed: %v", terminal, err)
			}

			held, err := api.HoldCommand(ctx, created.ID)
			if err != nil {
				t.Fatalf("HoldCommand failed: %v", err)
			}
			if held {
				t.Fatalf("HoldCommand withheld a %s command", terminal)
			}
			if got := loadOrFail(t, api, ctx, created.ID); got.Status != terminal.String() {
				t.Fatalf("a %s command became %s", terminal, got.Status)
			}
		})
	}
}

// TestMarkUndeliverableFailsWithAReason is the Sparkplug path: the transport carries no
// command at all, so the command ends now rather than waiting out a TTL it can never
// satisfy.
func TestMarkUndeliverableFailsWithAReason(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	created := enqueued(t, api, ctx, "cmd-undeliverable")

	failed, err := api.MarkUndeliverable(ctx, created.ID, "no command path")
	if err != nil {
		t.Fatalf("MarkUndeliverable failed: %v", err)
	}
	if !failed {
		t.Fatal("MarkUndeliverable did not report the transition it performed")
	}
	got := loadOrFail(t, api, ctx, created.ID)
	if got.Status != CommandFailed.String() {
		t.Fatalf("status = %s, want FAILED", got.Status)
	}
	// 🔑 The reason is the point. FAILED with no explanation is indistinguishable from a
	// device that rejected the command, which sends an operator to check hardware that is
	// working perfectly.
	if !got.Error.Valid || got.Error.String != "no command path" {
		t.Fatalf("the reason must be recorded on the row, got %+v", got.Error)
	}
	if got.SentTime.Valid {
		t.Fatal("an undeliverable command was never dispatched, so it must carry no sent_time")
	}
}

// TestMarkUndeliverableCannotFailADispatchedCommand is the same race as the hold's, and
// it is worse here because the terminal is permanent: a command that WAS published would
// be recorded as never-dispatched, and the device's imminent response would then be
// dropped by MarkResponse's terminal guard. The device acts, answers, and the platform's
// record says it was never asked.
func TestMarkUndeliverableCannotFailADispatchedCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	created := enqueued(t, api, ctx, "cmd-sent-then-failed")

	_, claimed, err := api.MarkSent(ctx, created.ID)
	if err != nil || !claimed {
		t.Fatalf("staging the race failed: claimed=%v err=%v", claimed, err)
	}

	failed, err := api.MarkUndeliverable(ctx, created.ID, "no command path")
	if err != nil {
		t.Fatalf("a lost race must be benign, not an error: %v", err)
	}
	if failed {
		t.Fatal("MarkUndeliverable failed a command that had already been dispatched")
	}
	got := loadOrFail(t, api, ctx, created.ID)
	if got.Status != CommandSent.String() {
		t.Fatalf("status = %s, want SENT left intact", got.Status)
	}
	if got.Error.Valid {
		t.Fatalf("an undeliverable reason was stamped on a dispatched command: %q", got.Error.String)
	}
}

// TestAHoldDoesNotChangeWhatTheCeilingCounts.
//
// 🔑 THE CEILING COUNTS EVERY STATE THE PLATFORM STILL HOLDS — QUEUED, HELD AND PARKED —
// PRECISELY SO THIS IS TRUE. A hold moves a row
// between two counted states, so the count is INVARIANT UNDER PROMOTION and no sweep tick
// can push a tenant over its ceiling however large its backlog is. While the ceiling
// counted HELD alone, a tenant whose fleet was absent could enqueue without limit — every
// check passing, because nothing was HELD yet — and then a single tick would convert the
// whole backlog at once.
//
// This is asserted here, on the gate's own write, rather than only at the enqueue site:
// the invariant is a property of the pair, and a future change to either half could break
// it while the half it changed still looked correct.
func TestAHoldDoesNotChangeWhatTheCeilingCounts(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	created := enqueued(t, api, ctx, "cmd-counted")

	before := undeliveredCount(t, api, ctx)
	held, err := api.HoldCommand(ctx, created.ID)
	if err != nil {
		t.Fatalf("HoldCommand failed: %v", err)
	}
	// 🔴 WITHOUT THIS THE TEST CANNOT FAIL. "The count did not change" is also exactly
	// what a HoldCommand that did nothing at all produces, so the transition has to be
	// confirmed before its invariance means anything.
	if !held || loadOrFail(t, api, ctx, created.ID).Status != CommandHeld.String() {
		t.Fatal("the promotion under test did not happen, so its invariance proves nothing")
	}
	if after := undeliveredCount(t, api, ctx); after != before {
		t.Fatalf("holding a command changed the ceiling's count from %d to %d; the counted set "+
			"must be invariant under QUEUED -> HELD or a tenant can enqueue past its ceiling and "+
			"one sweep tick converts the backlog", before, after)
	}
	// And the count must be the one that MOVES for a genuine enqueue — otherwise a
	// helper that counted nothing at all would satisfy every assertion above.
	enqueued(t, api, ctx, "cmd-counted-2")
	if after := undeliveredCount(t, api, ctx); after != before+1 {
		t.Fatalf("a new enqueue must raise the ceiling's count from %d, got %d", before, after)
	}
}

// undeliveredCount counts what the enqueue ceiling meters, through the same status set.
func undeliveredCount(t *testing.T, api *Api, ctx context.Context) int64 {
	t.Helper()
	var count int64
	if err := api.RDB.DB(ctx).Model(&Command{}).
		Where("status IN ?", undeliveredStatusStrings()).Count(&count).Error; err != nil {
		t.Fatalf("counting undelivered commands failed: %v", err)
	}
	return count
}
