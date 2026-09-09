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

	_, err = api.MarkResponse(ctx, "victim", "pump-9", nonceOf(t, api, ctx, created.ID), true, nil, nil)
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

	got, err := api.MarkResponse(ctx, "mine", "pump-1", nonceOf(t, api, ctx, created.ID), true, nil, nil)
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

	// A non-empty nonce, so the refusal under test is the IDENTITY one. With an empty nonce
	// this would still fail — the nonce guard sits just below the identity check — and the
	// test would then pass without the guard it names ever running.
	if _, err := api.MarkResponse(ctx, "done", "pump-9", "any-dispatch", true, nil, nil); !errors.Is(err, ErrResponderNotCommandOwner) {
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
//
// 🔴 WHAT REFUSES IT IS NOW THE NONCE, NOT THE STATUS, AND THE TEST STATES BOTH SHAPES. A
// command nothing has dispatched carries no dispatch nonce, so there is no value a device
// can echo that matches it: an answer naming a dispatch is refused because the row is on
// none, and an answer naming nothing is refused before the row's state is consulted at all.
// The property this test has always been about — a queued actuation cannot be closed out by
// a device that was never sent it — is unchanged; what changed is that it no longer rests on
// the status alone, which is what let the same status mean two different dispatches.
func TestMarkResponseRefusesStatesNoDispatcherHeld(t *testing.T) {
	answers := []struct {
		name  string
		nonce string
		want  error
	}{
		{"naming a dispatch the command is not on", "some-dispatch", ErrResponseNonceMismatch},
		{"naming no dispatch at all", "", ErrResponseMissingNonce},
	}
	for _, status := range []CommandStatus{CommandQueued, CommandHeld} {
		for _, answer := range answers {
			t.Run(status.String()+"/"+answer.name, func(t *testing.T) {
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

				_, err = api.MarkResponse(ctx, "early", "pump-1", answer.nonce, true, nil, nil)
				if !errors.Is(err, answer.want) {
					t.Fatalf("MarkResponse err = %v, want %v; a caller told nothing acks the "+
						"message and the device's answer is gone", err, answer.want)
				}
				// The status at the write is named whenever the row was consulted, because it
				// is the only thing that separates a released dispatch (QUEUED) from a
				// presence hold (HELD), and by the time anyone reads the dead letter the row
				// has moved. A refusal that never looked at the row cannot name one.
				if answer.want == ErrResponseNonceMismatch && !strings.Contains(err.Error(), status.String()) {
					t.Fatalf("MarkResponse err = %q, want it to name the status %s", err, status)
				}
				got := loadOrFail(t, api, ctx, created.ID)
				if got.Status != status.String() {
					t.Fatalf("status = %s, want %s left alone — a device settled a command no "+
						"dispatcher had handed it", got.Status, status)
				}
				if got.RespondedTime.Valid {
					t.Fatal("a refused response still stamped RespondedTime")
				}
			})
		}
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
	// The nonce comes from the claim rather than from a read-back, because the hook
	// registered below fires on every query: reading the row here would settle it before the
	// call under test ran, and this test would measure the terminal fast path instead.
	nonce, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
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

	got, err := api.MarkResponse(ctx, "raced-terminal", "d", nonce, true, nil, nil)
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

// TestMarkResponseRefusesARowReclaimedUnderTheReRead covers the interleaving where the
// re-read finds an ANSWERABLE status, which is the one case where the refusal has to
// explain itself carefully.
//
// The answer names a dispatch that has been released, so the write matches nothing. Before
// the re-read, a dispatcher claims the row — the sweep re-dispatching a command that a
// failed publish returned to the queue — stamping a NEW nonce. `current` therefore reads
// SENT, on a dispatch this answer is not for.
//
// 🔑 IT IS STILL REFUSED, AND THAT IS THE DELIBERATE CALL. Settling on the strength of the
// status would close out the SECOND dispatch with the FIRST dispatch's answer, and the real
// answer to the second would then arrive on a terminal row and be dropped as late. This is
// the whole reason the nonce is on the return path, and the reason it is the nonce and not
// the status that decides: both dispatches leave the row reading SENT.
//
// 🔴 AND THE MESSAGE MUST NOT CONTRADICT ITSELF. Reporting only the status NOW would read
// "could not be settled ... it reads SENT" to whoever opens the dead letter.
func TestMarkResponseRefusesARowReclaimedUnderTheReRead(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	// The released dispatch, in full: claimed, its publish reported an error, returned to the
	// queue carrying the nonce it went out under. Staged through the real transitions rather
	// than forced, because the nonce surviving the release is what the answer names.
	id := seedWithStatus(t, api, ctx, "reclaimed", CommandQueued)
	released, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("staging the first dispatch: claimed=%v err=%v", claimed, err)
	}
	if landed, moved, err := api.ReleaseClaim(ctx, id); err != nil || !moved || landed != CommandQueued {
		t.Fatalf("releasing the first dispatch: landed=%v moved=%v err=%v", landed, moved, err)
	}

	// Fire once, on the read MarkResponse opens with, so the SECOND dispatch lands between
	// that read and the write. That is the real sequence: the answer to the released dispatch
	// is in flight while the sweep re-dispatches the command. The row is therefore SENT under
	// a NEW nonce when the write runs, so the write matches nothing, and the snapshot the
	// error reports still reads QUEUED.
	reclaimed := false
	const hook = "test:reclaim_under_the_read"
	db := api.RDB.Database
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(*gorm.DB) {
		if reclaimed {
			return
		}
		reclaimed = true
		if _, claimed, err := api.MarkSent(ctx, id); err != nil || !claimed {
			t.Errorf("re-claiming under the read: claimed=%v err=%v", claimed, err)
		}
	}); err != nil {
		t.Fatalf("registering the re-claim hook: %v", err)
	}
	defer func() {
		if err := db.Callback().Query().Remove(hook); err != nil {
			t.Errorf("removing the re-claim hook: %v", err)
		}
	}()

	_, err = api.MarkResponse(ctx, "reclaimed", "d", released, true, nil, nil)
	if !errors.Is(err, ErrResponseNonceMismatch) {
		t.Fatalf("MarkResponse err = %v, want ErrResponseNonceMismatch; this answer belongs to "+
			"the dispatch that was released, not to the one now in flight", err)
	}
	if !reclaimed {
		t.Fatal("premise lost: the hook never fired, so nothing re-claimed the row")
	}
	// Both statuses, or the sentence argues against itself.
	if !strings.Contains(err.Error(), CommandQueued.String()) ||
		!strings.Contains(err.Error(), CommandSent.String()) {
		t.Fatalf("MarkResponse err = %q; it must name the status at the write (QUEUED) as well "+
			"as the one now (SENT), or it reads as refusing an answerable command", err)
	}
	// The re-claim stands: nothing about the refusal may disturb the dispatch in flight.
	if got := loadOrFail(t, api, ctx, id); got.Status != CommandSent.String() {
		t.Fatalf("status = %s, want SENT; the refusal must not touch the new dispatch", got.Status)
	}
}
