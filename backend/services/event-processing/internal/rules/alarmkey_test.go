// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"strings"
	"testing"
)

const testAlarmKeyCeiling uint64 = 100

func alarmTemplateRule(key, template string) Rule {
	return Rule{
		ID: "acme/p@1/r1", Name: "r", Type: TypeThreshold, Severity: SeverityCritical,
		When:    Condition{Metric: "tempC", Op: OpGt, Threshold: ptr(80)},
		Actions: []Action{{Type: ActionRaiseAlarm, RaiseAlarm: &RaiseAlarmAction{AlarmKey: key, AlarmKeyTemplate: template}}},
	}
}

// 🔴 The vocabulary restriction, asserted where an author meets it. `value` is UNDECLARED in the
// alarm-key environment on purpose: a key built from the crossing sample renders one string on the
// rising edge and another on the falling edge, which strands the alarm ACTIVE forever with nothing
// able to clear it. A publish-time type error is the only place that failure is visible at all —
// each edge, seen alone at runtime, looks perfectly well-formed.
func TestAlarmKeyTemplateRejectsEdgeUnstableVocabulary(t *testing.T) {
	for _, src := range []string{
		`"k-" + string(value)`,
		`hasValue ? "k-hot" : "k-cold"`,
	} {
		if _, err := CompileAlarmKeyTemplate(src, testAlarmKeyCeiling); err == nil {
			t.Fatalf("CompileAlarmKeyTemplate(%q) must be rejected: a key that reads the value is not edge-stable", src)
		}
	}
}

// The counterweight: rejecting the value vocabulary is only useful while the series vocabulary — the
// entire point of the feature — still compiles.
func TestAlarmKeyTemplateAcceptsSeries(t *testing.T) {
	if _, err := CompileAlarmKeyTemplate(`"overtemp-" + series`, testAlarmKeyCeiling); err != nil {
		t.Fatalf("a series-only alarm-key template must compile: %v", err)
	}
}

// A non-string template is refused: the key is a string and a template that yields anything else
// could only be coerced by guessing.
func TestAlarmKeyTemplateMustYieldAString(t *testing.T) {
	if _, err := CompileAlarmKeyTemplate(`size(series)`, testAlarmKeyCeiling); err == nil {
		t.Fatal("an int-valued alarm-key template must be rejected")
	}
}

// Cost is gated at the tenant ceiling, like a guard and a payload template, so a runaway key
// expression is refused at publish rather than burned on every dispatch.
func TestAlarmKeyTemplateIsCostGated(t *testing.T) {
	src := `"` + strings.Repeat("a", 8) + `" + series + series + series + series + series + series`
	if _, err := CompileAlarmKeyTemplate(src, 1); err == nil {
		t.Fatal("an alarm-key template above the ceiling must be rejected")
	}
}

// The rendered key is grammar-checked at EVALUATION, because that is the only place the answer
// exists — it depends on the device token. A template that compiles cleanly can still render a key
// the platform cannot store.
func TestRenderedAlarmKeyIsGrammarChecked(t *testing.T) {
	prog, err := BuildAlarmKeyTemplateProgram(`"overtemp-" + series`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got, err := prog.Eval("pump-01"); err != nil || got != "overtemp-pump-01" {
		t.Fatalf("Eval(pump-01) = %q, %v; want overtemp-pump-01, nil", got, err)
	}
	if _, err := prog.Eval(strings.Repeat("d", 130)); err == nil {
		t.Fatal("a rendered key past the 128-character storage column must be refused")
	}
}

// An empty rendered key must be refused too. The raise-alarm consumer drops an empty key as
// malformed, so producing one is a silent no-raise; refusing it makes the defect loud.
func TestEmptyRenderedAlarmKeyIsRefused(t *testing.T) {
	prog, err := BuildAlarmKeyTemplateProgram(`""`)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := prog.Eval("pump-01"); err == nil {
		t.Fatal("an empty rendered alarm key must be refused")
	}
}

// A literal key and a rendered key are mutually exclusive at publish: with both set, either choice
// silently discards half of what the author wrote.
func TestAlarmKeyAndAlarmKeyTemplateAreMutuallyExclusive(t *testing.T) {
	if _, err := Compile(alarmTemplateRule("over-temp", `"overtemp-" + series`), DefaultLimits()); err == nil {
		t.Fatal("a raiseAlarm declaring both alarmKey and alarmKeyTemplate must be rejected")
	}
	// Each alone compiles — the counterweight that keeps the check from being a blanket refusal.
	if _, err := Compile(alarmTemplateRule("over-temp", ""), DefaultLimits()); err != nil {
		t.Fatalf("a literal alarm key must still compile: %v", err)
	}
	if _, err := Compile(alarmTemplateRule("", `"overtemp-" + series`), DefaultLimits()); err != nil {
		t.Fatalf("a rendered alarm key must compile: %v", err)
	}
}

// The publish gate runs the alarm-key template through the same cost/type gate, so a rule carrying a
// value-reading key is refused as a RULE, not merely by the standalone compiler.
func TestCompileRejectsAValueReadingAlarmKeyTemplate(t *testing.T) {
	if _, err := Compile(alarmTemplateRule("", `"k-" + string(value)`), DefaultLimits()); err == nil {
		t.Fatal("a rule whose alarm-key template reads the value must be refused at publish")
	}
}
