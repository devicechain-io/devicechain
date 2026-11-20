// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/rs/zerolog"
)

// causeRule sends a command and carries the given extra actions after it, so the cause has to be
// found among the command's SIBLINGS, not in front of it.
func causeRule(extra ...rules.Action) rules.Rule {
	r := rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)},
		Actions: []rules.Action{{Type: rules.ActionSendCommand,
			SendCommand: &rules.SendCommandAction{Command: "derate"}}}}
	r.Actions = append(r.Actions, extra...)
	return r
}

func raiseAction(key, template, guard string) rules.Action {
	return rules.Action{Type: rules.ActionRaiseAlarm, Guard: guard,
		RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: key, AlarmKeyTemplate: template}}
}

func causeEvt(value *float64) runtime.DerivedEvent {
	return runtime.DerivedEvent{
		RuleID: "acme/p@1/r1", Tenant: "acme", Kind: "threshold", Series: "pump-01",
		Edge: runtime.EdgeRaised, Value: value,
		OccurredTime: time.Date(2026, 11, 20, 9, 15, 30, 123456789, time.UTC),
	}
}

// sentCause dispatches ev under rule and returns the cause of the one command it sent, with the
// drop reasons the dispatcher reported.
func sentCause(t *testing.T, rule rules.Rule, ev runtime.DerivedEvent) (*CommandCause, []string) {
	t.Helper()
	sink := &fakeSink{}
	var drops []string
	d := NewDispatcher(fakeResolver{rule: rule, found: true}, sink, &fakeAlarmSink{}, nil, nil, newFakeMetrics(),
		WithLogger(zerolog.Nop()), WithDropObserver(func(r string) { drops = append(drops, r) }))
	d.Dispatch(context.Background(), ev)
	if len(sink.sent) != 1 {
		t.Fatalf("want one command, got %d", len(sink.sent))
	}
	if sink.sent[0].Cause == nil {
		t.Fatal("a REACT command must carry its cause")
	}
	return sink.sent[0].Cause, drops
}

// A rule that raises an alarm and sends a command: the command names the detection AND the alarm
// key its sibling raised, whatever order the two actions are listed in.
func TestACommandNamesTheAlarmItsRuleRaised(t *testing.T) {
	v := 92.5
	for name, rule := range map[string]rules.Rule{
		"alarm after the command": causeRule(raiseAction("pump-overheat", "", "")),
		"alarm before the command": func() rules.Rule {
			r := causeRule()
			r.Actions = append([]rules.Action{raiseAction("pump-overheat", "", "")}, r.Actions...)
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			cause, _ := sentCause(t, rule, causeEvt(&v))
			if cause.Kind != CauseKindAlarm || cause.AlarmKey != "pump-overheat" {
				t.Fatalf("cause = %+v, want an ALARM cause naming pump-overheat", cause)
			}
			if cause.RuleID != "acme/p@1/r1" || cause.DetectionKind != "threshold" {
				t.Fatalf("cause names the wrong detection: %+v", cause)
			}
			if !cause.DetectedAt.Equal(causeEvt(nil).OccurredTime) {
				t.Fatalf("DetectedAt = %v, want the detection's event time to the nanosecond", cause.DetectedAt)
			}
			if cause.Value == nil || *cause.Value != v {
				t.Fatalf("Value = %v, want the triggering value %v", cause.Value, v)
			}
			if cause.PositionDerived {
				t.Fatal("a threshold rule is not position-derived")
			}
		})
	}
}

// The alarm key is the key the alarm is FILED under: a rendered template, and the stable rule key
// when the action names none.
func TestTheCausedAlarmKeyIsTheKeyTheAlarmIsFiledUnder(t *testing.T) {
	cause, _ := sentCause(t, causeRule(raiseAction("", `"overtemp-" + series`, "")), causeEvt(nil))
	if cause.AlarmKey != "overtemp-pump-01" {
		t.Fatalf("templated key = %q, want overtemp-pump-01", cause.AlarmKey)
	}
	cause, _ = sentCause(t, causeRule(raiseAction("", "", "")), causeEvt(nil))
	if cause.AlarmKey != "p/r1" {
		t.Fatalf("default key = %q, want the stable rule key p/r1", cause.AlarmKey)
	}
}

// A rule with no alarm, or whose alarm does not raise on this detection, sends a RULE cause — and
// asking does not report the sibling's failure a second time (the raise reports it once).
func TestACommandWithoutARaisedAlarmNamesTheRule(t *testing.T) {
	low := 10.0
	for _, tc := range []struct {
		name      string
		rule      rules.Rule
		wantDrops int
	}{
		{"no alarm action", causeRule(), 0},
		{"alarm guarded out", causeRule(raiseAction("hot", "", `value > 50.0`)), 0},
		{"alarm key does not render", causeRule(raiseAction("", `"bad key with spaces " + series`, "")), 1},
		{"alarm guard errors", causeRule(raiseAction("hot", "", `value.missing > 1.0`)), 1},
		{"alarm variant missing", causeRule(rules.Action{Type: rules.ActionRaiseAlarm}), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause, drops := sentCause(t, tc.rule, causeEvt(&low))
			if cause.Kind != CauseKindRule || cause.AlarmKey != "" {
				t.Fatalf("cause = %+v, want a RULE cause with no alarm key", cause)
			}
			if len(drops) != tc.wantDrops {
				t.Fatalf("drops = %v, want %d (the raise reports its own failure once)", drops, tc.wantDrops)
			}
		})
	}
}

// The first raiseAlarm that RAISES is named, skipping one guarded out ahead of it.
func TestTheFirstRaisingAlarmIsNamed(t *testing.T) {
	v := 70.0
	rule := causeRule(raiseAction("critical-hot", "", `value > 90.0`), raiseAction("warm", "", ""))
	cause, _ := sentCause(t, rule, causeEvt(&v))
	if cause.AlarmKey != "warm" {
		t.Fatalf("AlarmKey = %q, want warm (critical-hot is guarded out)", cause.AlarmKey)
	}
}

// Two alarms that both raise: the FIRST in list order is named, and a guard that passes counts as
// raising.
func TestTheFirstOfTwoRaisingAlarmsIsNamed(t *testing.T) {
	v := 95.0
	rule := causeRule(raiseAction("critical-hot", "", `value > 90.0`), raiseAction("warm", "", ""))
	cause, _ := sentCause(t, rule, causeEvt(&v))
	if cause.Kind != CauseKindAlarm || cause.AlarmKey != "critical-hot" {
		t.Fatalf("cause = %+v, want critical-hot (listed first, its guard passes)", cause)
	}
}

// A geofence rule's command cause is position-derived, so command-delivery withholds it from a
// reader without location:read; an uncompilable rule fails closed the same way.
func TestAGeofenceRulesCauseIsPositionDerived(t *testing.T) {
	fence := causeRule()
	fence.When = rules.Condition{CEL: `!geo.inFence("yard")`}
	if _, err := rules.Compile(fence, rules.DefaultLimits()); err != nil {
		t.Fatalf("fixture does not compile: %v", err)
	}
	if cause, _ := sentCause(t, fence, causeEvt(nil)); !cause.PositionDerived {
		t.Fatal("a geofence rule's cause must be position-derived")
	}
	broken := causeRule()
	broken.When = rules.Condition{CEL: `this is not cel`}
	if cause, _ := sentCause(t, broken, causeEvt(nil)); !cause.PositionDerived {
		t.Fatal("a rule that cannot be judged must be treated as position-derived")
	}
}
