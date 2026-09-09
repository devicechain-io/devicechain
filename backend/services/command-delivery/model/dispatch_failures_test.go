// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// reclaim puts a released row back into SENT the way a dispatcher does, so a test can walk
// a command up to the bound through the real writes rather than by editing columns.
func reclaim(t *testing.T, api *Api, ctx context.Context, id uint) {
	t.Helper()
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("re-claiming %d must land (claimed=%v err=%v); a released command has to be "+
			"claimable again or there is no retry to bound", id, claimed, err)
	}
}

// TestAnUnsetBoundIsThePlatformDefaultAndNotZero is the gate on the inversion that would be
// hardest to notice and worst to ship.
//
// 🔴 A ZERO READ LITERALLY FAILS EVERY COMMAND ON ITS FIRST PUBLISH ERROR, because the
// predicate is `dispatch_failures + 1 >= bound` and one is greater than zero. The Api is
// built by literal in every test and in any binary whose wiring is incomplete, so a zero is
// an unset field, not an operator asking for a bound of none. The harmless-looking value has
// to land on the default — the same direction ApplyDefaults takes.
func TestAnUnsetBoundIsThePlatformDefaultAndNotZero(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "unconfigured", CommandSent)

	if api.MaxDispatchFailures != 0 {
		t.Fatal("premise lost: this test needs an Api with no configured bound")
	}
	landed, released, err := api.ReleaseClaim(ctx, id)
	if err != nil || !released {
		t.Fatalf("release: landed=%q released=%v err=%v", landed, released, err)
	}
	if landed != CommandQueued {
		t.Fatalf("the first failed dispatch landed the command on %q; with no bound configured "+
			"the platform default is in force and one failure comes nowhere near it", landed)
	}
	if got := api.maxDispatchFailures(); got != config.DefaultMaxDispatchFailures {
		t.Fatalf("an unset bound resolves to %d, want the platform default %d",
			got, config.DefaultMaxDispatchFailures)
	}
}

// TestTheFailureCountIsStoredAndDrivesTheBound walks a command to the bound through the real
// claim/release pair and asserts the STORED count at every step.
//
// 🔑 THE STORED VALUE IS THE POINT. The bound is evaluated in SQL against this column, so a
// count that is logged but not written is a bound that can never be reached — and the
// symptom would be the original defect, unchanged, with a metric that never fires.
func TestTheFailureCountIsStoredAndDrivesTheBound(t *testing.T) {
	api := newTestApi(t)
	api.MaxDispatchFailures = 3
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "counted", CommandSent)

	for failure := 1; failure <= 2; failure++ {
		landed, released, err := api.ReleaseClaim(ctx, id)
		if err != nil || !released {
			t.Fatalf("failure %d: release must land (released=%v err=%v)", failure, released, err)
		}
		if landed != CommandQueued {
			t.Fatalf("failure %d landed on %q, want QUEUED; the bound is 3 and this is not it",
				failure, landed)
		}
		if got := loadOrFail(t, api, ctx, id); got.DispatchFailures != failure {
			t.Fatalf("after failure %d the row stores %d; the count must be written, not merely "+
				"logged, because the bound is tested against the column",
				failure, got.DispatchFailures)
		}
		reclaim(t, api, ctx, id)
	}

	landed, released, err := api.ReleaseClaim(ctx, id)
	if err != nil || !released {
		t.Fatalf("the exhausting release must land (released=%v err=%v)", released, err)
	}
	if landed != CommandFailed {
		t.Fatalf("the third failure landed on %q, want FAILED", landed)
	}
	row := loadOrFail(t, api, ctx, id)
	if row.DispatchFailures != 3 {
		t.Fatalf("the exhausting release stored %d failures, want 3", row.DispatchFailures)
	}
	if row.Status != CommandFailed.String() || row.Error.String != UndispatchableReason {
		t.Fatalf("the exhausted row is %s / %q; it must be FAILED and say the platform could "+
			"not publish it", row.Status, row.Error.String)
	}
}

// TestAHigherBoundKeepsRetryingPastTheLowerOne is the counterweight to the test above: it
// shows the terminal is driven by the CONFIGURED number and not by some fixed count the
// implementation happens to agree with at 3.
func TestAHigherBoundKeepsRetryingPastTheLowerOne(t *testing.T) {
	api := newTestApi(t)
	api.MaxDispatchFailures = 6
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "patient", CommandSent)

	for failure := 1; failure <= 5; failure++ {
		landed, released, err := api.ReleaseClaim(ctx, id)
		if err != nil || !released {
			t.Fatalf("failure %d: release must land (released=%v err=%v)", failure, released, err)
		}
		if landed != CommandQueued {
			t.Fatalf("failure %d landed on %q with a bound of 6; a bound an operator raised must "+
				"actually be the number in force", failure, landed)
		}
		reclaim(t, api, ctx, id)
	}
	if landed, _, err := api.ReleaseClaim(ctx, id); err != nil || landed != CommandFailed {
		t.Fatalf("the sixth failure landed on %q (err=%v), want FAILED", landed, err)
	}
}

// TestACancelledBatchStillWinsOverTheDispatchBound pins the ORDER of the release's writes.
//
// 🔴 THE BOUND MUST NOT OVERTAKE THE BRAKE. A batch cancel names the ACTOR — a human called
// this fleet write off — while exhaustion names only an outcome, and CANCELLED is the answer
// an operator who stopped the batch needs to see on the row. The exhaustion branch was added
// between the cancelled-batch write and the ordinary requeue for exactly this reason; put it
// first and a called-off command would be reported as a platform delivery failure instead.
func TestACancelledBatchStillWinsOverTheDispatchBound(t *testing.T) {
	api := newBatchTestApi(t)
	api.MaxDispatchFailures = 1000
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "stopped", map[string]string{
		"sent-1": CommandSent.String(),
	})
	if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	claimed := &Command{}
	if err := api.RDB.DB(ctx).Where("token = ?", "cmd-sent-1").First(claimed).Error; err != nil {
		t.Fatalf("read command: %v", err)
	}
	// Put it right on the bound, so the exhaustion branch would fire if it ran first.
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", claimed.ID).
		Update("dispatch_failures", 999).Error; err != nil {
		t.Fatalf("stage the failure count: %v", err)
	}

	landed, released, err := api.ReleaseClaim(ctx, claimed.ID)
	if err != nil || !released {
		t.Fatalf("release: landed=%q released=%v err=%v", landed, released, err)
	}
	if landed != CommandCancelled {
		t.Fatalf("a called-off command at the dispatch bound landed on %q; the cancel is the "+
			"stronger fact and must still win", landed)
	}
	if got := statusByToken(t, api, ctx, "cmd-sent-1"); got != CommandCancelled.String() {
		t.Errorf("the row reads %s, want CANCELLED", got)
	}
}

// TestParkingACommandDoesNotCountAgainstTheDispatchBound is the model-level half of the
// argument for counting FAILURES rather than claims.
//
// 🔴 A PARK IS A SUCCESSFUL PUBLISH. The transport put the command on the wire and found the
// device asleep; the platform did nothing wrong and nothing was lost. If parks counted, a
// queue-mode sleeper would accumulate a bound's worth of them over ordinary weeks of duty
// cycle and the first genuine publish failure after that would kill the command outright.
func TestParkingACommandDoesNotCountAgainstTheDispatchBound(t *testing.T) {
	api := newTestApi(t)
	api.MaxDispatchFailures = 3
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "sleeper", CommandQueued)

	for wake := 1; wake <= 5; wake++ {
		nonce, claimed, err := api.MarkSent(ctx, id)
		if err != nil || !claimed {
			t.Fatalf("wake %d: claim must land (claimed=%v err=%v)", wake, claimed, err)
		}
		if landed, parked, err := api.ParkClaim(ctx, "sleeper", nonce); err != nil || !parked {
			t.Fatalf("wake %d: park must land (landed=%q parked=%v err=%v)", wake, landed, parked, err)
		}
	}

	row := loadOrFail(t, api, ctx, id)
	if row.Status != CommandParked.String() {
		t.Fatalf("after five wakes the command is %s, want PARKED", row.Status)
	}
	if row.DispatchFailures != 0 {
		t.Fatalf("five parks recorded %d dispatch failures, want 0; parking is the transport "+
			"working, and counting it would declare a sleeper's command poison", row.DispatchFailures)
	}
}

// TestReleasingATerminalRowNeitherCountsNorFails keeps the from-state predicate honest on
// the new column as well as on the status.
//
// A release that matched a terminal row would already be the double-actuation defect the
// predicate exists to stop; what this adds is that it must not INCREMENT one either. A count
// that moved on a row nothing released would drift upward on every lost race and eventually
// fail a healthy command for other dispatchers' work.
func TestReleasingATerminalRowNeitherCountsNorFails(t *testing.T) {
	api := newTestApi(t)
	api.MaxDispatchFailures = 3
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "answered", CommandSuccessful)

	landed, released, err := api.ReleaseClaim(ctx, id)
	if err != nil {
		t.Fatalf("release on a terminal row errored: %v", err)
	}
	if released || landed != "" {
		t.Fatalf("release reported landed=%q released=%v on a terminal row; nothing moved, so "+
			"nothing landed anywhere", landed, released)
	}
	row := loadOrFail(t, api, ctx, id)
	if row.Status != CommandSuccessful.String() {
		t.Fatalf("a terminal row became %s", row.Status)
	}
	if row.DispatchFailures != 0 {
		t.Fatalf("a release that moved nothing counted %d failures", row.DispatchFailures)
	}
}

// TestTheExhaustedTerminalIsDistinguishableFromATimeout is the operator's question, asked as
// a test: given two finished commands, can you tell "the device never answered" from "we
// could never send this"?
//
// 🔴 THE STATUS ALONE IS NOT THE ANSWER AND WAS NEVER MEANT TO BE. Both rows are finished and
// neither was answered; what separates them is that one was DISPATCHED. So the exhausted row
// must carry no sent_time and must name the platform in its error, and the timed-out row must
// keep the sent_time that makes TIMEOUT a true statement about it.
func TestTheExhaustedTerminalIsDistinguishableFromATimeout(t *testing.T) {
	api := newTestApi(t)
	api.MaxDispatchFailures = 1
	ctx := core.WithTenant(context.Background(), "A")

	// A bound of 1 is refused by config validation and is used here deliberately: it makes
	// the exhausting release the FIRST one, so this test states one behaviour rather than
	// re-walking the loop the count test already owns.
	exhausted := seedWithStatus(t, api, ctx, "never-sent", CommandSent)
	if landed, _, err := api.ReleaseClaim(ctx, exhausted); err != nil || landed != CommandFailed {
		t.Fatalf("staging the exhausted row: landed=%q err=%v", landed, err)
	}

	// The other row: dispatched, unanswered, expired by the sweep as TIMEOUT.
	timedOut := seedWithStatus(t, api, ctx, "unanswered", CommandQueued)
	if _, claimed, err := api.MarkSent(ctx, timedOut); err != nil || !claimed {
		t.Fatalf("claiming the timeout row: claimed=%v err=%v", claimed, err)
	}

	poison := loadOrFail(t, api, ctx, exhausted)
	sent := loadOrFail(t, api, ctx, timedOut)

	if poison.SentTime.Valid {
		t.Error("the undispatchable command carries a sent_time; that column is what makes a " +
			"TIMEOUT reading of the row look justified, and nothing was sent")
	}
	if !sent.SentTime.Valid {
		t.Error("premise lost: a dispatched command must carry a sent_time")
	}
	if poison.Error.String != UndispatchableReason {
		t.Errorf("the undispatchable command's error is %q, want the platform-side reason",
			poison.Error.String)
	}
	if poison.DispatchFailures == 0 || sent.DispatchFailures != 0 {
		t.Errorf("failure counts are %d (undispatchable) and %d (dispatched); the count is the "+
			"other half of the answer to \"how hard did we try?\"",
			poison.DispatchFailures, sent.DispatchFailures)
	}
}
