// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// pullResolver is a fixed PullChannelResolver.
type pullResolver struct {
	pull map[string]bool
	err  error
}

func (p pullResolver) PullDevices(_ context.Context, tokens []string) (map[string]bool, error) {
	if p.err != nil {
		return nil, p.err
	}
	out := map[string]bool{}
	for _, t := range tokens {
		if p.pull[t] {
			out[t] = true
		}
	}
	return out, nil
}

func pullApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	api.PullChannels = pullResolver{pull: map[string]bool{"d": true}}
	return api, core.WithTenant(context.Background(), "A")
}

func enqueuePull(t *testing.T, api *Api, ctx context.Context, token string, mode *string, max *int32) {
	t.Helper()
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: token, DeviceToken: "d", Name: "reboot", DeliveryMode: mode, MaxAttempts: max,
	}); err != nil {
		t.Fatalf("enqueue %s: %v", token, err)
	}
}

func ptr[T any](v T) *T { return &v }

func ageSent(t *testing.T, api *Api, ctx context.Context, token string, age time.Duration) {
	t.Helper()
	res := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", token).Update("sent_time", time.Now().Add(-age))
	if res.Error != nil || res.RowsAffected != 1 {
		t.Fatalf("aging %s: rows=%d err=%v", token, res.RowsAffected, res.Error)
	}
}

// TestPullClaimsQueuedHeldAndParkedOldestFirst: every claimable state is handed out, in id order,
// each claimed to SENT with a nonce and an attempt count of one.
func TestPullClaimsQueuedHeldAndParkedOldestFirst(t *testing.T) {
	api, ctx := pullApi(t)
	seedWithStatus(t, api, ctx, "c1", CommandHeld)
	seedWithStatus(t, api, ctx, "c2", CommandParked)
	seedWithStatus(t, api, ctx, "c3", CommandQueued)

	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("pulled %d commands, want 3", len(got))
	}
	for i, want := range []string{"c1", "c2", "c3"} {
		if got[i].Command.Token != want {
			t.Fatalf("position %d = %s, want %s (oldest first)", i, got[i].Command.Token, want)
		}
		if got[i].Command.Status != CommandSent.String() || !got[i].Command.DispatchNonce.Valid || got[i].Attempt != 1 {
			t.Fatalf("%s: status %s nonce %v attempt %d; want SENT, a nonce, attempt 1",
				want, got[i].Command.Status, got[i].Command.DispatchNonce, got[i].Attempt)
		}
	}
}

// TestPullNeverHandsOutAClaimedCommandTwice is the claim's whole job: a second pull, before the
// acknowledgement is overdue, finds nothing.
func TestPullNeverHandsOutAClaimedCommandTwice(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "once", nil, nil)

	first, _ := api.PullCommands(ctx, "d", 0)
	second, err := api.PullCommands(ctx, "d", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 0 {
		t.Fatalf("first pull %d commands, second %d; want 1 then 0", len(first), len(second))
	}
}

// TestPullLimitAndOtherDevices: only the named device's commands, and no more than the limit.
func TestPullLimitAndOtherDevices(t *testing.T) {
	api, ctx := pullApi(t)
	for _, tok := range []string{"a", "b", "c"} {
		enqueuePull(t, api, ctx, tok, nil, nil)
	}
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "other", DeviceToken: "e", Name: "reboot"}); err != nil {
		t.Fatal(err)
	}
	got, err := api.PullCommands(ctx, "d", 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("pulled %d (err %v), want 2", len(got), err)
	}
	rest, _ := api.PullCommands(ctx, "d", 0)
	if len(rest) != 1 || rest[0].Command.Token != "c" {
		t.Fatalf("remaining pull = %v, want [c]", rest)
	}
	assertStatus(t, api, ctx, "other", CommandQueued)
}

// TestPullHonoursTheDisabledDeviceGate: a disabled device is handed nothing, the same gate every
// other claim applies.
func TestPullHonoursTheDisabledDeviceGate(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "gated", nil, nil)
	if err := api.RDB.DB(ctx).Create(&DeviceGate{TenantId: "A", DeviceToken: "d", Disabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	got, err := api.PullCommands(ctx, "d", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("pulled %d (err %v) for a disabled device, want 0", len(got), err)
	}
	assertStatus(t, api, ctx, "gated", CommandQueued)
}

// TestPullHonoursTheMaintenanceWindow: a command whose window has not opened is not handed out.
func TestPullHonoursTheMaintenanceWindow(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "waiting", nil, nil)
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "waiting").
		Updates(map[string]any{"dispatch_after": time.Now().Add(time.Hour), "dispatch_until": time.Now().Add(2 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	got, _ := api.PullCommands(ctx, "d", 0)
	if len(got) != 0 {
		t.Fatalf("pulled a command outside its window: %v", got)
	}
}

// TestAckRecordsDeliveryEvidenceAndIsIdempotent: the ack stamps delivered_time once and names
// its evidence; the status stays SENT (there is no DELIVERED).
func TestAckRecordsDeliveryEvidenceAndIsIdempotent(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "ack-me", nil, nil)
	if _, err := api.PullCommands(ctx, "d", 0); err != nil {
		t.Fatal(err)
	}
	ok, err := api.AcknowledgeDelivery(ctx, "ack-me", "d")
	if err != nil || !ok {
		t.Fatalf("ack: ok=%v err=%v", ok, err)
	}
	row := commandRow(t, api, ctx, "ack-me")
	if row.Status != CommandSent.String() || !row.DeliveredTime.Valid ||
		row.DeliveryEvidence.String != EvidenceDeviceAck {
		t.Fatalf("after ack: status %s delivered %v evidence %v; want SENT, a time, %s",
			row.Status, row.DeliveredTime, row.DeliveryEvidence, EvidenceDeviceAck)
	}
	first := row.DeliveredTime.Time
	time.Sleep(5 * time.Millisecond)
	if ok, err := api.AcknowledgeDelivery(ctx, "ack-me", "d"); err != nil || !ok {
		t.Fatalf("repeat ack: ok=%v err=%v", ok, err)
	}
	if again := commandRow(t, api, ctx, "ack-me").DeliveredTime.Time; !again.Equal(first) {
		t.Fatalf("a repeated ack moved delivered_time from %v to %v; the first evidence must stand", first, again)
	}
}

// TestAckIsRefusedForAnotherDeviceAndAnUnpulledCommand: nothing is learned about, or recorded
// for, a command the caller's device does not own or no pull handed out.
func TestAckIsRefusedForAnotherDeviceAndAnUnpulledCommand(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "mine", nil, nil)

	if _, err := api.AcknowledgeDelivery(ctx, "mine", "intruder"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("another device's ack: err = %v, want not found", err)
	}
	if _, err := api.AcknowledgeDelivery(ctx, "mine", "d"); !errors.Is(err, ErrCommandNotPulled) {
		t.Fatalf("ack of a command no pull handed out: err = %v, want ErrCommandNotPulled", err)
	}
	if _, err := api.AcknowledgeDelivery(ctx, "ghost", "d"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("ack of a missing command: err = %v, want not found", err)
	}
	if row := commandRow(t, api, ctx, "mine"); row.DeliveredTime.Valid {
		t.Fatalf("a refused ack recorded delivered_time %v", row.DeliveredTime)
	}
}

// TestOnewayIsSettledByTheAck: the acknowledgement alone completes a ONEWAY command.
func TestOnewayIsSettledByTheAck(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "fire-and-forget", ptr(DeliveryModeOneway), nil)
	if _, err := api.PullCommands(ctx, "d", 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := api.AcknowledgeDelivery(ctx, "fire-and-forget", "d"); err != nil || !ok {
		t.Fatalf("ack: ok=%v err=%v", ok, err)
	}
	row := commandRow(t, api, ctx, "fire-and-forget")
	if row.Status != CommandSuccessful.String() || row.Outcome.String != OutcomeSucceeded.String() ||
		!row.RespondedTime.Valid || !row.DeliveredTime.Valid {
		t.Fatalf("ONEWAY after ack: status %s outcome %v responded %v delivered %v; want SUCCESSFUL/SUCCEEDED, both times",
			row.Status, row.Outcome, row.RespondedTime, row.DeliveredTime)
	}
}

// TestTwoWayAckDoesNotSettle is the counterweight: an ordinary command is still waiting for its
// answer after the ack.
func TestTwoWayAckDoesNotSettle(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "two-way", nil, nil)
	api.PullCommands(ctx, "d", 0)
	api.AcknowledgeDelivery(ctx, "two-way", "d")
	if row := commandRow(t, api, ctx, "two-way"); row.Status != CommandSent.String() || row.Outcome.Valid {
		t.Fatalf("a two-way command after its ack: status %s outcome %v; want SENT and none", row.Status, row.Outcome)
	}
}

// TestResponseSettlesAndKeepsTheFirstEvidence: an answer after an ack settles the command but
// leaves the ack as the evidence; an answer with no ack records the answer as the evidence.
func TestResponseSettlesAndKeepsTheFirstEvidence(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "acked-first", nil, nil)
	enqueuePull(t, api, ctx, "answered-only", nil, nil)
	api.PullCommands(ctx, "d", 0)
	api.AcknowledgeDelivery(ctx, "acked-first", "d")

	for _, tok := range []string{"acked-first", "answered-only"} {
		if _, err := api.RecordPulledResponse(ctx, tok, "d", true, ptr("done"), nil); err != nil {
			t.Fatalf("respond %s: %v", tok, err)
		}
	}
	a := commandRow(t, api, ctx, "acked-first")
	b := commandRow(t, api, ctx, "answered-only")
	if a.Status != CommandSuccessful.String() || a.DeliveryEvidence.String != EvidenceDeviceAck {
		t.Fatalf("acked-first: status %s evidence %v; want SUCCESSFUL with the ack kept", a.Status, a.DeliveryEvidence)
	}
	if b.Status != CommandSuccessful.String() || b.DeliveryEvidence.String != EvidenceDeviceResponse || !b.DeliveredTime.Valid {
		t.Fatalf("answered-only: status %s evidence %v delivered %v; want SUCCESSFUL with DEVICE_RESPONSE evidence",
			b.Status, b.DeliveryEvidence, b.DeliveredTime)
	}
}

// TestADeviceRefusalIsStillEvidenceOfDelivery: a device that answered no had the command.
func TestADeviceRefusalIsStillEvidenceOfDelivery(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "refused", nil, nil)
	api.PullCommands(ctx, "d", 0)
	if _, err := api.RecordPulledResponse(ctx, "refused", "d", false, nil, ptr("no")); err != nil {
		t.Fatal(err)
	}
	row := commandRow(t, api, ctx, "refused")
	if row.Status != CommandFailed.String() || row.Outcome.String != OutcomeRejected.String() || !row.DeliveredTime.Valid {
		t.Fatalf("refused: status %s outcome %v delivered %v; want FAILED/REJECTED with delivery evidence",
			row.Status, row.Outcome, row.DeliveredTime)
	}
}

// TestEveryTransportsAnswerRecordsEvidence: a command answered through MarkResponse as any other
// transport does (no pull involved) gains the same evidence.
func TestEveryTransportsAnswerRecordsEvidence(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	_, nonce := sentWithNonce(t, api, ctx, "mqtt-style")
	if _, err := api.MarkResponse(ctx, "mqtt-style", "d", nonce, true, ReachAnswered, nil, nil); err != nil {
		t.Fatal(err)
	}
	row := commandRow(t, api, ctx, "mqtt-style")
	if !row.DeliveredTime.Valid || row.DeliveryEvidence.String != EvidenceDeviceResponse {
		t.Fatalf("answered command: delivered %v evidence %v; want %s", row.DeliveredTime, row.DeliveryEvidence, EvidenceDeviceResponse)
	}
}

func TestSuccessWithoutADeviceAnswerRecordsNoDeliveryEvidence(t *testing.T) {
	for _, reach := range []Reach{ReachUnanswered, ReachUnsent, ReachAnswered} {
		t.Run(string(reach), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")
			_, nonce := sentWithNonce(t, api, ctx, "queued")
			if _, err := api.MarkResponse(ctx, "queued", "d", nonce, true, reach, nil, nil); err != nil {
				t.Fatal(err)
			}
			row := commandRow(t, api, ctx, "queued")
			if row.Status != CommandSuccessful.String() || !row.Outcome.Valid || row.Outcome.String != OutcomeSucceeded.String() {
				t.Fatalf("status %s outcome %v; want SUCCESSFUL/SUCCEEDED", row.Status, row.Outcome)
			}
			if reach == ReachAnswered {
				if !row.DeliveredTime.Valid || !row.DeliveryEvidence.Valid || row.DeliveryEvidence.String != EvidenceDeviceResponse {
					t.Fatalf("device answer: delivered %v evidence %v", row.DeliveredTime, row.DeliveryEvidence)
				}
			} else if row.DeliveredTime.Valid || row.DeliveryEvidence.Valid {
				t.Fatalf("transport success stamped evidence: delivered %v evidence %v", row.DeliveredTime, row.DeliveryEvidence)
			}
		})
	}
}

// TestAFailureThatNeverReachedTheDeviceIsNotEvidence: ReachUnsent and ReachUnanswered stamp nothing.
func TestAFailureThatNeverReachedTheDeviceIsNotEvidence(t *testing.T) {
	for _, reach := range []Reach{ReachUnsent, ReachUnanswered} {
		api := newTestApi(t)
		ctx := core.WithTenant(context.Background(), "A")
		_, nonce := sentWithNonce(t, api, ctx, "lost")
		if _, err := api.MarkResponse(ctx, "lost", "d", nonce, false, reach, nil, ptr("never")); err != nil {
			t.Fatal(err)
		}
		if row := commandRow(t, api, ctx, "lost"); row.DeliveredTime.Valid || row.DeliveryEvidence.Valid {
			t.Fatalf("reach %q stamped delivery evidence: %v %v", reach, row.DeliveredTime, row.DeliveryEvidence)
		}
	}
}

// TestADroppedAckLeavesDeliveredTimeNullAndTheOutcomeNotDelivered is the planted-defect case: the
// device's acknowledgement never arrives. With one attempt allowed the command is failed, and
// nothing says it was delivered.
func TestADroppedAckLeavesDeliveredTimeNullAndTheOutcomeNotDelivered(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "dropped", nil, ptr(int32(1)))
	if got, _ := api.PullCommands(ctx, "d", 0); len(got) != 1 {
		t.Fatalf("first pull handed out %d, want 1", len(got))
	}
	// Within the wait, the pull is still presumed good: nothing happens.
	if got, _ := api.PullCommands(ctx, "d", 0); len(got) != 0 {
		t.Fatalf("a pull inside the ack wait handed out %d", len(got))
	}
	assertStatus(t, api, ctx, "dropped", CommandSent)

	ageSent(t, api, ctx, "dropped", 2*PullAckWait)
	if got, _ := api.PullCommands(ctx, "d", 0); len(got) != 0 {
		t.Fatalf("a command with its one attempt spent was handed out again: %v", got)
	}
	row := commandRow(t, api, ctx, "dropped")
	if row.Status != CommandFailed.String() || row.Outcome.String != OutcomeNotDelivered.String() ||
		row.DeliveredTime.Valid || row.DeliveryEvidence.Valid || row.Error.String != PullUnacknowledgedReason {
		t.Fatalf("dropped ack: status %s outcome %v delivered %v evidence %v error %q; want FAILED/NOT_DELIVERED, no delivery",
			row.Status, row.Outcome, row.DeliveredTime, row.DeliveryEvidence, row.Error.String)
	}
	// A late ack does not resurrect it.
	if ok, err := api.AcknowledgeDelivery(ctx, "dropped", "d"); err != nil || ok {
		t.Fatalf("late ack: ok=%v err=%v; want false, nil", ok, err)
	}
	if row := commandRow(t, api, ctx, "dropped"); row.DeliveredTime.Valid || row.Status != CommandFailed.String() {
		t.Fatalf("a late ack changed the row: status %s delivered %v", row.Status, row.DeliveredTime)
	}
}

// TestAnUnackedPullIsHandedOutAgainUntilAttemptsAreSpent: redelivery stamps a fresh nonce and
// counts the attempt; the last attempt is the last.
func TestAnUnackedPullIsHandedOutAgainUntilAttemptsAreSpent(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "retry", nil, ptr(int32(2)))
	first, _ := api.PullCommands(ctx, "d", 0)
	ageSent(t, api, ctx, "retry", 2*PullAckWait)
	second, err := api.PullCommands(ctx, "d", 0)
	if err != nil || len(second) != 1 {
		t.Fatalf("redelivery: %d commands (err %v), want 1", len(second), err)
	}
	if second[0].Attempt != 2 || second[0].Command.DispatchNonce.String == first[0].Command.DispatchNonce.String {
		t.Fatalf("redelivery attempt %d nonce %q (first %q); want attempt 2 and a fresh nonce",
			second[0].Attempt, second[0].Command.DispatchNonce.String, first[0].Command.DispatchNonce.String)
	}
	ageSent(t, api, ctx, "retry", 2*PullAckWait)
	if third, _ := api.PullCommands(ctx, "d", 0); len(third) != 0 {
		t.Fatalf("a third hand-out of a two-attempt command: %v", third)
	}
	assertStatus(t, api, ctx, "retry", CommandFailed)
}

// TestAnAckedPullIsNeverHandedOutAgain: the ack ends the redelivery clock.
func TestAnAckedPullIsNeverHandedOutAgain(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "settled", nil, nil)
	api.PullCommands(ctx, "d", 0)
	api.AcknowledgeDelivery(ctx, "settled", "d")
	ageSent(t, api, ctx, "settled", 10*PullAckWait)
	if got, _ := api.PullCommands(ctx, "d", 0); len(got) != 0 {
		t.Fatalf("an acknowledged command was handed out again: %v", got)
	}
	assertStatus(t, api, ctx, "settled", CommandSent)
}

// TestFailUnacknowledgedPullsCoversDevicesThatStoppedPulling: the sweep's cross-device form.
func TestFailUnacknowledgedPullsCoversDevicesThatStoppedPulling(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "abandoned", nil, ptr(int32(1)))
	api.PullCommands(ctx, "d", 0)
	ageSent(t, api, ctx, "abandoned", 2*PullAckWait)

	n, err := api.FailUnacknowledgedPulls(core.WithSystemContext(context.Background()), "", time.Now())
	if err != nil || n != 1 {
		t.Fatalf("sweep failed %d (err %v), want 1", n, err)
	}
	assertStatus(t, api, ctx, "abandoned", CommandFailed)
}

// TestFailUnacknowledgedPullsLeavesOtherSentCommandsAlone: a SENT command no pull handed out (an
// MQTT device's) is never this channel's to fail, however old.
func TestFailUnacknowledgedPullsLeavesOtherSentCommandsAlone(t *testing.T) {
	api, ctx := pullApi(t)
	id, _ := sentWithNonce(t, api, ctx, "mqtt-sent")
	backdateSentTime(t, api, ctx, id, 24*time.Hour)
	n, err := api.FailUnacknowledgedPulls(core.WithSystemContext(context.Background()), "", time.Now())
	if err != nil || n != 0 {
		t.Fatalf("sweep failed %d (err %v) of a command no pull handed out", n, err)
	}
	assertStatus(t, api, ctx, "mqtt-sent", CommandSent)
}

// TestDeliveryOptionsAreRefusedOffThePullChannel and the invalid ones everywhere.
func TestDeliveryOptionsAreRefusedOffThePullChannel(t *testing.T) {
	api := newTestApi(t)
	api.PullChannels = pullResolver{pull: map[string]bool{"d": true}}
	ctx := core.WithTenant(context.Background(), "A")

	cases := []struct {
		name string
		req  CommandCreateRequest
		want RejectionCode
	}{
		{"oneway on a device that is not pull", CommandCreateRequest{Token: "t1", DeviceToken: "mqtt-dev", Name: "x", DeliveryMode: ptr(DeliveryModeOneway)}, RejectDeliveryOptionNotApplicable},
		{"maxAttempts on a device that is not pull", CommandCreateRequest{Token: "t2", DeviceToken: "mqtt-dev", Name: "x", MaxAttempts: ptr(int32(2))}, RejectDeliveryOptionNotApplicable},
		{"unknown mode", CommandCreateRequest{Token: "t3", DeviceToken: "d", Name: "x", DeliveryMode: ptr("TWOWAY")}, RejectDeliveryOptionInvalid},
		{"zero attempts", CommandCreateRequest{Token: "t4", DeviceToken: "d", Name: "x", MaxAttempts: ptr(int32(0))}, RejectDeliveryOptionInvalid},
		{"too many attempts", CommandCreateRequest{Token: "t5", DeviceToken: "d", Name: "x", MaxAttempts: ptr(int32(MaxPullMaxAttempts + 1))}, RejectDeliveryOptionInvalid},
	}
	for _, c := range cases {
		_, err := api.CreateCommand(ctx, &c.req)
		var rej *EnqueueRejected
		if !errors.As(err, &rej) || rej.Code != c.want {
			t.Errorf("%s: err = %v, want rejection %s", c.name, err, c.want)
		}
	}
	// And with no resolver at all, nothing can honour an option.
	bare := newTestApi(t)
	_, err := bare.CreateCommand(ctx, &CommandCreateRequest{Token: "t6", DeviceToken: "d", Name: "x", DeliveryMode: ptr(DeliveryModeOneway)})
	var rej *EnqueueRejected
	if !errors.As(err, &rej) || rej.Code != RejectDeliveryOptionNotApplicable {
		t.Errorf("no resolver: err = %v, want %s", err, RejectDeliveryOptionNotApplicable)
	}
}

// TestACommandWithNoDeliveryOptionsIsUnchanged is the golden for a device type that has not
// opted in: the new columns are all NULL, and the resolver is never asked.
func TestACommandWithNoDeliveryOptionsIsUnchanged(t *testing.T) {
	api := newTestApi(t)
	api.PullChannels = pullResolver{err: errors.New("must not be asked")}
	ctx := core.WithTenant(context.Background(), "A")
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "plain", DeviceToken: "d", Name: "reboot"}); err != nil {
		t.Fatalf("a plain command was affected by the pull channel: %v", err)
	}
	row := commandRow(t, api, ctx, "plain")
	if row.DeliveredTime.Valid || row.DeliveryEvidence.Valid || row.DeliveryMode.Valid || row.MaxAttempts.Valid ||
		row.DeliveryAttempts.Valid || row.Status != CommandQueued.String() {
		t.Fatalf("a plain command carries pull state: %+v", row)
	}
}

// TestOptionsAreStored: the accepted options land on the row.
func TestOptionsAreStored(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "stored", ptr(DeliveryModeOneway), ptr(int32(5)))
	row := commandRow(t, api, ctx, "stored")
	if row.DeliveryMode.String != DeliveryModeOneway || row.MaxAttempts.Int32 != 5 || row.DeliveryAttempts.Valid {
		t.Fatalf("stored options: mode %v max %v attempts %v", row.DeliveryMode, row.MaxAttempts, row.DeliveryAttempts)
	}
}

// TestRecordPulledResponseRefusals: another device's command, an unpulled one and a finished one.
func TestRecordPulledResponseRefusals(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "r", nil, nil)
	if _, err := api.RecordPulledResponse(ctx, "r", "d", true, nil, nil); !errors.Is(err, ErrCommandNotPulled) {
		t.Fatalf("answer to an unpulled command: %v", err)
	}
	api.PullCommands(ctx, "d", 0)
	if _, err := api.RecordPulledResponse(ctx, "r", "intruder", true, nil, nil); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("another device's answer: %v", err)
	}
	if _, err := api.RecordPulledResponse(ctx, "r", "d", true, nil, nil); err != nil {
		t.Fatal(err)
	}
	// A repeat of the answer is idempotent, not an error.
	if _, err := api.RecordPulledResponse(ctx, "r", "d", true, nil, nil); err != nil {
		t.Fatalf("repeated answer: %v", err)
	}
}

// TestAnAcknowledgedCommandWithSpentAttemptsIsNotFailed: the acknowledgement is what keeps the
// failure pass off a command; attempts alone must not fail it.
func TestAnAcknowledgedCommandWithSpentAttemptsIsNotFailed(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "acked", nil, ptr(int32(1)))
	api.PullCommands(ctx, "d", 0)
	api.AcknowledgeDelivery(ctx, "acked", "d")
	ageSent(t, api, ctx, "acked", 10*PullAckWait)
	n, err := api.FailUnacknowledgedPulls(core.WithSystemContext(context.Background()), "", time.Now())
	if err != nil || n != 0 {
		t.Fatalf("failed %d (err %v) acknowledged commands", n, err)
	}
	assertStatus(t, api, ctx, "acked", CommandSent)
}

// TestARepeatedAckOfASettledOnewayStillStands: the device retries an ack it never saw answered.
func TestARepeatedAckOfASettledOnewayStillStands(t *testing.T) {
	api, ctx := pullApi(t)
	enqueuePull(t, api, ctx, "ow", ptr(DeliveryModeOneway), nil)
	api.PullCommands(ctx, "d", 0)
	api.AcknowledgeDelivery(ctx, "ow", "d")
	if ok, err := api.AcknowledgeDelivery(ctx, "ow", "d"); err != nil || !ok {
		t.Fatalf("repeat ack of a settled ONEWAY: ok=%v err=%v; want true", ok, err)
	}
}
