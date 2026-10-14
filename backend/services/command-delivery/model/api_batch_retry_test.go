// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/datatypes"
)

// retryFixture is a fired batch whose targets span every outcome class.
//
//	pump-0000  SUCCEEDED
//	pump-0001  REJECTED       (the device said no)
//	pump-0002  NOT_DELIVERED  (undeliverable)
//	pump-0003  UNKNOWN        (sent, no answer)
//	pump-0004  still QUEUED   (in flight)
//	pump-0005  refused at creation (DEVICE_DISABLED), no command row
type retryFixture struct {
	api      *Api
	ctx      context.Context
	original *CommandBatch
	gate     *countingValidator
}

func newRetryFixture(t *testing.T) *retryFixture {
	t.Helper()
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	gate := &countingValidator{refuse: map[string]RejectionCode{"pump-0005": "DEVICE_DISABLED"}}
	api.BatchValidator = gate

	payload := `{"delay":5}`
	tokens := deviceTokens(6)
	original, err := api.CreateCommandBatch(ctx, &CommandBatchCreateRequest{
		Token: "fleet-reboot", Name: "reboot", Payload: &payload, DeviceTokens: &tokens, AllowPartial: true,
	})
	if err != nil {
		t.Fatalf("create original: %v", err)
	}
	f := &retryFixture{api: api, ctx: ctx, original: original, gate: gate}

	byDevice := map[string]*Command{}
	for _, c := range commandsOf(t, api, ctx, original.Token) {
		byDevice[c.DeviceToken] = c
	}
	respond := func(device string, success bool, reach Reach) {
		c := byDevice[device]
		if _, won, err := api.MarkSent(ctx, c.ID); err != nil || !won {
			t.Fatalf("claim %s: %v %v", device, won, err)
		}
		nonce := nonceOf(t, api, ctx, c.ID)
		if _, err := api.MarkResponse(ctx, c.Token, device, nonce, success, reach, nil, strPtr("because")); err != nil {
			t.Fatalf("respond %s: %v", device, err)
		}
	}
	respond("pump-0000", true, ReachAnswered)
	respond("pump-0001", false, ReachAnswered)
	if ok, err := api.MarkUndeliverable(ctx, byDevice["pump-0002"].ID, "no path"); err != nil || !ok {
		t.Fatalf("undeliverable: %v %v", ok, err)
	}
	respond("pump-0003", false, ReachUnanswered)
	return f
}

func (f *retryFixture) retry(token string, include bool, partial bool, devices ...string) (*CommandBatch, error) {
	return f.api.RetryCommandBatch(f.ctx, &CommandBatchRetryRequest{
		Token: token, Of: f.original.Token, DeviceTokens: devices,
		IncludeUnknown: include, AllowPartial: partial,
	})
}

func wantCode(t *testing.T, err error, want RejectionCode) *BatchRejected {
	t.Helper()
	var br *BatchRejected
	if errors.As(err, &br) {
		if br.Code != want {
			t.Fatalf("rejected with %s, want %s (%s)", br.Code, want, br.Reason)
		}
		return br
	}
	var er *EnqueueRejected
	if errors.As(err, &er) {
		if er.Code != want {
			t.Fatalf("rejected with %s, want %s (%s)", er.Code, want, er.Reason)
		}
		return nil
	}
	t.Fatalf("err = %v, want a %s rejection", err, want)
	return nil
}

func TestARetryCopiesTheCommandAndLinksToTheOriginal(t *testing.T) {
	f := newRetryFixture(t)
	retry, err := f.retry("retry-1", false, false, "pump-0001", "pump-0002")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !retry.RetryOf.Valid || retry.RetryOf.String != f.original.Token {
		t.Fatalf("retry_of = %+v, want %s", retry.RetryOf, f.original.Token)
	}
	if retry.Name != "reboot" || retry.Payload == nil || string(*retry.Payload) != `{"delay":5}` {
		t.Fatalf("the retry must carry the original's command: %s %v", retry.Name, retry.Payload)
	}
	if retry.TargetKind != BatchTargetDeviceList.String() || retry.Accepted != 2 || retry.Resolved != 2 {
		t.Fatalf("retry record = %+v", retry)
	}
	cmds := commandsOf(t, f.api, f.ctx, "retry-1")
	if len(cmds) != 2 || cmds[0].Name != "reboot" || cmds[0].Status != CommandQueued.String() ||
		cmds[0].Payload == nil || string(*cmds[0].Payload) != `{"delay":5}` {
		t.Fatalf("retry commands = %+v", cmds)
	}
	// The original is untouched: its failed command stays failed and the batch row is unchanged.
	for _, c := range commandsOf(t, f.api, f.ctx, f.original.Token) {
		if c.DeviceToken == "pump-0001" && c.Status != CommandFailed.String() {
			t.Fatalf("the original command was altered: %s", c.Status)
		}
	}
	if got, _ := f.api.CommandBatchesByToken(f.ctx, []string{f.original.Token}); got[0].RetryOf.Valid {
		t.Fatal("the original batch gained a retry_of")
	}
}

func TestRetryRefusesNonMembersAndIneligibleTargets(t *testing.T) {
	f := newRetryFixture(t)

	_, err := f.retry("r-stranger", false, true, "pump-0001", "stranger")
	br := wantCode(t, err, RejectRetryNotAMember)
	if len(br.Refusals) != 1 || br.Refusals[0].DeviceToken != "stranger" {
		t.Fatalf("the refusal must name the offending device: %+v", br.Refusals)
	}

	_, err = f.retry("r-done", false, true, "pump-0000")
	wantCode(t, err, RejectRetryNotEligible)

	_, err = f.retry("r-flight", false, true, "pump-0004")
	wantCode(t, err, RejectRetryNotEligible)

	// Nothing was created by any of the refusals, and the tokens are unspent.
	for _, tok := range []string{"r-stranger", "r-done", "r-flight"} {
		if got, _ := f.api.CommandBatchesByToken(f.ctx, []string{tok}); len(got) != 0 {
			t.Fatalf("a refused retry created batch %s", tok)
		}
	}

	_, err = f.retry("r-empty", false, true)
	wantCode(t, err, RejectRetryNoTargets)

	_, err = f.api.RetryCommandBatch(f.ctx, &CommandBatchRetryRequest{
		Token: "r-ghost", Of: "no-such-batch", DeviceTokens: []string{"pump-0001"}, AllowPartial: true,
	})
	wantCode(t, err, RejectRetryOriginalNotFound)
}

func TestRetryIdempotencyRunsBothWays(t *testing.T) {
	f := newRetryFixture(t)
	first, err := f.retry("retry-1", false, false, "pump-0001")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	callsAfterFirst := f.gate.calls

	// The retry's own token replays the retry: same row, no new commands, no gate consulted.
	replay, err := f.retry("retry-1", false, false, "pump-0001", "pump-0002")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v %v, want batch %d", replay, err, first.ID)
	}
	if n := len(commandsOf(t, f.api, f.ctx, "retry-1")); n != 1 {
		t.Fatalf("a replayed retry must admit nothing more, has %d commands", n)
	}
	if f.gate.calls != callsAfterFirst {
		t.Fatal("a replayed retry consulted the enqueue gate")
	}

	// The ORIGINAL token replays the original and executes nothing.
	before := len(commandsOf(t, f.api, f.ctx, f.original.Token))
	orig, err := f.api.CreateCommandBatch(f.ctx, batchRequest(f.original.Token, deviceTokens(6)))
	if err != nil || orig.ID != f.original.ID || orig.RetryOf.Valid {
		t.Fatalf("original replay = %+v %v", orig, err)
	}
	if after := len(commandsOf(t, f.api, f.ctx, f.original.Token)); after != before {
		t.Fatalf("replaying the original token changed its commands: %d -> %d", before, after)
	}

	// A retry token that names a batch which is not a retry of this original is refused, not
	// answered with that batch.
	_, err = f.retry(f.original.Token, false, false, "pump-0002")
	wantCode(t, err, RejectRetryTokenInUse)
	other, _ := f.api.CreateCommandBatch(f.ctx, batchRequest("unrelated", deviceTokens(1)))
	_, err = f.retry(other.Token, false, false, "pump-0002")
	wantCode(t, err, RejectRetryTokenInUse)
}

func TestUnknownTargetsNeedAcknowledgement(t *testing.T) {
	f := newRetryFixture(t)

	_, err := f.retry("r-unack", false, true, "pump-0001", "pump-0003")
	br := wantCode(t, err, RejectRetryUnknownNotAcknowledged)
	if len(br.Refusals) != 1 || br.Refusals[0].DeviceToken != "pump-0003" {
		t.Fatalf("only the unknown device should be flagged: %+v", br.Refusals)
	}
	if got, _ := f.api.CommandBatchesByToken(f.ctx, []string{"r-unack"}); len(got) != 0 {
		t.Fatal("an unacknowledged retry created a batch")
	}

	ok, err := f.retry("r-ack", true, true, "pump-0001", "pump-0003")
	if err != nil || ok.Accepted != 2 {
		t.Fatalf("acknowledged retry = %+v %v", ok, err)
	}
}

func TestRetryGoesThroughTheSameEnqueueGate(t *testing.T) {
	f := newRetryFixture(t)
	// The device has since been disabled: a retry must be refused for it exactly as a fresh batch.
	f.gate.refuse["pump-0002"] = "DEVICE_DISABLED"

	_, err := f.retry("r-gate", false, false, "pump-0001", "pump-0002")
	br := wantCode(t, err, RejectBatchPartialRefused)
	if len(br.Refusals) != 1 || br.Refusals[0].DeviceToken != "pump-0002" || br.Refusals[0].Code != "DEVICE_DISABLED" {
		t.Fatalf("refusals = %+v", br.Refusals)
	}

	partial, err := f.retry("r-gate", false, true, "pump-0001", "pump-0002")
	if err != nil || partial.Accepted != 1 || partial.Resolved != 2 {
		t.Fatalf("partial retry = %+v %v", partial, err)
	}
}

func TestACoveredTargetIsNotRetriedTwiceFromOneParent(t *testing.T) {
	f := newRetryFixture(t)
	first, err := f.retry("retry-1", false, false, "pump-0001")
	if err != nil {
		t.Fatal(err)
	}
	// Its command is still live: a second retry from the same parent would send twice.
	_, err = f.retry("retry-2", false, false, "pump-0001")
	wantCode(t, err, RejectRetryNotEligible)

	// Once the retry's own command fails, retrying the RETRY is the way forward, and it names
	// the retry as its parent.
	c := commandsOf(t, f.api, f.ctx, first.Token)[0]
	if _, won, _ := f.api.MarkSent(f.ctx, c.ID); !won {
		t.Fatal("claim")
	}
	if _, err := f.api.MarkResponse(f.ctx, c.Token, "pump-0001", nonceOf(t, f.api, f.ctx, c.ID),
		false, ReachAnswered, nil, strPtr("still no")); err != nil {
		t.Fatal(err)
	}
	second, err := f.api.RetryCommandBatch(f.ctx, &CommandBatchRetryRequest{
		Token: "retry-of-retry", Of: first.Token, DeviceTokens: []string{"pump-0001"}, AllowPartial: false,
	})
	if err != nil || second.RetryOf.String != first.Token {
		t.Fatalf("retry of a retry = %+v %v", second, err)
	}
	listed, err := f.api.RetriesOfBatch(f.ctx, first.Token)
	if err != nil || len(listed) != 1 || listed[0] != "retry-of-retry" {
		t.Fatalf("retries of the first retry = %v %v", listed, err)
	}
	listed, _ = f.api.RetriesOfBatch(f.ctx, f.original.Token)
	if len(listed) != 1 || listed[0] != "retry-1" {
		t.Fatalf("retries of the original = %v", listed)
	}
}

func TestRefusedDevicesAreOfferedOnlyWhenTheirSampleIsComplete(t *testing.T) {
	f := newRetryFixture(t)

	// pump-0005 was refused DEVICE_DISABLED and the record holds all 1 of 1: it is a member. The
	// device has since been re-enabled, so the gate now lets it through.
	delete(f.gate.refuse, "pump-0005")
	ok, err := f.retry("r-refused", false, true, "pump-0005")
	if err != nil || ok.Accepted != 1 {
		t.Fatalf("a completely-recorded refused device must be retryable: %+v %v", ok, err)
	}

	// Now a record whose sample is truncated: 1 kept of 150 refused for the code.
	sample, _ := json.Marshal([]BatchDeviceRefusal{{DeviceToken: "kept", Code: "DEVICE_DISABLED", Reason: "x"}})
	counts, _ := json.Marshal([]RefusalCount{{Code: "DEVICE_DISABLED", Count: 150}})
	refs, cnts := datatypes.JSON(sample), datatypes.JSON(counts)
	trunc := &CommandBatch{
		Name: "reboot", TargetKind: BatchTargetDeviceList.String(),
		Refusals: &refs, RefusalCounts: &cnts,
	}
	trunc.Token = "truncated"
	if err := f.api.RDB.DB(f.ctx).Create(trunc).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.api.RetryCommandBatch(f.ctx, &CommandBatchRetryRequest{
		Token: "r-trunc", Of: "truncated", DeviceTokens: []string{"kept"}, AllowPartial: true,
	})
	wantCode(t, err, RejectRetryNotAMember)
}

func TestAFinishedCommandWithNoRecordedClassIsTreatedAsUnknown(t *testing.T) {
	f := newRetryFixture(t)
	// Simulate a legacy/defective row: terminal, outcome NULL (bypassing the write guard).
	if err := f.api.RDB.DB(f.ctx).Exec(
		"UPDATE commands SET outcome = NULL WHERE device_token = ?", "pump-0002").Error; err != nil {
		t.Fatal(err)
	}
	_, err := f.retry("r-legacy", false, true, "pump-0002")
	wantCode(t, err, RejectRetryUnknownNotAcknowledged)
}
