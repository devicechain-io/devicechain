// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// reactFakeResolver returns a canned rule / not-found / error for the dispatcher under test.
// calls, when non-nil, counts Resolve invocations so a test can corroborate that the resolve
// path was actually reached (a value receiver still shares the pointed-at counter).
type reactFakeResolver struct {
	rule  rules.Rule
	found bool
	err   error
	calls *int
}

func (r reactFakeResolver) Resolve(context.Context, string) (rules.Rule, bool, error) {
	if r.calls != nil {
		*r.calls++
	}
	return r.rule, r.found, r.err
}

// reactFakeSink records commands and can fail every Send (a command-delivery outage).
type reactFakeSink struct {
	sent []react.CommandRequest
	fail bool
}

func (s *reactFakeSink) Send(_ context.Context, req react.CommandRequest) error {
	if s.fail {
		return errors.New("command-delivery unreachable")
	}
	s.sent = append(s.sent, req)
	return nil
}

// newTestReactDispatcher builds a ReactDispatcher over the given resolver+sink with no metrics
// (nil-safe) and a live loop context, for direct handle() testing.
func newTestReactDispatcher(resolver react.RuleResolver, sink react.CommandSink) *ReactDispatcher {
	rd := &ReactDispatcher{
		dispatcher: react.NewDispatcher(resolver, sink, nil, nil, nil, NewReactMetrics(nil)),
	}
	rd.procCtx = context.Background()
	return rd
}

func derivedMsg(t *testing.T, tenant string, ev runtime.DerivedEvent, numDelivered int, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return messaging.NewConsumedMessage("dc."+tenant+".derived-events", b, numDelivered, nil, ack)
}

func sendCmdEvent() runtime.DerivedEvent {
	return runtime.DerivedEvent{
		RuleID: "acme/p@1/r1", Tenant: "acme", Kind: "threshold", Series: "device-1",
		OccurredTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
}

func sendCmdRule() rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		Actions: []rules.Action{{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "setMode"}}}}
}

// TestReactHandleDispatchesAndAcks proves a resolvable send-command event dispatches to the sink and
// is acked.
func TestReactHandleDispatchesAndAcks(t *testing.T) {
	sink := &reactFakeSink{}
	rd := newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true}, sink)
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 0, ack))

	if len(sink.sent) != 1 || sink.sent[0].DeviceToken != "device-1" || sink.sent[0].Command != "setMode" {
		t.Fatalf("expected one command dispatched to device-1: %+v", sink.sent)
	}
	if ack.acks != 1 {
		t.Fatalf("a dispatched event must ack once: acks=%d", ack.acks)
	}
}

// TestReactHandleLeavesTransientFailureUnackedBelowCap proves a sink failure below the redelivery cap
// is left UNACKED (AckWait-paced retry, never an immediate nak that would burn MaxDeliver in ~1.4ms),
// not acked.
func TestReactHandleLeavesTransientFailureUnackedBelowCap(t *testing.T) {
	rd := newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true}, &reactFakeSink{fail: true})
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, ack))
	if ack.acks != 0 {
		t.Fatalf("a transient failure below the cap must be left unacked (AckWait retry), not acked: acks=%d", ack.acks)
	}
}

// TestReactHandleDropsPoisonAtCap proves an event that keeps failing is dropped (acked) once the
// redelivery cap is reached, so it cannot redeliver forever.
func TestReactHandleDropsPoisonAtCap(t *testing.T) {
	rd := newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true}, &reactFakeSink{fail: true})
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, ack))
	if ack.acks != 1 {
		t.Fatalf("at the redelivery cap a failing event must be dropped (acked): acks=%d", ack.acks)
	}
}

// TestReactHandleDropsUndecodable proves a non-JSON payload is poison — acked, never dispatched.
func TestReactHandleDropsUndecodable(t *testing.T) {
	sink := &reactFakeSink{}
	rd := newTestReactDispatcher(reactFakeResolver{found: true}, sink)
	ack := &fakeAck{}
	rd.handle(messaging.NewConsumedMessage("dc.acme.derived-events", []byte("not json"), 0, nil, ack))
	if ack.acks != 1 || len(sink.sent) != 0 {
		t.Fatalf("an undecodable event must be acked and not dispatched: acks=%d sent=%d", ack.acks, len(sink.sent))
	}
}

// TestReactHandleDropsTenantMismatch proves a payload whose tenant disagrees with the subject is
// dropped (acked), never dispatched — the defense-in-depth guard.
func TestReactHandleDropsTenantMismatch(t *testing.T) {
	sink := &reactFakeSink{}
	rd := newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true}, sink)
	ack := &fakeAck{}
	ev := sendCmdEvent()
	ev.Tenant = "evil" // subject says acme
	rd.handle(derivedMsg(t, "acme", ev, 0, ack))
	if ack.acks != 1 || len(sink.sent) != 0 {
		t.Fatalf("a tenant-mismatched event must be dropped and not dispatched: acks=%d sent=%d", ack.acks, len(sink.sent))
	}
}

// TestReactHandleDropsRuleTenantMismatch proves the rule-id tenant backstop: an event on tenant
// acme's subject carrying a rule id owned by another tenant is dropped (acked), never dispatched —
// so a forged event cannot enqueue another tenant's authored command content.
func TestReactHandleDropsRuleTenantMismatch(t *testing.T) {
	sink := &reactFakeSink{}
	rd := newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true}, sink)
	ack := &fakeAck{}
	ev := sendCmdEvent()
	ev.RuleID = "beta/p@1/r1" // rule id owned by beta, but the subject/payload tenant is acme
	rd.handle(derivedMsg(t, "acme", ev, 0, ack))
	if ack.acks != 1 || len(sink.sent) != 0 {
		t.Fatalf("a rule-tenant-mismatched event must be dropped and not dispatched: acks=%d sent=%d", ack.acks, len(sink.sent))
	}
}

// TestReactHandleOrphanAcks proves a resolvable-as-gone rule acks (nothing to dispatch, no retry).
func TestReactHandleOrphanAcks(t *testing.T) {
	sink := &reactFakeSink{}
	rd := newTestReactDispatcher(reactFakeResolver{found: false}, sink)
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 0, ack))
	if ack.acks != 1 || len(sink.sent) != 0 {
		t.Fatalf("an orphan event must ack without dispatch: acks=%d sent=%d", ack.acks, len(sink.sent))
	}
}

// TestReactHandleLeavesResolverErrorUnacked proves a transient store failure is left UNACKED for
// AckWait-paced retry, never dropping the event's actions.
func TestReactHandleLeavesResolverErrorUnacked(t *testing.T) {
	calls := 0
	rd := newTestReactDispatcher(reactFakeResolver{err: errors.New("store down"), calls: &calls}, &reactFakeSink{})
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 0, ack))
	// Corroborate that the resolve path was actually reached, so acks==0 means "reached the
	// transient arm and deliberately left unacked", not "early-returned without touching the message".
	if calls != 1 {
		t.Fatalf("the resolver was invoked %d times, want 1: the failure path was not reached", calls)
	}
	if ack.acks != 0 {
		t.Fatalf("a resolver error must be left unacked (retry), not acked: acks=%d", ack.acks)
	}
}

// deadRecorder captures what an arm writes, so the test can assert on the letter rather
// than only on the fact that something was written.
//
// 🔴 IT RECORDS THE CONTEXT'S TENANT, AND THAT IS NOT DECORATION. The real writer scopes
// the subject from the context and is FAIL-CLOSED on a context without one — so an arm
// handed the wrong context writes nothing, counts a loss, and acks. A fake that ignores
// its context makes that outcome indistinguishable from success, and a mutation passing
// context.Background() survived every one of these tests until this field existed.
type deadRecorder struct {
	msgs    []messaging.Message
	tenants []string
	err     error
}

func (d *deadRecorder) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if d.err != nil {
		return d.err
	}
	tenant, _ := core.TenantFromContext(ctx)
	for range msgs {
		d.tenants = append(d.tenants, tenant)
	}
	d.msgs = append(d.msgs, msgs...)
	return nil
}

func (d *deadRecorder) letters(t *testing.T) []deadletter.Envelope {
	t.Helper()
	out := make([]deadletter.Envelope, 0, len(d.msgs))
	for _, m := range d.msgs {
		e, err := deadletter.Unmarshal(m.Value)
		if err != nil {
			t.Fatalf("a written dead letter does not read back: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func reactDispatcherWithSink(resolver react.RuleResolver, sink react.CommandSink,
	dead deadletter.Writer) *ReactDispatcher {
	return constructedReactDispatcher(&core.Microservice{FunctionalArea: "event-processing"},
		resolver, sink, dead)
}

// reactDispatcherWithRegistry is reactDispatcherWithSink over a Microservice with a
// registry, so the loss counter can be read back by the name it EXPORTS under — the name
// the alert selects on.
func reactDispatcherWithRegistry(resolver react.RuleResolver, sink react.CommandSink,
	dead deadletter.Writer) (*ReactDispatcher, *prometheus.Registry) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	return constructedReactDispatcher(ms, resolver, sink, dead), reg
}

// constructedReactDispatcher builds the dispatcher the way main.go does: through
// NewReactDispatcher, with the sink from a dead-letter producer on ms.
//
// 🔴 THROUGH THE CONSTRUCTOR, NOT A STRUCT LITERAL. A literal sets the sink itself, so a
// constructor that dropped the one it was handed would leave every dead-letter test here
// green while the dispatcher main.go builds dead-lettered nothing: a nil sink is the
// DISABLED shape by design, and it drops the event without a word.
//
// procCtx is set by hand because Start, which sets it in production, also launches the
// read loop, and these tests drive handle directly.
func constructedReactDispatcher(ms *core.Microservice, resolver react.RuleResolver,
	sink react.CommandSink, dead deadletter.Writer) *ReactDispatcher {
	rd := NewReactDispatcher(ms, nil, resolver, sink, nil, nil, nil,
		deadletter.NewProducer(ms).NewSink(dead), NewReactMetrics(ms))
	rd.procCtx = context.Background()
	return rd
}

const reactLost = "devicechain_eventprocessing_dead_letter_lost_total"

// gatheredCounter reads a plain counter off reg by its full exported name.
func gatheredCounter(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("the registry exports no %s", name)
	return 0
}

// 🔴 THE ARM. An event whose actions could not be dispatched used to end as a log line and
// a counter; the letter is what makes it something an operator can look at.
func TestReactDeadLettersAtTheCap(t *testing.T) {
	dead := &deadRecorder{}
	rd := reactDispatcherWithSink(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true}, dead)
	ack := &fakeAck{}

	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver+2, ack))

	letters := dead.letters(t)
	if len(letters) != 1 {
		t.Fatalf("wrote %d dead letters at the cap, want 1", len(letters))
	}
	e := letters[0]
	if e.Kind != deadletter.KindDetectionAction {
		t.Fatalf("kind = %q", e.Kind)
	}
	if e.Source != "event-processing" {
		t.Fatalf("source = %q, want the service that wrote it", e.Source)
	}
	if e.Reference != "acme/p@1/r1" {
		t.Fatalf("the letter does not name the rule that fired: %q", e.Reference)
	}
	// A value ABOVE the cap, so a hard-coded messaging.MaxDeliver would be visible here.
	if e.Attempts != messaging.MaxDeliver+2 {
		t.Fatalf("attempts = %d, want the message's own count %d", e.Attempts, messaging.MaxDeliver+2)
	}
	if e.Subject == "" || e.OccurredAt.IsZero() || len(e.Payload) == 0 {
		t.Fatalf("the letter cannot be located or understood: %+v", e)
	}
	if dead.tenants[0] != "acme" {
		t.Fatalf("the letter was written under tenant %q; the real writer is fail-closed on "+
			"the context's tenant, so a wrong one loses every letter silently", dead.tenants[0])
	}
	// 🔑 THE ACK STILL HAPPENS. This runs at the cap, so no redelivery follows whatever
	// the consumer does — leaving it unacked would strand the message, not retry it.
	if ack.acks != 1 {
		t.Fatalf("a dead-lettered event must still be acked: acks=%d", ack.acks)
	}
}

// 🔴 AND NOT BELOW THE CAP. An event still being retried has not been given up on, and a
// letter for it would say the platform had stopped trying when it had not.
func TestReactDoesNotDeadLetterBelowTheCap(t *testing.T) {
	dead := &deadRecorder{}
	rd := reactDispatcherWithSink(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true}, dead)

	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{}))

	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters below the cap, want 0", len(dead.msgs))
	}
}

// 🔴 THE UNATTRIBUTABLE PATHS STAY DROPS. A message with no parseable tenant, a forged
// tenant, or an undecodable body is not work the platform accepted and failed to finish —
// writing it to a tenant's dead-letter subject would file it against a tenant it was never
// demonstrably from.
func TestReactDoesNotDeadLetterWhatItCannotAttribute(t *testing.T) {
	for name, msg := range map[string]messaging.Message{
		"undecodable": messaging.NewConsumedMessage("dc.acme.derived-events", []byte("not json"),
			messaging.MaxDeliver, nil, &fakeAck{}),
		"no tenant in subject": messaging.NewConsumedMessage("derived-events", []byte("{}"),
			messaging.MaxDeliver, nil, &fakeAck{}),
	} {
		t.Run(name, func(t *testing.T) {
			dead := &deadRecorder{}
			rd := reactDispatcherWithSink(reactFakeResolver{found: true}, &reactFakeSink{}, dead)
			rd.handle(msg)
			if len(dead.msgs) != 0 {
				t.Fatalf("an unattributable message was dead-lettered: %s", name)
			}
		})
	}

	// The payload-tenant mismatch, which needs a well-formed event to reach. 🔴 THE SINK
	// MUST FAIL HERE: with a working sink the event dispatches successfully and nothing
	// would be dead-lettered whatever the guard did, which is how the first version of
	// this half passed with the guard deleted.
	dead := &deadRecorder{}
	rd := reactDispatcherWithSink(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true}, dead)
	ev := sendCmdEvent()
	ev.Tenant = "globex"
	rd.handle(derivedMsg(t, "acme", ev, messaging.MaxDeliver, &fakeAck{}))
	if len(dead.msgs) != 0 {
		t.Fatal("a forged-tenant event was dead-lettered under the subject's tenant")
	}
}

// 🔴 A DISPATCHER WITH NO SINK MUST STILL DROP RATHER THAN PANIC. That is the shape a
// deployment without the stream has, and the shape every other test in this file builds.
func TestReactWithNoDeadLetterSinkStillDrops(t *testing.T) {
	rd := newTestReactDispatcher(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true})
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, ack))
	if ack.acks != 1 {
		t.Fatalf("acks=%d", ack.acks)
	}
}

// 🔴 THE COUNTERS ARE WHAT THE ALERT RESTS ON, so swapping them has to fail here. Nothing
// read them until this test existed, and the pair is exactly the kind that reads the same
// either way round: one says "recorded", the other says "gone".
func TestTheDeadLetterCountersAreNotSwapped(t *testing.T) {
	written := &deadRecorder{}
	ok, okReg := reactDispatcherWithRegistry(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true}, written)
	ok.metrics = newTestReactMetrics()
	ok.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, &fakeAck{}))

	if got := counterOf(t, ok.metrics.deadLettered); got != 1 {
		t.Fatalf("a written letter counted %v on deadLettered, want 1", got)
	}
	if got := gatheredCounter(t, okReg, reactLost); got != 0 {
		t.Fatalf("a written letter counted %v as LOST", got)
	}

	broken := &deadRecorder{err: errors.New("broker is away")}
	bad, badReg := reactDispatcherWithRegistry(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true}, broken)
	bad.metrics = newTestReactMetrics()
	bad.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, &fakeAck{}))

	if got := gatheredCounter(t, badReg, reactLost); got != 1 {
		t.Fatalf("a LOST letter counted %v on %s, want 1", got, reactLost)
	}
	if got := counterOf(t, bad.metrics.deadLettered); got != 0 {
		t.Fatalf("a LOST letter counted %v as written — the alert would never fire", got)
	}
}

// newTestReactMetrics builds the counters this file asserts on, off the global registry
// so repeated construction cannot collide.
func newTestReactMetrics() *ReactMetrics {
	return &ReactMetrics{
		poisonDropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "poison_total"}),
		deadLettered:  prometheus.NewCounter(prometheus.CounterOpts{Name: "dl_total"}),
	}
}

func counterOf(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("reading a counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// A write that never succeeds must not stop the event being acked: no redelivery follows,
// so leaving it unacked would strand it on top of losing it.
func TestReactAcksEvenWhenTheDeadLetterWriteFails(t *testing.T) {
	dead := &deadRecorder{err: errors.New("broker is away")}
	rd := reactDispatcherWithSink(reactFakeResolver{rule: sendCmdRule(), found: true},
		&reactFakeSink{fail: true}, dead)
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), messaging.MaxDeliver, ack))
	if ack.acks != 1 {
		t.Fatalf("a lost dead letter must still ack its source: acks=%d", ack.acks)
	}
}
