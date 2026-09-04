// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// The gap this closes: a command whose response was dead-lettered stays SENT with no
// writer left to move it. The write-back has to reach a TERMINAL state, and it has to be
// distinguishable from a device's own failure — otherwise the record still lies, only in
// a different direction.
func TestMarkResponseLostSettlesASentCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedWithStatus(t, api, ctx, "cmd-sent", CommandSent)

	settled, err := api.MarkResponseLost(ctx, "cmd-sent")
	if err != nil {
		t.Fatalf("MarkResponseLost: %v", err)
	}
	if !settled {
		t.Fatal("MarkResponseLost reported no transition on a SENT command; the command " +
			"would read as in flight until its TTL and then lapse to TIMEOUT, blaming a " +
			"device that answered — which is the whole defect")
	}
	assertStatus(t, api, ctx, "cmd-sent", CommandFailed)

	matches, err := api.CommandsByToken(ctx, []string{"cmd-sent"})
	if err != nil || len(matches) != 1 {
		t.Fatalf("reading back cmd-sent: %v (%d rows)", err, len(matches))
	}
	if !matches[0].Error.Valid || matches[0].Error.String != ResponseLostReason {
		t.Fatalf("error column = %#v, want the fixed lost-response reason. Without it a "+
			"FAILED row is indistinguishable from a device reporting its own failure",
			matches[0].Error)
	}
	// A response was never recorded, so there is no responded time to claim.
	if matches[0].RespondedTime.Valid {
		t.Fatal("responded_time was stamped for a response the platform never recorded")
	}
}

// PARKED is answerable (a park can land on a row the device really did run), so a lost
// answer to a parked command is the same loss and must settle the same way.
func TestMarkResponseLostSettlesAParkedCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedWithStatus(t, api, ctx, "cmd-parked", CommandParked)

	settled, err := api.MarkResponseLost(ctx, "cmd-parked")
	if err != nil {
		t.Fatalf("MarkResponseLost: %v", err)
	}
	if !settled {
		t.Fatal("MarkResponseLost declined a PARKED command; PARKED is in the answerable " +
			"set, so a response to it can be lost exactly as a SENT one can")
	}
	assertStatus(t, api, ctx, "cmd-parked", CommandFailed)
}

// 🔴 THE QUESTION A LATE DEAD LETTER RAISES. The letter is written at the redelivery cap
// and consumed minutes later; by then the command may have been answered by a redelivery,
// expired by the sweep or called off by a human. Every one of those is a real outcome and
// none of them may be overwritten by a record of a failure to record.
func TestMarkResponseLostNeverClobbersARealTerminal(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	for _, status := range []CommandStatus{
		CommandSuccessful, CommandFailed, CommandTimeout, CommandExpired, CommandCancelled,
	} {
		token := "cmd-" + status.String()
		seedWithStatus(t, api, ctx, token, status)
		settled, err := api.MarkResponseLost(ctx, token)
		if err != nil {
			t.Fatalf("MarkResponseLost on %s: %v", status, err)
		}
		if settled {
			t.Fatalf("MarkResponseLost reported a transition on an already-%s command", status)
		}
		assertStatus(t, api, ctx, token, status)
		matches, err := api.CommandsByToken(ctx, []string{token})
		if err != nil || len(matches) != 1 {
			t.Fatalf("reading back %s: %v", token, err)
		}
		if matches[0].Error.Valid {
			t.Fatalf("MarkResponseLost wrote an error onto an already-%s command", status)
		}
	}
}

// The mirror of the terminal case, and the one a "not terminal" guard would have got
// wrong: a row back in QUEUED (a released claim) or HELD is a LIVE command the platform
// still intends to deliver. Failing it on the strength of an answer to an earlier
// dispatch would cancel work that is still going to happen.
func TestMarkResponseLostDeclinesACommandBackInFlight(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	for _, status := range []CommandStatus{CommandQueued, CommandHeld} {
		token := "cmd-live-" + status.String()
		seedWithStatus(t, api, ctx, token, status)
		settled, err := api.MarkResponseLost(ctx, token)
		if err != nil {
			t.Fatalf("MarkResponseLost on %s: %v", status, err)
		}
		if settled {
			t.Fatalf("MarkResponseLost failed a %s command that the platform still intends "+
				"to deliver", status)
		}
		assertStatus(t, api, ctx, token, status)
	}
}

// The write is tenant-scoped by the callbacks, and this is the assertion that says so:
// another tenant's command with the same token is not reachable from here. Command tokens
// are client-chosen, so a collision across tenants is ordinary rather than exotic.
func TestMarkResponseLostCannotReachAnotherTenantsCommand(t *testing.T) {
	api := newTestApi(t)
	ctxA := core.WithTenant(context.Background(), "A")
	ctxB := core.WithTenant(context.Background(), "B")
	seedWithStatus(t, api, ctxA, "shared-token", CommandSent)
	seedWithStatus(t, api, ctxB, "shared-token", CommandSent)

	settled, err := api.MarkResponseLost(ctxA, "shared-token")
	if err != nil {
		t.Fatalf("MarkResponseLost: %v", err)
	}
	if !settled {
		t.Fatal("tenant A's own command was not settled")
	}
	assertStatus(t, api, ctxA, "shared-token", CommandFailed)
	assertStatus(t, api, ctxB, "shared-token", CommandSent)
}

// An empty reference is refused rather than turned into an unscoped UPDATE. The envelope
// field is optional in the wire type, so this is a reachable input, and an empty token
// against a table where nothing carries an empty token would match nothing today — which
// is exactly the kind of accidentally-safe that stops being safe.
func TestMarkResponseLostRefusesAnEmptyToken(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	if _, err := api.MarkResponseLost(ctx, "   "); err == nil {
		t.Fatal("MarkResponseLost accepted a blank command token")
	}
}

// Fail-closed: no tenant in context is a refusal, not an unscoped write.
func TestMarkResponseLostRefusesWithNoTenant(t *testing.T) {
	api := newTestApi(t)
	if _, err := api.MarkResponseLost(context.Background(), "cmd-x"); err == nil {
		t.Fatal("MarkResponseLost ran with no tenant in context")
	}
}
