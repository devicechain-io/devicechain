// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

func f64(v float64) *float64 { return &v }

// raisedEvt is a rising-edge derived event carrying a value.
func raisedEvt(value *float64) runtime.DerivedEvent {
	return runtime.DerivedEvent{
		RuleID: "acme/p@1/r1", Tenant: "acme", Kind: "threshold", Series: "device-1",
		Edge: runtime.EdgeRaised, Value: value,
		OccurredTime: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
	}
}

func guardedSendRule(guard string) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold,
		Actions: []rules.Action{{Type: rules.ActionSendCommand,
			SendCommand: &rules.SendCommandAction{Command: "cool"}, Guard: guard}}}
}

func guardedAlarmRule(guard string) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm,
			RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}, Guard: guard}}}
}

// TestGuardGatesSendCommand: a sendCommand whose guard is false for the detection is skipped (Done,
// nothing sent); the same rule with a value that passes the guard sends.
func TestGuardGatesSendCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    *float64
		wantSent int
	}{
		{"guard false → skipped", f64(50), 0},
		{"guard true → sent", f64(150), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeSink{}
			d := NewDispatcher(fakeResolver{rule: guardedSendRule(`value > 100.0`), found: true}, sink, nil, newFakeMetrics())
			if out := d.Dispatch(context.Background(), raisedEvt(tc.value)); out != Done {
				t.Fatalf("outcome = %v, want Done", out)
			}
			if len(sink.sent) != tc.wantSent {
				t.Fatalf("sent %d commands, want %d", len(sink.sent), tc.wantSent)
			}
		})
	}
}

// TestGuardGatesRaiseButNeverClear is the load-bearing correctness case: a guard gates the RAISE on
// the rising edge, but NEVER the structural clear on the falling edge — otherwise a guard whose
// inputs changed between raise and resolve would strand the alarm active forever.
func TestGuardGatesRaiseButNeverClear(t *testing.T) {
	// Rising edge, guard false (value 50 ≤ 100): no raise.
	alarm := &fakeAlarmSink{}
	d := NewDispatcher(fakeResolver{rule: guardedAlarmRule(`value > 100.0`), found: true}, nil, alarm, newFakeMetrics())
	if out := d.Dispatch(context.Background(), raisedEvt(f64(50))); out != Done {
		t.Fatalf("rising/guard-false: outcome = %v, want Done", out)
	}
	if len(alarm.raised) != 0 {
		t.Fatalf("rising/guard-false: dispatched %d alarm requests, want 0 (guarded out)", len(alarm.raised))
	}

	// Rising edge, guard true (value 150 > 100): raise.
	alarm = &fakeAlarmSink{}
	d = NewDispatcher(fakeResolver{rule: guardedAlarmRule(`value > 100.0`), found: true}, nil, alarm, newFakeMetrics())
	if out := d.Dispatch(context.Background(), raisedEvt(f64(150))); out != Done {
		t.Fatalf("rising/guard-true: outcome = %v, want Done", out)
	}
	if len(alarm.raised) != 1 || alarm.raised[0].Edge != runtime.EdgeRaised {
		t.Fatalf("rising/guard-true: want one raised request, got %+v", alarm.raised)
	}

	// Falling edge: the clear ALWAYS dispatches, regardless of the guard (a resolved carries no
	// value, so the guard `value > 100.0` would be false — proving the clear does not consult it).
	alarm = &fakeAlarmSink{}
	d = NewDispatcher(fakeResolver{rule: guardedAlarmRule(`value > 100.0`), found: true}, nil, alarm, newFakeMetrics())
	resolved := raisedEvt(nil)
	resolved.Edge = runtime.EdgeResolved
	if out := d.Dispatch(context.Background(), resolved); out != Done {
		t.Fatalf("falling: outcome = %v, want Done", out)
	}
	if len(alarm.raised) != 1 || alarm.raised[0].Edge != runtime.EdgeResolved {
		t.Fatalf("falling: the structural clear must dispatch regardless of the guard, got %+v", alarm.raised)
	}
}

// TestNoGuardIsUnconditional: an action with no guard dispatches exactly as before 9c.
func TestNoGuardIsUnconditional(t *testing.T) {
	sink := &fakeSink{}
	d := NewDispatcher(fakeResolver{rule: guardedSendRule(""), found: true}, sink, nil, newFakeMetrics())
	if out := d.Dispatch(context.Background(), raisedEvt(f64(1))); out != Done {
		t.Fatalf("outcome = %v, want Done", out)
	}
	if len(sink.sent) != 1 {
		t.Fatalf("an unguarded action must always dispatch, sent %d", len(sink.sent))
	}
}

// TestGuardProgramCached: repeated dispatches of the same guarded rule reuse one compiled program
// (the cache is keyed by guard source), so the second dispatch does not recompile.
func TestGuardProgramCached(t *testing.T) {
	d := NewDispatcher(fakeResolver{rule: guardedSendRule(`value > 100.0`), found: true}, &fakeSink{}, nil, newFakeMetrics())
	ev := raisedEvt(f64(150))
	_ = d.Dispatch(context.Background(), ev)
	first, ok := d.guards.Load(`value > 100.0`)
	if !ok {
		t.Fatal("guard was not cached after the first dispatch")
	}
	_ = d.Dispatch(context.Background(), ev)
	second, _ := d.guards.Load(`value > 100.0`)
	if first != second {
		t.Fatal("guard program was rebuilt rather than reused from the cache")
	}
}
