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
	"github.com/devicechain-io/dc-microservice/messaging"
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
		dispatcher: react.NewDispatcher(resolver, sink, nil, nil, nil, newReactMetrics(nil)),
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
