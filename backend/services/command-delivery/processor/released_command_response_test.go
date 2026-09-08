// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// realApi builds the ACTUAL command API over an in-memory database, rather than the
// fakeApi the rest of this package's tests use.
//
// 🔴 THE FAKE CANNOT MEASURE THIS SEQUENCE, WHICH IS WHY THIS EXISTS. What is under test
// is the interaction between three writes — the claim, the release that follows a failed
// publish, and the response that arrives afterwards — and every one of those is a
// from-state-predicated conditional UPDATE whose whole behaviour is in the predicate. A
// fake answers each of them from a field a test set, so it would agree with any predicate
// at all, including none. Only a real row moving through real statuses can show that the
// response finds the command in a state it cannot settle.
func realApi(t *testing.T) *model.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	// CommandBatch is migrated alongside Command because ReleaseClaim READS it, to decide
	// whether a failed publish returns the command to the queue or retires it as cancelled.
	// Without the table the release fails on "no such table" and the premise of this test
	// collapses in a way that looks unrelated.
	if err := db.AutoMigrate(&model.Command{}, &model.CommandBatch{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := rdb.CreateTenantTokenIndex(db, &model.Command{}); err != nil {
		t.Fatalf("create tenant token index: %v", err)
	}
	return model.NewApi(&rdb.RdbManager{Database: db})
}

// failingWriter is a device-command writer whose publish always reports an error. It is
// the ONE ingredient the sequence needs that the platform cannot produce on demand: a
// publish outcome that says "failed" without proving nothing was delivered.
type failingWriter struct{ err error }

func (w *failingWriter) WriteMessages(context.Context, ...messaging.Message) error { return w.err }
func (w *failingWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	return w.err
}
func (w *failingWriter) HandleResponse(error) {}

// TestAnAnswerToAReleasedCommandIsRecordedRatherThanDiscarded drives the whole sequence
// end to end, over a real database, with no step faked except the publish failure.
//
// 🔴 THE SEQUENCE IS AN ORDINARY DAY, NOT A CONTRIVANCE:
//
//  1. A dispatcher claims the command, QUEUED -> SENT.
//  2. The publish reports an error. That is not proof the device did not get it — a lost
//     acknowledgement looks exactly the same from here.
//  3. ReleaseClaim correctly returns the row to QUEUED so it can be tried again.
//  4. The device, which DID receive it, answers.
//
// The answer then names a command that is QUEUED, which is not a state a response may
// settle. Every write in this path is guarded on that, and correctly so — writing the
// answer onto a queued row would close out a command that might never have been dispatched
// at all. What was missing was the other half: the guard's verdict was never READ, so the
// refusal was reported to the consumer as success, the consumer acked, and the device's
// answer was gone with nothing anywhere recording that it had arrived.
//
// 🔑 WHAT THIS ASSERTS IS THE DISPOSITION, NOT THE ABSENCE OF A REFUSAL. The command is
// still QUEUED afterwards and that is deliberate — see the note at the end.
func TestAnAnswerToAReleasedCommandIsRecordedRatherThanDiscarded(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	// Steps 1-3, through the real dispatch path rather than by hand.
	dispatcher := procWith(api, &failingWriter{err: errors.New("publish acknowledgement timed out")})
	if err := dispatcher.deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("premise lost: the publish was expected to report an error")
	}

	// The premise, asserted rather than assumed: the release really did put a command the
	// device may already hold back into the dispatchable set.
	if got := statusOf(t, api, ctx, "cmd-1"); got != model.CommandQueued.String() {
		t.Fatalf("after a failed publish the command is %s, want QUEUED; without that the rest "+
			"of this test is measuring a sequence the platform does not produce", got)
	}

	// Step 4: the device answers the dispatch it did receive.
	dead := &deadRecorder{}
	consumer := &CommandDeliveryProcessor{
		Api:                    api,
		area:                   "command-delivery",
		dead:                   deadletter.NewSink(dead, func(error) {}),
		ResponsesNotAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_total"}),
		ResponsesRefused:       prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_e2e_total"}),
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.NewConsumedMessage("inst-1.acme.command-responses.pump-1",
				[]byte(`{"commandToken":"cmd-1","success":true}`), 1, nil, nil),
		},
	}

	if stop := consumer.ProcessMessage(context.Background()); stop {
		t.Fatal("this response must not stop the consumer loop")
	}

	// 🔴 THE ANSWER IS RECORDED. A dead letter is the only place it can now live: it cannot
	// be written against a command the platform still intends to deliver, and it must not
	// simply evaporate.
	if len(dead.msgs) != 1 {
		t.Fatalf("wrote %d dead letters, want 1; the device's answer was discarded", len(dead.msgs))
	}
	letter, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if letter.Reference != "cmd-1" {
		t.Fatalf("the letter must name the command answered: %q", letter.Reference)
	}
	// 🔑 UNPROCESSABLE, NOT EXHAUSTED, and the difference is what the letter TELLS a reader.
	// Exhausted says the platform tried and could not finish, sending someone to look for a
	// failing database; this work could never have been completed however many times it was
	// attempted, and it was attempted once.
	if letter.Reason != deadletter.ReasonUnprocessable {
		t.Fatalf("letter reason = %q, want %q", letter.Reason, deadletter.ReasonUnprocessable)
	}
	if len(letter.Payload) == 0 || letter.Subject == "" {
		t.Fatalf("the letter cannot be located or understood: %+v", letter)
	}
	if got := dead.tenants[0]; got != "acme" {
		t.Fatalf("the letter was written under tenant %q; the real writer is fail-closed on the "+
			"context's tenant, so a wrong one loses every letter silently", got)
	}

	// 🔴 AND IT IS COUNTED. Without this the event is invisible: the shared result
	// vocabulary can only call it "failed", the same bucket as a database outage.
	if got := counterValue(t, consumer.ResponsesNotAnswerable); got != 1 {
		t.Fatalf("ResponsesNotAnswerable = %v, want 1", got)
	}
	// It is NOT the refused counter. That one means a device answering for a command it
	// does not own — an identity problem with a different remedy — and this device owns it.
	if got := counterValue(t, consumer.ResponsesRefused); got != 0 {
		t.Fatalf("ResponsesRefused = %v, want 0; this is a delivery outcome, not a forgery", got)
	}

	// 🔴 THE ANSWER WAS NOT WRITTEN ONTO THE ROW EITHER, and that is not an oversight. The
	// platform cannot tell this answer apart from one for a command no transport ever
	// carried: nothing in the response envelope names the dispatch it is answering. Settling
	// the command here would mean reporting an actuation complete on the strength of the
	// device's word alone, which is the hole the answerable set was made positive to close.
	after := loadByToken(t, api, ctx, "cmd-1")
	if after.Status != model.CommandQueued.String() {
		t.Fatalf("status = %s, want QUEUED unchanged", after.Status)
	}
	if after.RespondedTime.Valid || after.ResponsePayload != nil {
		t.Fatalf("the answer was written against a command no dispatcher was holding: %+v", after)
	}

	// ⚠️ THE COMMAND REMAINS DISPATCHABLE, SO IT WILL GO OUT AGAIN — to a device that has
	// already run it. That half needs the response to carry the dispatch it is answering,
	// which the wire contract does not yet do; it is not closed here and this test does not
	// pretend otherwise. What HAS changed is that the platform now says so, on a counter and
	// in a dead letter, instead of reporting the answer as recorded and going quiet.
}

// TestAnAnswerToADispatchedCommandStillSettlesIt is the counterweight, and without it the
// test above is satisfied by a consumer that dead-letters every response it ever sees.
//
// The same command, the same device, the same message — the one difference is that the
// publish succeeds, so the row is SENT when the answer arrives. It must settle normally,
// write no letter, and move no counter.
func TestAnAnswerToADispatchedCommandStillSettlesIt(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if err := procWith(api, &recordingWriter{}).deliverCommand(ctx, created, pathSweep); err != nil {
		t.Fatalf("deliverCommand: %v", err)
	}

	dead := &deadRecorder{}
	consumer := &CommandDeliveryProcessor{
		Api:                    api,
		area:                   "command-delivery",
		dead:                   deadletter.NewSink(dead, func(error) {}),
		ResponsesNotAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_ok_total"}),
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.NewConsumedMessage("inst-1.acme.command-responses.pump-1",
				[]byte(`{"commandToken":"cmd-1","success":true}`), 1, nil, nil),
		},
	}

	consumer.ProcessMessage(context.Background())

	if got := loadByToken(t, api, ctx, "cmd-1").Status; got != model.CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL; a real answer to a dispatched command must land", got)
	}
	if len(dead.msgs) != 0 {
		t.Fatalf("dead-lettered %d ordinary answers, want 0", len(dead.msgs))
	}
	if got := counterValue(t, consumer.ResponsesNotAnswerable); got != 0 {
		t.Fatalf("ResponsesNotAnswerable = %v on an ordinary answer, want 0", got)
	}
}

// loadByToken reads a command back through the public API.
func loadByToken(t *testing.T, api *model.Api, ctx context.Context, token string) *model.Command {
	t.Helper()
	matches, err := api.CommandsByToken(ctx, []string{token})
	if err != nil {
		t.Fatalf("loading %s: %v", token, err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 row for %s, got %d", token, len(matches))
	}
	return matches[0]
}

func statusOf(t *testing.T, api *model.Api, ctx context.Context, token string) string {
	t.Helper()
	return loadByToken(t, api, ctx, token).Status
}
