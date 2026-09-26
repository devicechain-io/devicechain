// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// 🔑 WHAT THIS FILE PINS. A rule's actions used to run in list order with the first failure ending
// the attempt, so every retry re-ran the actions before it and never reached the ones after it: a
// command that could not be enqueued for a few minutes cost the alarm listed after it, for good.
// Every action is now attempted on every delivery, each failure is reported with its kind and
// token, and a retry's re-send of an action that already succeeded goes out under the same token,
// which is what lets its sink collapse it.

var (
	indCmd   = rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "reboot"}}
	indAlarm = rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}}
	indHTTP  = rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://x.example/h"}}
)

func independentRule() rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{indCmd, indAlarm, indHTTP}}
}

// independentSinks are the three sinks of independentRule, with the one at failAt failing on every
// call.
type independentSinks struct {
	cmd   *fakeSink
	alarm *fakeAlarmSink
	conn  *fakeConnectorSink
}

func newIndependentSinks(failAt int) independentSinks {
	s := independentSinks{cmd: &fakeSink{}, alarm: &fakeAlarmSink{}, conn: &fakeConnectorSink{}}
	switch failAt {
	case 0:
		s.cmd.failFirst = 1 << 30
	case 1:
		s.alarm.fail = true
	case 2:
		s.conn.fail = true
	}
	return s
}

func (s independentSinks) dispatcher(m *fakeMetrics) *Dispatcher {
	return NewDispatcher(fakeResolver{rule: independentRule(), found: true}, s.cmd, s.alarm, s.conn, nil, m)
}

// Whichever action fails, the other two are dispatched on the same attempt — exactly once, with
// their own content — and the failure names the one that failed, by kind and by the token its
// sink was sent.
func TestAFailingActionDoesNotStopItsSiblings(t *testing.T) {
	ev := evt()
	cmdToken := idempotencyToken(ev, indCmd)
	alarmTok := alarmToken(ev, indAlarm)
	httpToken := idempotencyToken(ev, indHTTP)
	for _, tc := range []struct {
		name   string
		failAt int
		want   FailedAction
	}{
		{"the command fails", 0, FailedAction{Kind: "sendCommand", Token: cmdToken}},
		{"the alarm fails", 1, FailedAction{Kind: "raiseAlarm", Token: alarmTok}},
		{"the connector fails", 2, FailedAction{Kind: "httpCall", Token: httpToken}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sinks := newIndependentSinks(tc.failAt)
			res := sinks.dispatcher(newFakeMetrics()).Dispatch(context.Background(), ev)

			if res.Outcome != Retry {
				t.Fatalf("Outcome = %v, want Retry", res.Outcome)
			}
			if want := []FailedAction{tc.want}; !reflect.DeepEqual(res.Failed, want) {
				t.Fatalf("Failed = %+v, want %+v", res.Failed, want)
			}
			// Every sink was reached exactly once on this attempt, the failing one included.
			if len(sinks.cmd.attempted) != 1 || len(sinks.alarm.attempted) != 1 || len(sinks.conn.attempted) != 1 {
				t.Fatalf("each action must be attempted exactly once: command=%d alarm=%d connector=%d",
					len(sinks.cmd.attempted), len(sinks.alarm.attempted), len(sinks.conn.attempted))
			}
			if tc.failAt != 0 {
				if len(sinks.cmd.sent) != 1 || sinks.cmd.sent[0].Command != "reboot" || sinks.cmd.sent[0].Token != cmdToken {
					t.Fatalf("the command must be sent once under its token: %+v", sinks.cmd.sent)
				}
			}
			if tc.failAt != 1 {
				r := sinks.alarm.raised
				if len(r) != 1 || r[0].AlarmKey != "overheat" || r[0].RuleID != stableContributorID(ev.RuleID) || r[0].Token != alarmTok {
					t.Fatalf("the alarm must be raised once under its edge token: %+v", r)
				}
			}
			if tc.failAt != 2 {
				if len(sinks.conn.got) != 1 || sinks.conn.got[0].Token != httpToken || sinks.conn.got[0].Action.HTTPCall.URL != "https://x.example/h" {
					t.Fatalf("the connector action must be published once under its token: %+v", sinks.conn.got)
				}
			}
		})
	}

	// On a falling edge the only side effect is the structural clear, and its failure is named as
	// what it is — a clearAlarm — under the RESOLVED edge's token.
	t.Run("a failing clear is named clearAlarm", func(t *testing.T) {
		sinks := newIndependentSinks(1)
		res := sinks.dispatcher(newFakeMetrics()).Dispatch(context.Background(), resolvedEvt())
		want := []FailedAction{{Kind: "clearAlarm", Token: alarmToken(resolvedEvt(), indAlarm)}}
		if res.Outcome != Retry || !reflect.DeepEqual(res.Failed, want) {
			t.Fatalf("got %v %+v, want Retry %+v", res.Outcome, res.Failed, want)
		}
	})
}

// Two failures are two entries, in rule order, and the success between them still happened.
func TestDispatchReportsEveryFailedActionInRuleOrder(t *testing.T) {
	ev := evt()
	sinks := independentSinks{cmd: &fakeSink{failFirst: 1 << 30}, alarm: &fakeAlarmSink{}, conn: &fakeConnectorSink{fail: true}}
	m := newFakeMetrics()
	res := sinks.dispatcher(m).Dispatch(context.Background(), ev)

	want := []FailedAction{
		{Kind: "sendCommand", Token: idempotencyToken(ev, indCmd)},
		{Kind: "httpCall", Token: idempotencyToken(ev, indHTTP)},
	}
	if res.Outcome != Retry || !reflect.DeepEqual(res.Failed, want) {
		t.Fatalf("got %v %+v, want Retry %+v", res.Outcome, res.Failed, want)
	}
	if len(sinks.alarm.raised) != 1 {
		t.Fatalf("the alarm between the two failures must still be raised once, got %d", len(sinks.alarm.raised))
	}
	if m.dispatched["raiseAlarm"] != 1 || m.dispatched["sendCommand"] != 0 || m.dispatched["httpCall"] != 0 {
		t.Fatalf("only the alarm was dispatched: %+v", m.dispatched)
	}
}

// A retry sends every action again, and each one that already succeeded goes out under the SAME
// token as before — the property its sink's dedup rests on.
func TestARetryResendsEverySucceededActionUnderTheSameToken(t *testing.T) {
	ev := evt()
	sinks := newIndependentSinks(0)
	d := sinks.dispatcher(newFakeMetrics())
	if res := d.Dispatch(context.Background(), ev); res.Outcome != Retry {
		t.Fatalf("first attempt: %v, want Retry", res.Outcome)
	}
	sinks.cmd.failFirst = 0 // command-delivery is back
	res := d.Dispatch(context.Background(), ev)
	if res.Outcome != Done || len(res.Failed) != 0 {
		t.Fatalf("second attempt: %v %+v, want Done with nothing failed", res.Outcome, res.Failed)
	}
	if a := sinks.alarm.raised; len(a) != 2 || a[0].Token == "" || a[0].Token != a[1].Token {
		t.Fatalf("the alarm must be re-published under one stable token: %+v", a)
	}
	if c := sinks.conn.got; len(c) != 2 || c[0].Token == "" || c[0].Token != c[1].Token {
		t.Fatalf("the connector request must be re-published under one stable token: %+v", c)
	}
	if len(sinks.cmd.sent) != 1 || sinks.cmd.sent[0].Token != idempotencyToken(ev, indCmd) {
		t.Fatalf("the command must be enqueued once, on the attempt that succeeded: %+v", sinks.cmd.sent)
	}
}

// Negative control for "always report": when nothing fails, nothing is reported and the event is
// Done.
func TestAllActionsSucceedingIsDoneWithNoFailures(t *testing.T) {
	sinks := newIndependentSinks(-1)
	m := newFakeMetrics()
	res := sinks.dispatcher(m).Dispatch(context.Background(), evt())
	if res.Outcome != Done || len(res.Failed) != 0 {
		t.Fatalf("got %v %+v, want Done with nothing failed", res.Outcome, res.Failed)
	}
	if m.dispatched["sendCommand"] != 1 || m.dispatched["raiseAlarm"] != 1 || m.dispatched["httpCall"] != 1 {
		t.Fatalf("every action must be dispatched once: %+v", m.dispatched)
	}
}

// The alarm dedup key must tell a detection's raised and resolved edges apart even when they share
// an event time — otherwise the broker would drop the resolve as a repeat of the raise and strand
// the alarm active — while staying identical across redeliveries of one edge, and staying tenant-
// scoped through the rule id.
func TestAlarmTokenIsEdgeScopedAndRedeliveryStable(t *testing.T) {
	tokenOf := func(ev runtime.DerivedEvent) string {
		t.Helper()
		sink := &fakeAlarmSink{}
		rule := independentRule()
		rule.ID = ev.RuleID
		rule.Actions = []rules.Action{indAlarm}
		if res := NewDispatcher(fakeResolver{rule: rule, found: true}, nil, sink, nil, nil, newFakeMetrics()).
			Dispatch(context.Background(), ev); res.Outcome != Done || len(sink.raised) != 1 {
			t.Fatalf("dispatch: %v, %d requests", res.Outcome, len(sink.raised))
		}
		return sink.raised[0].Token
	}
	raised := evt()
	resolved := evt()
	resolved.Edge = runtime.EdgeResolved // the SAME OccurredTime as the raise

	up, down := tokenOf(raised), tokenOf(resolved)
	if up == "" || down == "" {
		t.Fatalf("an alarm request must carry a token: raised=%q resolved=%q", up, down)
	}
	if up == down {
		t.Fatal("a raise and a resolve at one event time share a dedup key: the broker would drop the resolve")
	}
	if again := tokenOf(raised); again != up {
		t.Fatalf("a redelivery of one edge must reuse its token: %q then %q", up, again)
	}
	other := evt()
	other.RuleID = "beta/p@1/r1"
	other.Tenant = "beta"
	if tokenOf(other) == up {
		t.Fatal("two tenants' identical detections share an alarm dedup key")
	}
}

// A retried detection charges the outbound gate for its connector actions on EVERY attempt, not
// once: the re-publish is collapsed at the stream, but the charge is not. This is a decision
// (the alternative lets an action shed on its first delivery out uncharged on a retry), and this
// test is what makes changing it one rather than an accident.
func TestARetriedDetectionChargesItsConnectorActionsOnEveryAttempt(t *testing.T) {
	const attempts = 5
	gate := &fakeGate{admit: true}
	conn := &fakeConnectorSink{}
	rule := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Actions: []rules.Action{indCmd, indHTTP}}
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, &fakeSink{failFirst: 1 << 30}, nil, conn, gate, newFakeMetrics())
	for i := 0; i < attempts; i++ {
		if res := d.Dispatch(context.Background(), evt()); res.Outcome != Retry {
			t.Fatalf("attempt %d: %v, want Retry", i+1, res.Outcome)
		}
	}
	if len(gate.charged) != attempts {
		t.Fatalf("the gate was charged %d times over %d attempts, want one charge per attempt", len(gate.charged), attempts)
	}
	if len(conn.got) != attempts {
		t.Fatalf("the connector action was published %d times, want once per attempt", len(conn.got))
	}
	for _, r := range conn.got {
		if r.Token != conn.got[0].Token {
			t.Fatalf("every re-publish must carry the one token its stream collapses on: %+v", conn.got)
		}
	}
}
