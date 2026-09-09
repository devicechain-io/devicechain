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
		Api:  api,
		area: "command-delivery",
		dead: deadletter.NewSink(dead, func(error) {}),
		DeliveryMetrics: DeliveryMetrics{
			ResponsesNotAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_total"}),
			ResponsesRefused:       prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_e2e_total"}),
		},
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.NewConsumedMessage("inst-1.acme.command-responses.pump-1",
				[]byte(`{"commandToken":"cmd-1","success":true}`), 1, nil, nil),
		},
	}

	if stop := consumer.ProcessMessage(context.Background()); stop {
		t.Fatal("this response must not stop the consumer loop")
	}

	// 🔴 THE ANSWER IS RECORDED. A dead letter is the only place it can now live: this
	// consumer will not write it against a command the platform still intends to deliver,
	// and it must not simply evaporate.
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
	//
	// 🔑 THIS IS ABOUT THIS CALL, NOT ABOUT THE COMMAND'S FUTURE. The letter written above
	// reaches DeadLetterWriteback, which settles commands from letters — so "nothing can
	// write this answer against a live command" is a claim about two consumers, not one, and
	// it is the write-back's reason gate that makes it true. See
	// TestTheWritebackDoesNotSettleACommandItsProducerDeclinedToSettle.
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
		Api:  api,
		area: "command-delivery",
		dead: deadletter.NewSink(dead, func(error) {}),
		DeliveryMetrics: DeliveryMetrics{
			ResponsesNotAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_ok_total"}),
		},
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

// TestTheWritebackDoesNotSettleACommandItsProducerDeclinedToSettle closes the loop the
// test above leaves open, and it is about the SECOND consumer of the letter.
//
// 🔴 A DEAD LETTER IS NOT INERT HERE. DeadLetterWriteback reads the same stream and, for
// any command-response letter, calls MarkResponseLost — which drives a SENT or PARKED row
// to FAILED. That is right for the letter it was built for: the redelivery cap gave up on
// a write, so the command is genuinely unanswerable and must stop reading as in flight.
//
// It is WRONG for the letter written when the platform declined to write at all. The
// sequence needs nothing exotic:
//
//  1. publish reports an error, ReleaseClaim returns the command to QUEUED;
//  2. the device answers, and the answer is recorded as a letter rather than written;
//  3. the sweep re-dispatches the command — the device actuates a SECOND time — so the
//     row is SENT again, under a new nonce;
//  4. the letter is consumed, matches the SENT row, and stamps FAILED.
//
// The device's real answer to the second dispatch then arrives on a terminal row and is
// dropped as late. The command ends FAILED, unanswered, having run twice. That is the
// mis-filing the response consumer refuses by not retrying — reached through the other
// door.
//
// 🔑 IT IS NOT ONLY A RACE. A write-back that lands before the re-dispatch is a harmless
// no-op (the control below), so ordinarily the letter wins in milliseconds. It loses
// whenever the letter is redelivered late — a transient database error leaves it unacked
// for AckWait, which is longer than a sweep tick — and it needs no race at all if the row
// is re-claimed between the failed UPDATE and its re-read. A disposition that depends on
// winning a race is not a disposition, so this is fixed at the reason, not at the timing.
func TestTheWritebackDoesNotSettleACommandItsProducerDeclinedToSettle(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if err := procWith(api, &failingWriter{err: errors.New("publish acknowledgement timed out")}).
		deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("premise lost: the publish was expected to report an error")
	}

	dead := &deadRecorder{}
	consumer := &CommandDeliveryProcessor{
		Api:  api,
		area: "command-delivery",
		dead: deadletter.NewSink(dead, func(error) {}),
		DeliveryMetrics: DeliveryMetrics{
			ResponsesNotAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_wb_total"}),
		},
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.NewConsumedMessage("inst-1.acme.command-responses.pump-1",
				[]byte(`{"commandToken":"cmd-1","success":true}`), 1, nil, nil),
		},
	}
	consumer.ProcessMessage(context.Background())
	if len(dead.msgs) != 1 {
		t.Fatalf("premise lost: wrote %d letters, want 1", len(dead.msgs))
	}

	// Step 3: the sweep re-dispatches. This is the second actuation, and asserting it is
	// what makes the final state a lie rather than merely a wrong status.
	requeued := loadByToken(t, api, ctx, "cmd-1")
	redispatch := &recordingWriter{}
	if err := procWith(api, redispatch).deliverCommand(ctx, requeued, pathSweep); err != nil {
		t.Fatalf("re-dispatch: %v", err)
	}
	if redispatch.count() != 1 {
		t.Fatalf("premise lost: the command was published %d times on re-dispatch", redispatch.count())
	}
	if got := statusOf(t, api, ctx, "cmd-1"); got != model.CommandSent.String() {
		t.Fatalf("premise lost: after re-dispatch the command is %s, want SENT", got)
	}

	// Step 4: the letter this run actually produced, on the stream the write-back reads.
	// The REAL bytes, not a reconstruction — a hand-built envelope here would stop
	// measuring what the producer writes the day the two drift apart.
	w := newTestWriteback(t, api)
	ack := &countingAck{}
	w.Handle(messaging.NewConsumedMessage(messaging.ScopedSubject("inst", "acme", "dead-letters"),
		dead.msgs[0].Value, 1, nil, ack))

	after := loadByToken(t, api, ctx, "cmd-1")
	if after.Status == model.CommandFailed.String() {
		t.Fatalf("the write-back stamped FAILED on a command that had just been dispatched again; "+
			"its device's real answer will now be dropped as late (error=%q)", after.Error.String)
	}
	if after.Status != model.CommandSent.String() {
		t.Fatalf("status = %s, want SENT left alone", after.Status)
	}
	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1; the letter must not stay on the stream", ack.acks)
	}
}

// TestTheWritebackStillSettlesAnExhaustedLetter is the counterweight, and without it the
// test above is satisfied by a write-back that settles nothing at all — which would
// silently restore the gap that consumer was built to close.
//
// Same command, same SENT row, same call. The one difference is the letter's REASON:
// exhausted means the platform tried to record the answer and could not, so the command is
// genuinely unanswerable and must stop reading as in flight.
func TestTheWritebackStillSettlesAnExhaustedLetter(t *testing.T) {
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

	w := newTestWriteback(t, api)
	ack := &countingAck{}
	w.Handle(letter(t, "acme", responseLetter("cmd-1"), 1, ack))

	after := loadByToken(t, api, ctx, "cmd-1")
	if after.Status != model.CommandFailed.String() {
		t.Fatalf("status = %s, want FAILED; a command whose answer was lost must stop reading "+
			"as in flight, which is the whole reason this consumer exists", after.Status)
	}
	if after.Error.String != model.ResponseLostReason {
		t.Fatalf("error = %q, want the lost-response reason", after.Error.String)
	}
}

// TestTheWritebackIsANoOpBeforeTheRedispatch is the control the reading of the race
// depends on: while the command is still QUEUED the write-back misses its predicate and
// does nothing, whatever reason the letter carries. It is here so the fix above cannot be
// mistaken for the reason the ordinary case is safe — it was already safe, by timing, and
// timing is what the fix removes the dependence on.
func TestTheWritebackIsANoOpBeforeTheRedispatch(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if err := procWith(api, &failingWriter{err: errors.New("publish acknowledgement timed out")}).
		deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("premise lost: the publish was expected to report an error")
	}

	w := newTestWriteback(t, api)
	w.Handle(letter(t, "acme", responseLetter("cmd-1"), 1, &countingAck{}))

	if got := statusOf(t, api, ctx, "cmd-1"); got != model.CommandQueued.String() {
		t.Fatalf("status = %s, want QUEUED; a queued command is not one the platform has "+
			"given up on delivering", got)
	}
}
