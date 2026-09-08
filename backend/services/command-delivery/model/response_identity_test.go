// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// TestMarkResponseRefusesADeviceAnsweringForAnother is the defect, written as a test.
//
// Before the response subject carried the responding device, any authenticated device in
// a tenant could publish a response naming any command token and this function stamped
// it. The command below belongs to pump-1; pump-9 answers for it.
func TestMarkResponseRefusesADeviceAnsweringForAnother(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "victim", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if _, claimed, err := api.MarkSent(ctx, created.ID); err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}

	_, err = api.MarkResponse(ctx, "victim", "pump-9", true, nil, nil)
	if !errors.Is(err, ErrResponderNotCommandOwner) {
		t.Fatalf("a device answering for another device returned %v, want ErrResponderNotCommandOwner", err)
	}

	// 🔴 THE ERROR IS NOT THE PROPERTY — THE UNCHANGED ROW IS. A refusal that still wrote
	// the row would satisfy the assertion above and leave the defect exactly as it was.
	got := loadOrFail(t, api, ctx, created.ID)
	if got.Status != CommandSent.String() {
		t.Fatalf("status = %s, want SENT untouched; another device settled this command", got.Status)
	}
	if got.RespondedTime.Valid {
		t.Fatal("a refused response still stamped RespondedTime")
	}
}

// TestMarkResponseAcceptsTheOwningDevice is the counterweight. Refusing responses is only
// safe while a real device's own answer still lands untouched — a guard that rejected
// everything would pass the test above and break every command in the platform.
func TestMarkResponseAcceptsTheOwningDevice(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "mine", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if _, claimed, err := api.MarkSent(ctx, created.ID); err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}

	got, err := api.MarkResponse(ctx, "mine", "pump-1", true, nil, nil)
	if err != nil {
		t.Fatalf("the owning device's response was refused: %v", err)
	}
	if got.Status != CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL", got.Status)
	}
}

// TestMarkResponseRefusesADeviceAnsweringForAnotherOnATerminalCommand pins that identity
// is checked BEFORE the terminal fast path.
//
// 🔑 THE FAST PATH IS AN EARLY RETURN, AND AN EARLY RETURN IS A WAY PAST A GUARD PLACED
// AFTER IT. Ordering them the other way would still refuse every forgery that mattered —
// no row changes either way on a terminal command — so no test of the WRITE can tell the
// two orderings apart. What differs is the report: a forged response would be answered
// with the command's current state and counted as an ordinary duplicate, so the one
// signal a fleet operator has that a device is answering for its neighbours would go
// quiet exactly when that device targets already-finished commands.
func TestMarkResponseRefusesADeviceAnsweringForAnotherOnATerminalCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "done", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if err := forceStatus(api, ctx, created.ID, CommandSuccessful); err != nil {
		t.Fatalf("forcing terminal: %v", err)
	}

	if _, err := api.MarkResponse(ctx, "done", "pump-9", true, nil, nil); !errors.Is(err, ErrResponderNotCommandOwner) {
		t.Fatalf("a foreign response to a TERMINAL command returned %v, want ErrResponderNotCommandOwner "+
			"— the identity check must sit above the terminal fast path", err)
	}
}

// TestMarkResponseRefusesStatesNoDispatcherHeld covers the other half of the guard: a
// device cannot settle a command that was never handed to a transport.
//
// This is not the forgery case — the device owns the command. It is the pre-emptive one:
// reporting success for an actuation still sitting in the queue closes the command out,
// so the platform stops trying to deliver it and the record says it ran.
//
// 🔴 IT USED TO ASSERT A NIL ERROR HERE, WHICH IS THE HALF OF THE GUARD THAT WAS MISSING.
// Leaving the row alone is necessary and was never in doubt; what the caller also needs is
// to be TOLD, because a nil error is what it acks on. Asserting only the status made this
// test pass against a function that refused the write and reported success, which is how a
// device's answer could be discarded with nothing recording it. The sentinel is asserted
// too, so a return to the silent shape fails here rather than in production.
func TestMarkResponseRefusesStatesNoDispatcherHeld(t *testing.T) {
	for _, status := range []CommandStatus{CommandQueued, CommandHeld} {
		t.Run(status.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")

			created, err := api.CreateCommand(ctx, &CommandCreateRequest{
				Token: "early", DeviceToken: "pump-1", Name: "reboot",
			})
			if err != nil {
				t.Fatalf("CreateCommand: %v", err)
			}
			if status != CommandQueued {
				if err := forceStatus(api, ctx, created.ID, status); err != nil {
					t.Fatalf("forcing %s: %v", status, err)
				}
			}

			_, err = api.MarkResponse(ctx, "early", "pump-1", true, nil, nil)
			if !errors.Is(err, ErrCommandNotAnswerable) {
				t.Fatalf("MarkResponse err = %v, want ErrCommandNotAnswerable; a caller told "+
					"nothing acks the message and the device's answer is gone", err)
			}
			// The status is named in the error because it is the only thing that separates a
			// released dispatch (QUEUED) from a presence hold (HELD), and by the time anyone
			// reads the dead letter the row has moved.
			if !strings.Contains(err.Error(), status.String()) {
				t.Fatalf("MarkResponse err = %q, want it to name the status %s", err, status)
			}
			got := loadOrFail(t, api, ctx, created.ID)
			if got.Status != status.String() {
				t.Fatalf("status = %s, want %s left alone — a device settled a command no "+
					"dispatcher had handed it", got.Status, status)
			}
		})
	}
}

// TestAnswerableStatusesAreDispatcherHeld pins the SET, not just its effects.
//
// 🔴 A LIST IS A SPECIFICATION, so this states the members independently rather than
// reading them back out of the function under test — an expectation derived from the
// production list is tautological and passes for any list at all.
func TestAnswerableStatusesAreDispatcherHeld(t *testing.T) {
	want := map[string]bool{"SENT": true, "PARKED": true}
	got := answerableStatusStrings()
	if len(got) != len(want) {
		t.Fatalf("answerable = %v, want exactly SENT and PARKED", got)
	}
	for _, s := range got {
		if !want[s] {
			t.Fatalf("answerable includes %q; a device may only answer a command a dispatcher held for it", s)
		}
	}
	// QUEUED and HELD are the states the old negative guard admitted by default, and
	// naming them here is what keeps the regression from being silent.
	for _, s := range got {
		if s == CommandQueued.String() || s == CommandHeld.String() {
			t.Fatalf("answerable includes %q, which no dispatcher has ever handed to a device", s)
		}
	}
}

// TestMarkResponseAcceptsARaceToTerminal is the counterweight to the test above, and it
// is the reason the non-match is CLASSIFIED rather than simply turned into an error.
//
// 🔴 RowsAffected==0 HAS TWO CAUSES AND ONLY ONE OF THEM IS A PROBLEM. The row can have
// moved FORWARD to a terminal state between MarkResponse's read and its write — a
// duplicate answer, an expiry, a cancel — which is an ordinary race the caller must go on
// treating as settled. Refusing that one would turn every such race into a dead letter and
// a warning, on a command that is already finished.
//
// It is not reachable through the public API alone: both statements live inside the one
// call, so the row has to be moved UNDER the read. The callback below does exactly that,
// once, which is why it is registered rather than the status being forced up front — a row
// forced terminal beforehand takes the fast path and never reaches the branch under test.
func TestMarkResponseAcceptsARaceToTerminal(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "raced-terminal", CommandQueued)
	if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
		t.Fatalf("staging the claim failed: claimed=%v err=%v", claimed, err)
	}

	// A plain flag rather than a sync.Once: gorm runs its callbacks on the calling
	// goroutine, and the test needs to READ afterwards whether the hook ever fired — a
	// once that silently never ran would leave this measuring the fast path instead.
	raced := false
	const hook = "test:settle_under_the_read"
	db := api.RDB.Database
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(*gorm.DB) {
		if raced {
			return
		}
		raced = true
		if err := forceStatus(api, ctx, id, CommandCancelled); err != nil {
			t.Errorf("settling the row under the read: %v", err)
		}
	}); err != nil {
		t.Fatalf("registering the race hook: %v", err)
	}
	defer func() {
		if err := db.Callback().Query().Remove(hook); err != nil {
			t.Errorf("removing the race hook: %v", err)
		}
	}()

	got, err := api.MarkResponse(ctx, "raced-terminal", "d", true, nil, nil)
	if err != nil {
		t.Fatalf("a response that lost a race to a terminal state must settle quietly, got %v", err)
	}
	if got.Status != CommandCancelled.String() {
		t.Fatalf("status = %s, want CANCELLED — the caller is handed the row as it now is", got.Status)
	}
	// The premise: the snapshot MarkResponse read really was answerable, so this exercised
	// the zero-match branch rather than the terminal fast path above it.
	if !raced {
		t.Fatal("premise lost: the hook never fired, so nothing raced the write")
	}
}
