// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// TestParkClaimHandsBackAnUndeliveredCommand is the core of the transition: a command the
// transport could not deliver goes SENT -> PARKED, and its sent_time is cleared because
// nothing was sent.
func TestParkClaimHandsBackAnUndeliveredCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "park-me", CommandQueued)
	nonce, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}

	parked, err := api.ParkClaim(ctx, "park-me", nonce)
	if err != nil {
		t.Fatalf("ParkClaim: %v", err)
	}
	if !parked {
		t.Fatal("ParkClaim reported it moved nothing, but the row was SENT on the nonce it was given")
	}
	if got := statusByToken(t, api, ctx, "park-me"); got != CommandParked.String() {
		t.Fatalf("status = %s, want PARKED", got)
	}

	matches, err := api.CommandsByToken(ctx, []string{"park-me"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if matches[0].SentTime.Valid {
		t.Fatal("a parked command still carries a sent_time; it was never sent, and leaving the " +
			"stamp would report a delivery that did not happen")
	}
}

// TestParkClaimRefusesAStaleNonce is THE safety test of this design, and the scenario it
// stages is ordinary rather than exotic.
//
// A park request can be a redelivery of a publish whose command has since been re-claimed by
// a wake drain and ACTUATED. Before the dispatch nonce existed, a park predicated only on
// `status = 'SENT'` would MATCH that freshly-actuated row, drag it back into the dispatchable
// set, and let the next wake actuate the device a second time. Matching the nonce means the
// stale request names a dispatch that no longer exists.
//
// 🔴 The assertion is on the row's STATUS, not merely on the boolean. A ParkClaim that
// returned false while still writing something would pass a boolean-only check.
func TestParkClaimRefusesAStaleNonce(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "re-armed", CommandQueued)

	// The publish that is about to be redelivered. Its nonce is captured here.
	stale, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("first MarkSent: claimed=%v err=%v", claimed, err)
	}

	// The device woke, the drain claimed the row and actuated it. That claim re-stamps the
	// nonce, which is what makes the in-flight park stale.
	if _, err := api.ParkClaim(ctx, "re-armed", stale); err != nil {
		t.Fatalf("park to set up the drain: %v", err)
	}
	won, err := api.MarkSentByToken(ctx, "re-armed")
	if err != nil || !won {
		t.Fatalf("drain claim: won=%v err=%v", won, err)
	}

	// The original message now redelivers and tries to park the dispatch it was handed.
	parked, err := api.ParkClaim(ctx, "re-armed", stale)
	if err != nil {
		t.Fatalf("ParkClaim: %v", err)
	}
	if parked {
		t.Fatal("a stale park reported success; it named a dispatch that had been superseded")
	}
	if got := statusByToken(t, api, ctx, "re-armed"); got != CommandSent.String() {
		t.Fatalf("status = %s, want SENT — a stale park pulled an ACTUATED command back into the "+
			"dispatchable set, and the device's next wake would run it a second time", got)
	}
}

// TestMarkSentStampsADistinctNonceEachTime is the property the test above rests on. Two
// claims of the same command must not share a nonce, or a stale park would match the row it
// is supposed to miss.
//
// 🔑 It also pins that the nonce is only handed out on a WON claim: a caller that published
// on a lost claim's empty nonce would publish something no park could ever name.
func TestMarkSentStampsADistinctNonceEachTime(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "twice", CommandQueued)

	first, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("first MarkSent: claimed=%v err=%v", claimed, err)
	}
	if first == "" {
		t.Fatal("a won claim handed back an empty nonce; the publish it authorises could never be parked")
	}

	// A second claim loses — the row is no longer claimable — and must yield no nonce.
	lost, claimed, err := api.MarkSent(ctx, id)
	if err != nil {
		t.Fatalf("second MarkSent: %v", err)
	}
	if claimed {
		t.Fatal("MarkSent claimed an already-SENT command twice")
	}
	if lost != "" {
		t.Fatalf("a LOST claim handed back nonce %q; nothing was dispatched, so there is no dispatch to name", lost)
	}

	// Park it and claim again: this time the claim wins, and the nonce must differ.
	if _, err := api.ParkClaim(ctx, "twice", first); err != nil {
		t.Fatalf("park: %v", err)
	}
	second, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("third MarkSent: claimed=%v err=%v", claimed, err)
	}
	if second == first {
		t.Fatal("two dispatches of one command share a nonce; a park for the first would match the second")
	}
}

// TestParkClaimRetiresACancelledBatchsCommand covers the branch ParkClaim shares with
// ReleaseClaim, and without it the park would be a hole in the brake.
//
// The sequence: an operator cancels a fleet write; one of its commands is already SENT, so
// the cancel correctly passes over it and reports it as alreadySent; the transport then finds
// the device unreachable. Parking it plainly would put a CANCELLED batch's command back in
// the dispatchable set, and the next wake would actuate a device the operator was told had
// been called off. A park of a cancelled batch's command therefore lands on CANCELLED.
func TestParkClaimRetiresACancelledBatchsCommand(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "fleet-1", map[string]string{
		"pump-1": CommandQueued.String(),
	})
	// Claim it, exactly as the sweep would, so it is SENT with a live nonce.
	matches, err := api.CommandsByToken(ctx, []string{"cmd-pump-1"})
	if err != nil {
		t.Fatalf("read seeded command: %v", err)
	}
	nonce, claimed, err := api.MarkSent(ctx, matches[0].ID)
	if err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}

	if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
		t.Fatalf("CancelCommandBatch: %v", err)
	}

	parked, err := api.ParkClaim(ctx, "cmd-pump-1", nonce)
	if err != nil {
		t.Fatalf("ParkClaim: %v", err)
	}
	if !parked {
		t.Fatal("ParkClaim moved nothing for a cancelled batch's SENT command")
	}
	if got := statusByToken(t, api, ctx, "cmd-pump-1"); got != CommandCancelled.String() {
		t.Fatalf("status = %s, want CANCELLED — parking a called-off batch's command to PARKED "+
			"would put it back in the dispatchable set and actuate the device after the brake", got)
	}
}

// TestCancelBatchTakesAParkedCommand is the F1 guard, and the defect it pins was found in
// review rather than by any test.
//
// The batch cancel runs several passes so a command released back into the dispatchable set
// between the UPDATE and the recount is caught by the next one. Its convergence check used to
// be the literal expression `counts[QUEUED] + counts[HELD]` — an expression PARALLEL to the
// cancellable list rather than derived from it. Adding PARKED to the list without touching
// that expression would make a snapshot of {PARKED:1, QUEUED:0, HELD:0} read as converged: the
// loop breaks, the transaction commits with a dispatchable row inside a cancelled batch, and
// the next wake actuates it after the brake was pulled.
//
// 🔴 The assertion is on the ROW, not on the reported counts. A cancel that reported one
// cancelled command while leaving the row PARKED would satisfy a count-only test.
func TestCancelBatchTakesAParkedCommand(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "fleet-2", map[string]string{
		"pump-1": CommandParked.String(),
		"pump-2": CommandSent.String(),
	})

	result, err := api.CancelCommandBatch(ctx, batch)
	if err != nil {
		t.Fatalf("CancelCommandBatch: %v", err)
	}

	if got := statusByToken(t, api, ctx, "cmd-pump-1"); got != CommandCancelled.String() {
		t.Fatalf("the PARKED command is %s, want CANCELLED — it went nowhere and is fully "+
			"recallable, so a brake that leaves it dispatchable is not a brake", got)
	}
	if got := statusByToken(t, api, ctx, "cmd-pump-2"); got != CommandSent.String() {
		t.Fatalf("the SENT command is %s, want it left SENT — it is at the device", got)
	}
	if result.Cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1 (the parked one)", result.Cancelled)
	}
	if result.AlreadySent != 1 {
		t.Fatalf("alreadySent = %d, want 1 (the sent one)", result.AlreadySent)
	}
}

// TestStillCancellableCountsEveryCancellableState is the direct guard on the convergence
// helper, and it exists because the test above cannot distinguish "the loop converged
// correctly" from "the loop happened to catch it on pass one".
//
// 🔑 The expectation is written from the STATES, not from cancellableStatusStrings(), so a
// state silently dropped from that list fails here rather than making both sides agree.
func TestStillCancellableCountsEveryCancellableState(t *testing.T) {
	counts := map[string]int{
		CommandQueued.String():    1,
		CommandHeld.String():      2,
		CommandParked.String():    4,
		CommandSent.String():      8,
		CommandCancelled.String(): 16,
	}
	if got := stillCancellable(counts); got != 7 {
		t.Fatalf("stillCancellable = %d, want 7 (QUEUED 1 + HELD 2 + PARKED 4); SENT and terminal "+
			"rows are not cancellable and must not keep the loop running", got)
	}
	// The failure this is really defending: a snapshot holding ONLY parked rows must not read
	// as converged.
	if got := stillCancellable(map[string]int{CommandParked.String(): 1}); got != 1 {
		t.Fatalf("a snapshot of one PARKED row reported %d still-cancellable; the cancel loop would "+
			"break and commit a dispatchable row inside a cancelled batch", got)
	}
}

// TestParkedCommandsCountAgainstTheCeiling pins the governance decision.
//
// The ceiling's own argument is that its counted set must be INVARIANT UNDER PROMOTION, or a
// tenant enqueues freely while rows drain out of the count. QUEUED -> SENT -> PARKED is
// exactly such a promotion and it completes within a tick for a queue-mode fleet, so leaving
// PARKED uncounted would let a tenant with a sleeping fleet fill its ceiling, watch the count
// fall to zero, and refill — every tick, bounded only by the command TTL.
func TestParkedCommandsCountAgainstTheCeiling(t *testing.T) {
	if !containsStatus(undeliveredStatusStrings(), CommandParked) {
		t.Fatal("PARKED is not counted against the undelivered ceiling; a tenant whose fleet sleeps " +
			"could enqueue without bound, which is the hole the counted set exists to close")
	}
	// SENT stays out, and for a different reason: that work is no longer the platform's to
	// hold. Asserting it here keeps the two decisions from being conflated later.
	if containsStatus(undeliveredStatusStrings(), CommandSent) {
		t.Fatal("SENT is counted against the undelivered ceiling; a command at the device is not " +
			"the tenant holding work open")
	}
}

// TestParkedCommandExpiresRatherThanTimingOut is the false-TIMEOUT fix, asserted end to end
// rather than on the mapping function alone.
//
// TIMEOUT means "the device was reached and never answered". A parked command reached
// nothing, so recording TIMEOUT blames the device for the platform's own undelivered command
// — and this fires on the ordinary expiry path, with no fleet write or cancel involved.
func TestParkedCommandExpiresRatherThanTimingOut(t *testing.T) {
	if got := expiredTerminalFor(CommandParked.String()); got != CommandExpired.String() {
		t.Fatalf("a lapsed PARKED command records %s, want EXPIRED — it never reached a device, so "+
			"TIMEOUT would blame hardware for a delivery that never happened", got)
	}
	// The counterweight: SENT must still map to TIMEOUT, or the distinction is lost in the
	// other direction and a genuinely unanswered command reads as never sent.
	if got := expiredTerminalFor(CommandSent.String()); got != CommandTimeout.String() {
		t.Fatalf("a lapsed SENT command records %s, want TIMEOUT", got)
	}
}

// TestParkClaimIsANoOpOnACommandThatMovedOn covers the settled-false contract the transport
// depends on. RowsAffected 0 is an OUTCOME, not a failure: the row was answered, cancelled or
// expired under the park. A caller that read it as failure would retry until its redelivery
// budget ran out, on a row already where it should be.
func TestParkClaimIsANoOpOnACommandThatMovedOn(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id := seedWithStatus(t, api, ctx, "answered", CommandQueued)
	nonce, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}
	if _, err := api.MarkResponse(ctx, "answered", true, nil, nil); err != nil {
		t.Fatalf("MarkResponse: %v", err)
	}

	parked, err := api.ParkClaim(ctx, "answered", nonce)
	if err != nil {
		t.Fatalf("parking a command that already finished must not error: %v", err)
	}
	if parked {
		t.Fatal("ParkClaim reported it moved a command that had already been answered")
	}
	if got := statusByToken(t, api, ctx, "answered"); got != CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL left intact", got)
	}
}

// TestMarkResponseAcceptsAParkedCommand pins the free mitigation for the residual this design
// does NOT close.
//
// Parking is not proof the op never ran: the request may be a redelivery of a publish that
// did reach the device before its connection lapsed. PARKED is non-terminal, so a late answer
// still lands and terminalizes the row. Had PARKED been made terminal "because the command
// went nowhere", that answer would have been discarded — the same defect cancelling from SENT
// used to cause.
func TestMarkResponseAcceptsAParkedCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedWithStatus(t, api, ctx, "late-answer", CommandParked)

	got, err := api.MarkResponse(ctx, "late-answer", true, nil, nil)
	if err != nil {
		t.Fatalf("MarkResponse: %v", err)
	}
	if got.Status != CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL — a device's real answer to a parked command must "+
			"still land, or parking would discard the truth the way cancelling from SENT did", got.Status)
	}
}

// containsStatus is a local helper so the ceiling assertions read as prose. It takes the wire
// form because that is what the derived lists hold.
func containsStatus(set []string, status CommandStatus) bool {
	for _, s := range set {
		if s == status.String() {
			return true
		}
	}
	return false
}
