// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// commandRow reads one command back whole, so a test can compare every column it cares about
// rather than only the status.
func commandRow(t *testing.T, api *Api, ctx context.Context, token string) *Command {
	t.Helper()
	matches, err := api.CommandsByToken(ctx, []string{token})
	if err != nil {
		t.Fatalf("read %s: %v", token, err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 row for %s, got %d", token, len(matches))
	}
	return matches[0]
}

// sentWithNonce seeds a command and claims it the way the delivery sweep does, answering the
// nonce the published envelope would carry.
func sentWithNonce(t *testing.T, api *Api, ctx context.Context, token string) (uint, string) {
	t.Helper()
	id := seedWithStatus(t, api, ctx, token, CommandQueued)
	nonce, claimed, err := api.MarkSent(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("MarkSent %s: claimed=%v err=%v", token, claimed, err)
	}
	return id, nonce
}

// TestConfirmDispatchRotatesTheNonceAndRestampsSentTime is the won branch, asserted on the
// VALUES: the answer is a new nonce, the row now carries exactly that nonce, and its sent_time
// was restamped at the confirmation.
func TestConfirmDispatchRotatesTheNonceAndRestampsSentTime(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id, n := sentWithNonce(t, api, ctx, "confirm-me")
	backdateSentTime(t, api, ctx, id, time.Hour)
	before := time.Now()

	n2, won, err := api.ConfirmDispatch(ctx, "confirm-me", n)
	if err != nil || !won {
		t.Fatalf("ConfirmDispatch: won=%v err=%v", won, err)
	}
	if n2 == "" || n2 == n {
		t.Fatalf("ConfirmDispatch answered %q for a row on %q; it must rotate to a NEW nonce, or "+
			"every later copy of the same envelope would confirm too", n2, n)
	}
	row := commandRow(t, api, ctx, "confirm-me")
	if row.Status != CommandSent.String() {
		t.Fatalf("status = %s, want SENT", row.Status)
	}
	if !row.DispatchNonce.Valid || row.DispatchNonce.String != n2 {
		t.Fatalf("row nonce = %v, want the answered %q", row.DispatchNonce, n2)
	}
	if !row.SentTime.Valid || row.SentTime.Time.Before(before.Add(-time.Second)) {
		t.Fatalf("sent_time = %v, want restamped at the confirmation (>= %v)", row.SentTime, before)
	}
}

// TestConfirmDispatchRefusesAStaleNonce: an envelope naming a dispatch the row has moved off
// loses, and the row is left exactly as it was.
func TestConfirmDispatchRefusesAStaleNonce(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	_, n := sentWithNonce(t, api, ctx, "stale")
	before := commandRow(t, api, ctx, "stale")

	n2, won, err := api.ConfirmDispatch(ctx, "stale", n+"-superseded")
	if err != nil {
		t.Fatalf("ConfirmDispatch: %v", err)
	}
	if won || n2 != "" {
		t.Fatalf("a stale nonce confirmed (won=%v, nonce=%q); the late envelope would actuate", won, n2)
	}
	after := commandRow(t, api, ctx, "stale")
	if after.Status != before.Status || after.DispatchNonce != before.DispatchNonce ||
		!after.SentTime.Time.Equal(before.SentTime.Time) {
		t.Fatalf("a lost confirm changed the row: before %s/%v/%v, after %s/%v/%v",
			before.Status, before.DispatchNonce, before.SentTime, after.Status, after.DispatchNonce, after.SentTime)
	}
}

// TestConfirmDispatchRefusesAParkedRowStillCarryingItsNonce pins why `status = 'SENT'` is in
// the predicate. ParkClaim keeps the nonce, so a PARKED row still carries N; a confirm on the
// nonce alone would match it and actuate a command the wake drain is also about to claim.
func TestConfirmDispatchRefusesAParkedRowStillCarryingItsNonce(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	_, n := sentWithNonce(t, api, ctx, "parked")
	if _, parked, err := api.ParkClaim(ctx, "parked", n); err != nil || !parked {
		t.Fatalf("ParkClaim: parked=%v err=%v", parked, err)
	}
	if row := commandRow(t, api, ctx, "parked"); row.DispatchNonce.String != n {
		t.Fatalf("fixture premise: a parked row keeps its nonce, got %v want %q", row.DispatchNonce, n)
	}

	_, won, err := api.ConfirmDispatch(ctx, "parked", n)
	if err != nil {
		t.Fatalf("ConfirmDispatch: %v", err)
	}
	if won {
		t.Fatal("a PARKED row carrying the envelope's nonce was confirmed; the live copy and the " +
			"wake drain would both actuate it")
	}
	if got := statusByToken(t, api, ctx, "parked"); got != CommandParked.String() {
		t.Fatalf("status = %s, want PARKED", got)
	}
}

// TestStrandedParkQuotingTheScannedNonceLosesToAConfirm is the race the rotation exists for: the
// stranded pass scanned (row, N), the live transport confirmed first, and the pass's park then
// lands. It must match nothing, because the row is now on N2 and being actuated.
func TestStrandedParkQuotingTheScannedNonceLosesToAConfirm(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id, n := sentWithNonce(t, api, ctx, "raced")
	backdateSentTime(t, api, ctx, id, time.Hour)
	scanned, _, err := api.StrandedSentCommands(ctx, StrandedCursor{}, time.Now().Add(-time.Minute), 10)
	if err != nil || len(scanned) != 1 || scanned[0].DispatchNonce.String != n {
		t.Fatalf("fixture scan: %v err=%v", tokensOf(scanned), err)
	}

	n2, won, err := api.ConfirmDispatch(ctx, "raced", n)
	if err != nil || !won {
		t.Fatalf("ConfirmDispatch: won=%v err=%v", won, err)
	}

	_, parked, err := api.ParkClaim(ctx, "raced", scanned[0].DispatchNonce.String)
	if err != nil {
		t.Fatalf("ParkClaim: %v", err)
	}
	if parked {
		t.Fatal("the stranded pass parked a row the live transport had just confirmed; the drain " +
			"would actuate it a second time")
	}
	row := commandRow(t, api, ctx, "raced")
	if row.Status != CommandSent.String() || row.DispatchNonce.String != n2 {
		t.Fatalf("row = %s/%v, want SENT on the confirmed nonce %q", row.Status, row.DispatchNonce, n2)
	}
}

// TestConfirmedRowLeavesTheStrandedHorizon: a late envelope's row keeps an old sent_time, so
// without the restamp the next scan would find it past the horizon and park it while the op it
// was just confirmed for is still in flight.
func TestConfirmedRowLeavesTheStrandedHorizon(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	id, n := sentWithNonce(t, api, ctx, "late")
	backdateSentTime(t, api, ctx, id, time.Hour)
	horizon := time.Now().Add(-10 * time.Minute)

	// Negative control: before the confirm the row IS stranded.
	found, _, err := api.StrandedSentCommands(ctx, StrandedCursor{}, horizon, 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("control: an hour-old SENT row must be past the horizon, got %v err=%v", tokensOf(found), err)
	}

	if _, won, err := api.ConfirmDispatch(ctx, "late", n); err != nil || !won {
		t.Fatalf("ConfirmDispatch: won=%v err=%v", won, err)
	}
	found, _, err = api.StrandedSentCommands(ctx, StrandedCursor{}, horizon, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a row confirmed moments ago is still reported stranded (%v); the stranded pass "+
			"would park it under the op and the drain would re-actuate it", tokensOf(found))
	}
}

// TestConfirmOnACalledOffBatchLandsCancelled: an LwM2M command published but not yet actuated
// when its batch was called off stops at the confirmation, and the record says CANCELLED.
func TestConfirmOnACalledOffBatchLandsCancelled(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "fleet-1", map[string]string{"pump-1": CommandQueued.String()})
	row := commandRow(t, api, ctx, "cmd-pump-1")
	n, claimed, err := api.MarkSent(ctx, row.ID)
	if err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}
	if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
		t.Fatalf("CancelCommandBatch: %v", err)
	}

	n2, won, err := api.ConfirmDispatch(ctx, "cmd-pump-1", n)
	if err != nil {
		t.Fatalf("ConfirmDispatch: %v", err)
	}
	if won || n2 != "" {
		t.Fatal("a called-off batch's command was confirmed for actuation")
	}
	if got := statusByToken(t, api, ctx, "cmd-pump-1"); got != CommandCancelled.String() {
		t.Fatalf("status = %s, want CANCELLED", got)
	}
}

// TestRotationRefusesACalledOffBatchEvenAfterTheBrakeWriteMissed stages the interleaving the
// folded predicate closes: the brake write ran while the batch was still live, and the cancel
// committed before the rotation. The rotation must match nothing, and the row stays SENT on its
// old nonce for the stranded pass to retire through the brake.
func TestRotationRefusesACalledOffBatchEvenAfterTheBrakeWriteMissed(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	batch := seedBatchWithCommands(t, api, ctx, "fleet-2", map[string]string{"pump-2": CommandQueued.String()})
	row := commandRow(t, api, ctx, "cmd-pump-2")
	n, claimed, err := api.MarkSent(ctx, row.ID)
	if err != nil || !claimed {
		t.Fatalf("MarkSent: claimed=%v err=%v", claimed, err)
	}
	if _, err := api.CancelCommandBatch(ctx, batch); err != nil {
		t.Fatalf("CancelCommandBatch: %v", err)
	}

	_, won, err := api.rotateDispatch(ctx, "token = ? AND dispatch_nonce = ?", []any{"cmd-pump-2", n})
	if err != nil {
		t.Fatalf("rotateDispatch: %v", err)
	}
	if won {
		t.Fatal("the rotation confirmed a command whose batch was called off; the brake has a gap")
	}
	after := commandRow(t, api, ctx, "cmd-pump-2")
	if after.Status != CommandSent.String() || after.DispatchNonce.String != n {
		t.Fatalf("row = %s/%v, want untouched SENT on %q", after.Status, after.DispatchNonce, n)
	}

	// Counterweight: a command in a LIVE batch still rotates, so the predicate is not simply
	// refusing every batched command.
	seedBatchWithCommands(t, api, ctx, "fleet-3", map[string]string{"pump-3": CommandQueued.String()})
	row3 := commandRow(t, api, ctx, "cmd-pump-3")
	n3, claimed, err := api.MarkSent(ctx, row3.ID)
	if err != nil || !claimed {
		t.Fatalf("MarkSent pump-3: claimed=%v err=%v", claimed, err)
	}
	if _, won, err := api.rotateDispatch(ctx, "token = ? AND dispatch_nonce = ?", []any{"cmd-pump-3", n3}); err != nil || !won {
		t.Fatalf("a live batch's command must still rotate: won=%v err=%v", won, err)
	}
}

// TestConfirmDispatchRefusesAnEmptyNonce: an envelope with no nonce names no dispatch.
func TestConfirmDispatchRefusesAnEmptyNonce(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	_, n := sentWithNonce(t, api, ctx, "blank")
	if _, won, err := api.ConfirmDispatch(ctx, "blank", ""); err != nil || won {
		t.Fatalf("an empty nonce must lose without error: won=%v err=%v", won, err)
	}
	if row := commandRow(t, api, ctx, "blank"); row.DispatchNonce.String != n {
		t.Fatalf("an empty-nonce confirm moved the row's nonce to %v", row.DispatchNonce)
	}
}

// TestResponseMustQuoteTheConfirmedNonce pins the contract the live dispatcher relies on: once
// confirmed, the command is on the NEW dispatch, so the outcome must quote it, and an answer
// quoting the envelope's original nonce is refused.
func TestResponseMustQuoteTheConfirmedNonce(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	_, n := sentWithNonce(t, api, ctx, "answer-me")
	n2, won, err := api.ConfirmDispatch(ctx, "answer-me", n)
	if err != nil || !won {
		t.Fatalf("ConfirmDispatch: won=%v err=%v", won, err)
	}

	if _, err := api.MarkResponse(ctx, "answer-me", "d", n, true, nil, nil); !errors.Is(err, ErrResponseNonceMismatch) {
		t.Fatalf("an answer quoting the pre-confirmation nonce: err=%v, want ErrResponseNonceMismatch", err)
	}
	cmd, err := api.MarkResponse(ctx, "answer-me", "d", n2, true, nil, nil)
	if err != nil {
		t.Fatalf("an answer quoting the confirmed nonce was refused: %v", err)
	}
	if cmd.Status != CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL", cmd.Status)
	}
}
