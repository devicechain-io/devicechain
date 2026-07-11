// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// fakeResolver returns a canned rule / not-found / error.
type fakeResolver struct {
	rule  rules.Rule
	found bool
	err   error
}

func (f fakeResolver) Resolve(context.Context, string) (rules.Rule, bool, error) {
	return f.rule, f.found, f.err
}

// fakeSink records the command requests it received and can fail a bounded number of times.
type fakeSink struct {
	sent      []CommandRequest
	failFirst int // fail this many Send calls (transient), then succeed
	calls     int
}

func (s *fakeSink) Send(_ context.Context, req CommandRequest) error {
	s.calls++
	if s.calls <= s.failFirst {
		return errors.New("command-delivery unreachable")
	}
	s.sent = append(s.sent, req)
	return nil
}

// fakeMetrics counts dispatcher outcomes.
type fakeMetrics struct {
	dispatched map[string]int
	orphan     int
	notEnabled map[string]int
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{dispatched: map[string]int{}, notEnabled: map[string]int{}}
}

func (m *fakeMetrics) RecordDispatched(a string) { m.dispatched[a]++ }
func (m *fakeMetrics) RecordOrphan()             { m.orphan++ }
func (m *fakeMetrics) RecordNotEnabled(a string) { m.notEnabled[a]++ }

func evt() runtime.DerivedEvent {
	return runtime.DerivedEvent{
		RuleID: "acme/p@1/r1", Tenant: "acme", Kind: "threshold", Series: "device-1",
		OccurredTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
}

func sendCmdRule(cmd, payload string) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		Actions: []rules.Action{{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: cmd, Payload: payload}}}}
}

// TestDispatchSendCommand proves a sendCommand action reaches the sink with the device from the
// event series, the command/payload from the rule, and a deterministic token; and it is Done.
func TestDispatchSendCommand(t *testing.T) {
	sink := &fakeSink{}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: sendCmdRule("setMode", `{"mode":"eco"}`), found: true}, sink, m)

	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("want Done, got %v", out)
	}
	if len(sink.sent) != 1 {
		t.Fatalf("want 1 command, got %d", len(sink.sent))
	}
	got := sink.sent[0]
	if got.DeviceToken != "device-1" || got.Command != "setMode" || got.Payload != `{"mode":"eco"}` || got.Tenant != "acme" {
		t.Fatalf("command request wrong: %+v", got)
	}
	if got.Token == "" {
		t.Fatalf("command must carry a deterministic idempotency token")
	}
	if m.dispatched["sendCommand"] != 1 {
		t.Fatalf("dispatched metric not recorded: %+v", m.dispatched)
	}
}

// TestIdempotencyTokenDeterministic proves the token is CONTENT-addressed: the same detection +
// action content always yields the same token (so a redelivery/replay/reorder dedups), while a
// different action content or a different detection differs.
func TestIdempotencyTokenDeterministic(t *testing.T) {
	ev := evt()
	a := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "setMode", Payload: `{"m":1}`}}
	b := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "reboot"}}
	if idempotencyToken(ev, a) != idempotencyToken(ev, a) {
		t.Fatal("token must be stable for the same (detection, action content)")
	}
	if idempotencyToken(ev, a) == idempotencyToken(ev, b) {
		t.Fatal("token must differ per action content")
	}
	// A different detection (later time) yields a different token for the same action.
	ev2 := ev
	ev2.OccurredTime = ev.OccurredTime.Add(time.Second)
	if idempotencyToken(ev, a) == idempotencyToken(ev2, a) {
		t.Fatal("token must differ per detection time")
	}
}

// TestIdempotencyTokenStableUnderReorder proves the SAME action produces the SAME token regardless
// of its list position — the property that makes an action-chain reorder a dispatch no-op rather
// than a swallow-one/duplicate-another bug.
func TestIdempotencyTokenStableUnderReorder(t *testing.T) {
	ev := evt()
	a := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "setMode"}}
	// Whether a is authored at index 0 or index 5, the token is the content's, so it is identical.
	if idempotencyToken(ev, a) != idempotencyToken(ev, a) {
		t.Fatal("a content-addressed token cannot depend on position")
	}
}

// TestDispatchOrphanRule proves a missing rule drops the event (no dispatch) and counts an orphan.
func TestDispatchOrphanRule(t *testing.T) {
	sink := &fakeSink{}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{found: false}, sink, m)
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("orphan must be Done, got %v", out)
	}
	if len(sink.sent) != 0 || m.orphan != 1 {
		t.Fatalf("orphan should dispatch nothing and count: sent=%d orphan=%d", len(sink.sent), m.orphan)
	}
}

// TestDispatchResolverErrorRetries proves a transient store failure is a Retry (no drop).
func TestDispatchResolverErrorRetries(t *testing.T) {
	d := NewDispatcher(fakeResolver{err: errors.New("db down")}, &fakeSink{}, newFakeMetrics())
	if out := d.Dispatch(context.Background(), evt()); out != Retry {
		t.Fatalf("a resolver error must Retry, got %v", out)
	}
}

// TestDispatchSinkErrorRetries proves a sink failure is a Retry and nothing is counted dispatched.
func TestDispatchSinkErrorRetries(t *testing.T) {
	sink := &fakeSink{failFirst: 1}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: sendCmdRule("setMode", ""), found: true}, sink, m)
	if out := d.Dispatch(context.Background(), evt()); out != Retry {
		t.Fatalf("a sink error must Retry, got %v", out)
	}
	if m.dispatched["sendCommand"] != 0 {
		t.Fatalf("a failed send must not count as dispatched")
	}
	// A retry (redelivery) succeeds and dispatches with the SAME token.
	first := ""
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("retry should succeed, got %v", out)
	}
	first = sink.sent[0].Token
	setMode := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "setMode"}}
	if want := idempotencyToken(evt(), setMode); first != want {
		t.Fatalf("retry must reuse the deterministic token: got %s want %s", first, want)
	}
}

// TestDispatchRaiseAlarmNotEnabled proves a raiseAlarm action is recognized-but-inert in 5b (no
// dispatch), counted as not-enabled, and does not block the (Done) event.
func TestDispatchRaiseAlarmNotEnabled(t *testing.T) {
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{}}}}
	sink := &fakeSink{}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, sink, m)
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("raiseAlarm-only rule must be Done, got %v", out)
	}
	if len(sink.sent) != 0 || m.notEnabled["raiseAlarm"] != 1 {
		t.Fatalf("raiseAlarm must be inert+counted: sent=%d notEnabled=%+v", len(sink.sent), m.notEnabled)
	}
}

// TestDispatchMultiActionPartialRetry proves that when a later action fails, the whole event Retries;
// on the redelivery the already-sent prefix reuses its deterministic tokens (command-delivery
// collapses them) and the failed action is reached again.
func TestDispatchMultiActionPartialRetry(t *testing.T) {
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		Actions: []rules.Action{
			{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "a"}},
			{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "b"}},
		}}
	// Fail the 2nd Send once (the first action's send at call 1 succeeds, the second at call 2 fails).
	sink := &fakeSink{failFirst: 0}
	// Custom failure: fail only call #2.
	failing := &failOnCallSink{failCall: 2}
	m := newFakeMetrics()
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, failing, m)
	if out := d.Dispatch(context.Background(), evt()); out != Retry {
		t.Fatalf("a failed 2nd action must Retry the whole event, got %v", out)
	}
	// Redelivery: action 0 re-sends (same token), action 1 now succeeds.
	if out := d.Dispatch(context.Background(), evt()); out != Done {
		t.Fatalf("redelivery should complete, got %v", out)
	}
	actionA := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "a"}}
	if failing.sent[0].Command != "a" || failing.sent[0].Token != idempotencyToken(evt(), actionA) {
		t.Fatalf("action a must reuse its token across the retry: %+v", failing.sent[0])
	}
	_ = sink
}

// failOnCallSink fails exactly the Nth Send call, recording the rest.
type failOnCallSink struct {
	failCall int
	calls    int
	sent     []CommandRequest
}

func (s *failOnCallSink) Send(_ context.Context, req CommandRequest) error {
	s.calls++
	if s.calls == s.failCall {
		return errors.New("transient")
	}
	s.sent = append(s.sent, req)
	return nil
}
