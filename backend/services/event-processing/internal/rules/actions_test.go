// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"encoding/json"
	"strings"
	"testing"
)

// thresholdWith is a minimal, always-valid threshold rule the action tests hang severity/actions
// off, so a failure isolates to the REACT validation and never the detection lowering.
func thresholdWith(sev Severity, actions ...Action) Rule {
	return Rule{
		ID: "acme/prof@1/r1", Name: "hot", Type: TypeThreshold,
		When:     Condition{Metric: "temperature", Op: OpGt, Threshold: ptr(30)},
		Severity: sev, Actions: actions,
	}
}

// TestCompileCarriesSeverity proves a valid severity survives compile onto the CompiledRule (the
// field the publisher stamps onto the derived event), and an empty severity stays empty.
func TestCompileCarriesSeverity(t *testing.T) {
	cr, err := Compile(thresholdWith(SeverityMajor), testLimits)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if cr.Severity != SeverityMajor {
		t.Fatalf("carried severity = %q, want %q", cr.Severity, SeverityMajor)
	}
	cr, err = Compile(thresholdWith(""), testLimits)
	if err != nil {
		t.Fatalf("compile no-severity: %v", err)
	}
	if cr.Severity != "" {
		t.Fatalf("carried severity = %q, want empty", cr.Severity)
	}
}

// TestCompileRejectsUnknownSeverity proves an out-of-set severity is a publish-time rejection.
func TestCompileRejectsUnknownSeverity(t *testing.T) {
	_, err := Compile(thresholdWith("catastrophic"), testLimits)
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("want a severity error, got %v", err)
	}
}

// TestCompileValidSendCommandAction proves a well-formed sendCommand action compiles, with and
// without a payload.
func TestCompileValidSendCommandAction(t *testing.T) {
	for _, payload := range []string{"", `{"mode":"eco"}`} {
		r := thresholdWith("", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "setMode", Payload: payload}})
		if _, err := Compile(r, testLimits); err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
	}
}

// TestCompileValidRaiseAlarmActionNeedsSeverity proves a raiseAlarm action compiles only with a
// valid rule severity — an alarm cannot be raised without a tier.
func TestCompileValidRaiseAlarmActionNeedsSeverity(t *testing.T) {
	raise := Action{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{}}
	if _, err := Compile(thresholdWith(SeverityCritical, raise), testLimits); err != nil {
		t.Fatalf("raiseAlarm with severity should compile: %v", err)
	}
	_, err := Compile(thresholdWith("", raise), testLimits)
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("raiseAlarm without severity should be rejected, got %v", err)
	}
}

// TestCompileRejectsMalformedActions sweeps the fail-closed cases: wrong/missing payload for the
// type, a foreign payload set, an invalid command/alarm token, non-JSON payload, unknown type,
// and an over-cap chain.
func TestCompileRejectsMalformedActions(t *testing.T) {
	cases := []struct {
		name string
		a    Action
	}{
		{"raiseAlarm missing payload", Action{Type: ActionRaiseAlarm}},
		{"raiseAlarm foreign payload", Action{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{}, SendCommand: &SendCommandAction{Command: "x"}}},
		{"raiseAlarm bad alarmKey", Action{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{AlarmKey: "bad key!"}}},
		{"sendCommand missing payload", Action{Type: ActionSendCommand}},
		{"sendCommand empty command", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: ""}}},
		{"sendCommand bad command token", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "bad cmd!"}}},
		{"sendCommand non-json payload", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "c", Payload: "not json"}}},
		{"sendCommand scalar payload", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "c", Payload: "42"}}},
		{"sendCommand array payload", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "c", Payload: "[1,2]"}}},
		{"sendCommand null payload", Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "c", Payload: "null"}}},
		{"unknown type", Action{Type: "reboot"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A critical severity is supplied so the failure is the ACTION, never a missing tier.
			if _, err := Compile(thresholdWith(SeverityCritical, tc.a), testLimits); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

// TestCompileRejectsOverCapActionChain proves the MaxActionsPerRule backstop fires.
func TestCompileRejectsOverCapActionChain(t *testing.T) {
	actions := make([]Action, MaxActionsPerRule+1)
	for i := range actions {
		actions[i] = Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "c"}}
	}
	_, err := Compile(thresholdWith("", actions...), testLimits)
	if err == nil || !strings.Contains(err.Error(), "actions") {
		t.Fatalf("want an over-cap actions error, got %v", err)
	}
}

// TestCompileRejectsDuplicateActions proves an exact-duplicate action is a publish-time rejection
// (a double sendCommand would otherwise dispatch twice), while two sendCommands that differ only in
// payload are legitimately distinct and both compile.
func TestCompileRejectsDuplicateActions(t *testing.T) {
	dup := Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "setMode", Payload: `{"mode":"eco"}`}}
	_, err := Compile(thresholdWith("", dup, dup), testLimits)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want a duplicate-action error, got %v", err)
	}
	// Same command, different payloads: distinct, both allowed.
	a := Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "setMode", Payload: `{"mode":"eco"}`}}
	b := Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "setMode", Payload: `{"mode":"boost"}`}}
	if _, err := Compile(thresholdWith("", a, b), testLimits); err != nil {
		t.Fatalf("distinct payloads should both compile: %v", err)
	}
	// Two raiseAlarm actions with distinct alarm keys are distinct, both allowed.
	r1 := Action{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{AlarmKey: "over-temp"}}
	r2 := Action{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{AlarmKey: "over-pressure"}}
	if _, err := Compile(thresholdWith(SeverityMajor, r1, r2), testLimits); err != nil {
		t.Fatalf("distinct alarm keys should both compile: %v", err)
	}
}

// TestActionRuleByteIdentity proves a rule carrying severity + actions round-trips byte-identically
// — the ADR-053 authoring contract holds for the REACT fields the console form/canvas emit.
func TestActionRuleByteIdentity(t *testing.T) {
	r := thresholdWith(SeverityMajor,
		Action{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{AlarmKey: "over-temp"}},
		Action{Type: ActionSendCommand, SendCommand: &SendCommandAction{Command: "setMode", Payload: `{"mode":"eco"}`}},
	)
	first, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// Decode is the fail-closed reader every consumer uses (DisallowUnknownFields); it must
	// accept the new severity/actions fields, not reject them as unknown.
	back, err := Decode(first)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	second, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("round-trip not byte-identical:\n first=%s\nsecond=%s", first, second)
	}
	if _, err := Compile(back, testLimits); err != nil {
		t.Fatalf("round-tripped action rule should compile: %v", err)
	}
}
