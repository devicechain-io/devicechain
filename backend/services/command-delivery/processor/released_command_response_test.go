// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
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

// failingWriter is a device-command writer that RECORDS what it was handed and then reports
// an error. It is the ONE ingredient the sequence needs that the platform cannot produce on
// demand: a publish outcome that says "failed" without proving nothing was delivered.
//
// 🔴 IT RECORDS BEFORE IT FAILS, AND THAT IS THE WHOLE POINT OF IT. The case being modelled
// is a publish that LANDED and whose acknowledgement was lost, so the envelope reached the
// device and the platform does not know it. A writer that dropped the message would model a
// different failure — one where nothing was delivered — and no test built on it could show a
// device answering a dispatch it really received. The recorded envelope is also how the test
// learns the dispatch nonce: from the wire, exactly as the device does, rather than from an
// API call the device has no access to.
type failingWriter struct {
	err      error
	messages []messaging.Message
}

func (w *failingWriter) WriteMessages(context.Context, ...messaging.Message) error { return w.err }
func (w *failingWriter) WriteToDevice(_ context.Context, _ string, msgs ...messaging.Message) error {
	w.messages = append(w.messages, msgs...)
	return w.err
}
func (w *failingWriter) HandleResponse(error) {}

// dispatchNonceOf reads the nonce out of the delivery envelope a writer captured, which is
// the only place a device ever sees it.
//
// It fails the test on an absent or empty nonce rather than returning one, because every
// test below asks a question about WHICH dispatch an answer names — and an empty string
// silently turns all of them into the no-nonce case, which is a different test that passes
// for different reasons.
func dispatchNonceOf(t *testing.T, msgs []messaging.Message) string {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("expected exactly one published command, got %d", len(msgs))
	}
	var env struct {
		DispatchNonce string `json:"dispatchNonce"`
	}
	if err := json.Unmarshal(msgs[0].Value, &env); err != nil {
		t.Fatalf("the published delivery envelope does not decode: %v", err)
	}
	if env.DispatchNonce == "" {
		t.Fatal("the published delivery envelope names no dispatch, so nothing the device " +
			"echoes back could ever settle this command")
	}
	return env.DispatchNonce
}

// answer is the JSON a device publishes: the outcome, naming the dispatch it acted on. It is
// built as a literal rather than from the production struct so the test states the WIRE, and
// a field renamed on that struct fails here instead of being renamed on both sides at once.
func answer(commandToken, dispatchNonce string) []byte {
	if dispatchNonce == "" {
		return []byte(`{"commandToken":"` + commandToken + `","success":true}`)
	}
	return []byte(`{"commandToken":"` + commandToken + `","success":true,"dispatchNonce":"` +
		dispatchNonce + `"}`)
}

// responseConsumer builds the response-consuming half of the processor over a real API, with
// every counter this file asserts on. Built by literal, as the rest of this package's tests
// build it — the constructor needs a live microservice.
func responseConsumer(api *model.Api, dead *deadRecorder, body []byte) *CommandDeliveryProcessor {
	return &CommandDeliveryProcessor{
		Api:  api,
		area: "command-delivery",
		dead: deadletter.NewSink(dead, func(error) {}),
		DeliveryMetrics: DeliveryMetrics{
			ResponsesNotAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_total"}),
			ResponsesRefused:       prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_e2e_total"}),
			ResponsesWithoutNonce:  prometheus.NewCounter(prometheus.CounterOpts{Name: "without_nonce_total"}),
			ResponsesStaleNonce:    prometheus.NewCounter(prometheus.CounterOpts{Name: "stale_nonce_total"}),
		},
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.NewConsumedMessage("inst-1.acme.command-responses.pump-1", body, 1, nil, nil),
		},
	}
}

// TestAnAnswerToAReleasedCommandSettlesItByTheNonceItNames drives the whole sequence end to
// end, over a real database, with no step faked except the publish failure.
//
// 🔴 THE SEQUENCE IS AN ORDINARY DAY, NOT A CONTRIVANCE:
//
//  1. A dispatcher claims the command, QUEUED -> SENT, stamping a dispatch nonce and
//     publishing it in the envelope.
//  2. The publish reports an error. That is not proof the device did not get it — a lost
//     acknowledgement looks exactly the same from here.
//  3. ReleaseClaim correctly returns the row to QUEUED so it can be tried again.
//  4. The device, which DID receive it, answers — quoting the nonce it was sent.
//
// 🔑 THE NONCE IS WHAT MAKES STEP 4 SETTLEABLE, AND NOTHING ELSE COULD. The row is QUEUED, a
// state no dispatcher is holding, so a status predicate must refuse it — and did, which left
// the answer in a dead letter and the command about to be dispatched to a device that had
// already run it. The echoed nonce is evidence of a different kind: it can only have come
// from an envelope the platform published, so this answer is provably for the dispatch that
// was released. The command settles once and is not dispatched again.
func TestAnAnswerToAReleasedCommandSettlesItByTheNonceItNames(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	// Steps 1-3, through the real dispatch path rather than by hand.
	writer := &failingWriter{err: errors.New("publish acknowledgement timed out")}
	if err := procWith(api, writer).deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("premise lost: the publish was expected to report an error")
	}
	nonce := dispatchNonceOf(t, writer.messages)

	// The premise, asserted rather than assumed: the release really did put a command the
	// device may already hold back into the dispatchable set.
	if got := statusOf(t, api, ctx, "cmd-1"); got != model.CommandQueued.String() {
		t.Fatalf("after a failed publish the command is %s, want QUEUED; without that the rest "+
			"of this test is measuring a sequence the platform does not produce", got)
	}

	// Step 4: the device answers the dispatch it did receive.
	dead := &deadRecorder{}
	consumer := responseConsumer(api, dead, answer("cmd-1", nonce))
	if stop := consumer.ProcessMessage(context.Background()); stop {
		t.Fatal("this response must not stop the consumer loop")
	}

	after := loadByToken(t, api, ctx, "cmd-1")
	if after.Status != model.CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL; the answer named the dispatch that was released, "+
			"which is exactly the evidence that the device received it", after.Status)
	}
	if !after.RespondedTime.Valid {
		t.Fatal("the command settled without recording when it was answered")
	}

	// 🔴 AND IT IS NOT DISPATCHED AGAIN. Settling it is only half the point: the defect this
	// closes is a device being told twice to do one thing, so the row must also have left the
	// dispatchable set. SUCCESSFUL is terminal, and the sweep's own source is asserted here
	// rather than inferred from the status.
	pending, err := api.PendingCommands(core.WithSystemContext(ctx))
	if err != nil {
		t.Fatalf("PendingCommands: %v", err)
	}
	for _, cmd := range pending {
		if cmd.Token == "cmd-1" {
			t.Fatal("the settled command is still dispatchable, so the device will be told to " +
				"run it a second time")
		}
	}

	// Nothing was refused, so nothing is recorded as a give-up.
	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters for an answer that settled its command, want 0", len(dead.msgs))
	}
	if got := counterValue(t, consumer.ResponsesNotAnswerable); got != 0 {
		t.Fatalf("ResponsesNotAnswerable = %v, want 0", got)
	}
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 0 {
		t.Fatalf("ResponsesStaleNonce = %v, want 0", got)
	}
}

// TestAnAnswerToASupersededDispatchDoesNotSettleTheOneThatReplacedIt is the defect this whole
// return path exists for, and it is the case a status predicate cannot see at all.
//
// The command is dispatched, its publish reports an error, and it is re-dispatched — so there
// are two dispatches in flight and the row reads SENT under the SECOND one. The device's
// answer to the FIRST then arrives.
//
// 🔴 SETTLING IT WOULD BE A LIE IN BOTH DIRECTIONS. The command would record an outcome for an
// actuation the device has not performed yet, and the device's real answer to the second
// dispatch would arrive on a terminal row and be discarded as late. Both dispatches leave the
// row reading SENT, so the only thing that tells them apart is the nonce.
func TestAnAnswerToASupersededDispatchDoesNotSettleTheOneThatReplacedIt(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	// The first dispatch: delivered, unacknowledged, released.
	lost := &failingWriter{err: errors.New("publish acknowledgement timed out")}
	if err := procWith(api, lost).deliverCommand(ctx, created, pathSweep); err == nil {
		t.Fatal("premise lost: the publish was expected to report an error")
	}
	released := dispatchNonceOf(t, lost.messages)

	// The second dispatch, by the sweep, on the row the release returned to the queue.
	requeued := loadByToken(t, api, ctx, "cmd-1")
	live := &recordingWriter{}
	if err := procWith(api, live).deliverCommand(ctx, requeued, pathSweep); err != nil {
		t.Fatalf("re-dispatching the released command: %v", err)
	}
	current := dispatchNonceOf(t, live.messages)
	if current == released {
		t.Fatal("premise lost: the second dispatch reused the first dispatch's nonce, so nothing " +
			"in the exchange could tell the two apart")
	}

	// The device answers the FIRST dispatch.
	dead := &deadRecorder{}
	consumer := responseConsumer(api, dead, answer("cmd-1", released))
	consumer.ProcessMessage(context.Background())

	after := loadByToken(t, api, ctx, "cmd-1")
	if after.Status != model.CommandSent.String() {
		t.Fatalf("status = %s, want SENT untouched; the answer to a superseded dispatch settled "+
			"the one that replaced it", after.Status)
	}
	if after.RespondedTime.Valid || after.ResponsePayload != nil {
		t.Fatalf("a refused answer was still written onto the row: %+v", after)
	}

	// It is recorded rather than dropped: the answer is real and came from the right device.
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
	// 🔑 UNPROCESSABLE, NOT EXHAUSTED, and the difference is what the letter TELLS a reader —
	// and, downstream, what the write-back does with it. Exhausted means the platform tried
	// and lost the work, which is the reason that settles a command to FAILED; this work was
	// looked at once and declined, and the command is legitimately still in flight.
	if letter.Reason != deadletter.ReasonUnprocessable {
		t.Fatalf("letter reason = %q, want %q", letter.Reason, deadletter.ReasonUnprocessable)
	}
	if got := dead.tenants[0]; got != "acme" {
		t.Fatalf("the letter was written under tenant %q; the real writer is fail-closed on the "+
			"context's tenant, so a wrong one loses every letter silently", got)
	}
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 1 {
		t.Fatalf("ResponsesStaleNonce = %v, want 1; this event is invisible without it", got)
	}
	if got := counterValue(t, consumer.ResponsesRefused); got != 0 {
		t.Fatalf("ResponsesRefused = %v, want 0; this is a delivery outcome, not a forgery", got)
	}

	// 🔴 THE COUNTERWEIGHT, IN THE SAME TEST: the dispatch that IS in flight still settles.
	// Without this the refusal above is satisfied by a consumer that refuses everything, and
	// the command would be left to expire as TIMEOUT against a device that answered twice.
	second := responseConsumer(api, &deadRecorder{}, answer("cmd-1", current))
	second.ProcessMessage(context.Background())
	if got := loadByToken(t, api, ctx, "cmd-1").Status; got != model.CommandSuccessful.String() {
		t.Fatalf("status = %s, want SUCCESSFUL; the answer to the LIVE dispatch must land", got)
	}
}

// TestAnAnswerNamingNoDispatchIsRefusedAndRecorded pins the decision that a response carrying
// no dispatch nonce cannot settle a command.
//
// 🔴 THIS IS THE DELIBERATELY BREAKING HALF, AND THE COUNTER IS WHY IT IS SAFE TO SHIP. Every
// client in this repository echoes the nonce; a device built against the older contract does
// not, and its commands stop settling on the day this lands. The alternative — accepting it on
// the status alone and merely counting it — leaves the platform unable to tell an answer to
// the dispatch it holds from an answer to one it released, which is the entire property being
// bought. So the answer is refused, written to the dead-letter stream so it is not lost, and
// counted on its own series so an operator can see whether any such client is still out there.
func TestAnAnswerNamingNoDispatchIsRefusedAndRecorded(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	// Dispatched normally: the row is SENT, which is the state that WOULD have accepted this
	// answer before the nonce was required. Anything weaker would pass for other reasons.
	if err := procWith(api, &recordingWriter{}).deliverCommand(ctx, created, pathSweep); err != nil {
		t.Fatalf("deliverCommand: %v", err)
	}

	dead := &deadRecorder{}
	consumer := responseConsumer(api, dead, answer("cmd-1", ""))
	if stop := consumer.ProcessMessage(context.Background()); stop {
		t.Fatal("this response must not stop the consumer loop")
	}

	after := loadByToken(t, api, ctx, "cmd-1")
	if after.Status != model.CommandSent.String() {
		t.Fatalf("status = %s, want SENT untouched; an answer that names no dispatch settled a "+
			"command anyway", after.Status)
	}
	if after.RespondedTime.Valid || after.ResponsePayload != nil {
		t.Fatalf("a refused answer was still written onto the row: %+v", after)
	}
	if len(dead.msgs) != 1 {
		t.Fatalf("wrote %d dead letters, want 1; the device's answer was discarded", len(dead.msgs))
	}
	letter, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if letter.Reason != deadletter.ReasonUnprocessable {
		t.Fatalf("letter reason = %q, want %q", letter.Reason, deadletter.ReasonUnprocessable)
	}
	if len(letter.Payload) == 0 || letter.Subject == "" {
		t.Fatalf("the letter cannot be located or understood: %+v", letter)
	}
	// 🔴 THE COUNTER IS THE OPERATOR'S ONLY VIEW OF THE POPULATION THIS RULE BREAKS.
	if got := counterValue(t, consumer.ResponsesWithoutNonce); got != 1 {
		t.Fatalf("ResponsesWithoutNonce = %v, want 1; without it a fleet whose commands have "+
			"silently stopped settling is invisible", got)
	}
	// It is not any of the neighbouring refusals: each names a different remedy.
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 0 {
		t.Fatalf("ResponsesStaleNonce = %v, want 0; this answer named no dispatch at all", got)
	}
	if got := counterValue(t, consumer.ResponsesNotAnswerable); got != 0 {
		t.Fatalf("ResponsesNotAnswerable = %v, want 0", got)
	}
	if got := counterValue(t, consumer.ResponsesRefused); got != 0 {
		t.Fatalf("ResponsesRefused = %v, want 0; this is not a device answering for another", got)
	}
}

// TestAnAnswerToADispatchedCommandStillSettlesIt is the counterweight, and without it every
// test above is satisfied by a consumer that dead-letters every response it ever sees.
//
// The ordinary round trip, with nothing gone wrong: the publish succeeds, the row is SENT, and
// the device answers quoting the nonce it was sent. It must settle normally, write no letter,
// and move no counter.
func TestAnAnswerToADispatchedCommandStillSettlesIt(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	writer := &recordingWriter{}
	if err := procWith(api, writer).deliverCommand(ctx, created, pathSweep); err != nil {
		t.Fatalf("deliverCommand: %v", err)
	}

	dead := &deadRecorder{}
	consumer := responseConsumer(api, dead, answer("cmd-1", dispatchNonceOf(t, writer.messages)))
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
	if got := counterValue(t, consumer.ResponsesWithoutNonce); got != 0 {
		t.Fatalf("ResponsesWithoutNonce = %v on an ordinary answer, want 0", got)
	}
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 0 {
		t.Fatalf("ResponsesStaleNonce = %v on an ordinary answer, want 0", got)
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
