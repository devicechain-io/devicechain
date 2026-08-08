// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---- Shared scaffolding for the scenarios' rule tests -------------------------
//
// The decode mirror below existed three times in this package before it was pulled
// here. That is a different case from the authored-rules fixture's DELIBERATE
// per-module mirrors: those are separate tripwires in separate modules, each forcing its
// owner to judge a producer-side change. Three copies inside ONE package are one
// tripwire duplicated, and the only thing they buy is three places to forget.
//
// What stays per-scenario is every EXPECTED value — each scenario's threshold, metric,
// alarm key and severity are its own design, and a shared expectation would make these
// tests agree with each other rather than with anything real.

// thresholdRuleDoc is the shape of the rules.Rule document a ThresholdAlarmRule renders,
// as a consumer of the published bytes sees it.
type thresholdRuleDoc struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Severity string `json:"severity"`
	When     struct {
		Metric    string  `json:"metric"`
		Op        string  `json:"op"`
		Threshold float64 `json:"threshold"`
	} `json:"when"`
	Actions []struct {
		Type       string `json:"type"`
		RaiseAlarm struct {
			AlarmKey string `json:"alarmKey"`
		} `json:"raiseAlarm"`
		// 🔴 THE MIRROR MUST CARRY EVERY VARIANT THE BUILDER CAN RENDER, and its missing
		// sendCommand is what this comment exists to stop happening again. Decoding here is
		// plain json.Unmarshal, which is LENIENT — a document carrying an action shape the
		// mirror does not name does not fail, it is silently dropped. So every assertion
		// written against this struct went on judging one action of a two-action chain
		// while reading exactly like it judged the whole thing, and a builder that emitted
		// the wrong command key, or emitted one when it should not have, was asserted by
		// nothing at all. A new variant on ThresholdAlarmRule gets mirrored HERE in the
		// same change, or its tests are measuring the fixture instead of the code.
		SendCommand struct {
			Command string `json:"command"`
			// Nothing renders a payload today, and it is mirrored so a test can assert
			// that — but as a POINTER, because a string cannot say it.
			//
			// The distinction is load-bearing rather than pedantic. The publish gate reads
			// `payload != "" && !isJSONObject(payload)`, so `"payload":""` is ACCEPTED —
			// empty is treated as absent, not as a malformed object. There is therefore no
			// downstream gate that would catch a builder emitting an empty payload key, and
			// a `Payload string` mirror decodes an absent key and an emitted-but-empty one
			// to the identical "" — so `Payload != ""` would be a check that cannot fail,
			// dressed as an absence assertion. nil means the key was not emitted; a non-nil
			// pointer to "" means it was emitted empty, which is the case that has no other
			// alarm anywhere.
			Payload *string `json:"payload"`
		} `json:"sendCommand"`
	} `json:"actions"`
}

func decodeThresholdRule(t *testing.T, definition string) thresholdRuleDoc {
	t.Helper()
	var doc thresholdRuleDoc
	if err := json.Unmarshal([]byte(definition), &doc); err != nil {
		t.Fatalf("rule definition is not decodable: %v", err)
	}
	return doc
}

// profileByToken returns the named profile from a manifest, failing loudly rather than
// returning a zero value a caller would then assert against.
func profileByToken(t *testing.T, m SimManifest, token string) ProfileSpec {
	t.Helper()
	for _, p := range m.Profiles {
		if p.Token == token {
			return p
		}
	}
	t.Fatalf("manifest %q declares no profile %q", m.Name, token)
	return ProfileSpec{}
}

// soleDetectionRule returns the one rule a profile declares.
func soleDetectionRule(t *testing.T, p ProfileSpec) DetectionRuleSpec {
	t.Helper()
	if len(p.DetectionRules) != 1 {
		t.Fatalf("profile %q declares %d detection rules, want exactly 1", p.Token, len(p.DetectionRules))
	}
	return p.DetectionRules[0]
}

// ---- The type itself ----------------------------------------------------------

// The seam ThresholdAlarmRule exists to close: DetectionRuleSpec.Metric and the
// document's own when.metric are two statements of ONE fact, written separately at every
// call site before this type. A rule DECLARING one metric while READING another passes
// every JSON check and simply never fires on the curve its scenario drives.
//
// Manifest.Validate cross-checks the two, which is what made the old arrangement safe;
// this asserts the stronger property that they cannot differ in the first place.
func TestThresholdAlarmRuleSpecFillsBothMetricsFromOneField(t *testing.T) {
	spec := ThresholdAlarmRule{
		Token:     "t-rule",
		Name:      "Test rule",
		Metric:    "some_metric",
		Op:        OpGt,
		Threshold: 12.5,
		Severity:  SeverityMajor,
		AlarmKey:  "t-alarm",
		Enabled:   true,
	}.Spec()

	if spec.Metric != "some_metric" {
		t.Errorf("spec declares metric %q, want the field's %q", spec.Metric, "some_metric")
	}
	doc := decodeThresholdRule(t, spec.Definition)
	if doc.When.Metric != spec.Metric {
		t.Errorf("the rule READS %q while its spec DECLARES %q — the seam this type exists to "+
			"close is open", doc.When.Metric, spec.Metric)
	}
	// The rest of the round trip, so a field silently dropped from the rendered document
	// is caught here rather than by whichever scenario happens to assert it.
	if doc.When.Threshold != 12.5 {
		t.Errorf("rendered threshold %v, want 12.5", doc.When.Threshold)
	}
	if doc.When.Op != string(OpGt) {
		t.Errorf("rendered op %q, want %q — the Op field is what decides the direction",
			doc.When.Op, OpGt)
	}
	if doc.Type != "threshold" {
		t.Errorf("rendered type %q, want \"threshold\"", doc.Type)
	}
	if doc.Name != "Test rule" || spec.Name != "Test rule" {
		t.Errorf("name did not survive: document %q, spec %q", doc.Name, spec.Name)
	}
	if doc.Severity != SeverityMajor {
		t.Errorf("rendered severity %q, want the AUTHORING form %q", doc.Severity, SeverityMajor)
	}
	if len(doc.Actions) != 1 || doc.Actions[0].Type != "raiseAlarm" {
		t.Fatalf("want exactly one raiseAlarm action, got %+v", doc.Actions)
	}
	if doc.Actions[0].RaiseAlarm.AlarmKey != "t-alarm" {
		t.Errorf("rendered alarm key %q, want %q", doc.Actions[0].RaiseAlarm.AlarmKey, "t-alarm")
	}
	if !spec.Enabled {
		t.Error("Enabled did not survive into the spec, so the rule would publish unchecked")
	}
	if spec.Token != "t-rule" {
		t.Errorf("spec token %q, want %q", spec.Token, "t-rule")
	}
}

// The authoring severity must be the LOWERCASE form wherever a rule is rendered. The
// uppercase spelling compiles nowhere: event-processing's rule compiler rejects it, so a
// scenario handed the wire constant by mistake fails to publish at all.
func TestThresholdAlarmRuleRendersTheAuthoringSeverityNotTheWireOne(t *testing.T) {
	if SeverityMajor == AlarmSeverityMajorWire {
		t.Fatal("the authoring and wire severities are the same string — one of them is wrong, " +
			"and either way something downstream fails silently")
	}
	doc := decodeThresholdRule(t, ThresholdAlarmRule{
		Metric: "m", Op: OpGt, Severity: SeverityMajor, AlarmKey: "a",
	}.DefinitionJSON())
	if doc.Severity == AlarmSeverityMajorWire {
		t.Errorf("the rendered document carries the WIRE severity %q; the rule compiler rejects "+
			"it and the profile would not publish", doc.Severity)
	}
}

// ---- Direction ----------------------------------------------------------------

// OpGt means "fire while the metric is ABOVE the threshold" and OpLt means "fire while it
// is BELOW it" — the direction test the type comment asked for by name, and the reason it
// is worth writing down is that BOTH renderings are valid documents. An inverted operator
// publishes cleanly, compiles, reports ACTIVE, and then alarms on the wrong half of the
// cycle: sitepulse's fuel rule would raise low-fuel on a FULL tank and go quiet on an empty
// one, and every layer between the manifest and the alarm table would report success.
//
// So the constants are checked against their literal wire spellings as well as against the
// document. Swapping the two constants' VALUES would leave a "does OpLt render OpLt" test
// passing while inverting every rule in the module.
func TestThresholdAlarmRuleOpGtFiresAboveTheThresholdAndOpLtFiresBelowIt(t *testing.T) {
	if OpGt != "gt" || OpLt != "lt" {
		t.Fatalf("the operator constants are %q/%q, want the rules.CompareOp literals \"gt\"/\"lt\"; "+
			"a rule spelling anything else fails to compile at publish", OpGt, OpLt)
	}

	// The metric running HIGH — an overheating sensor. "gt" is the ABOVE direction.
	above := decodeThresholdRule(t, ThresholdAlarmRule{
		Token: "over-temp", Metric: "temperature_c", Op: OpGt, Threshold: 30,
		Severity: SeverityMajor, AlarmKey: "over-temp",
	}.DefinitionJSON())
	if above.When.Op != "gt" {
		t.Errorf("Op OpGt rendered %q; a rule meant to fire ABOVE %v would fire below it instead",
			above.When.Op, above.When.Threshold)
	}

	// The metric running LOW — a depleting tank. "lt" is the BELOW direction, and this is
	// the case the field was added for.
	below := decodeThresholdRule(t, ThresholdAlarmRule{
		Token: "low-fuel", Metric: "fuel_pct", Op: OpLt, Threshold: 15,
		Severity: SeverityMajor, AlarmKey: "low-fuel",
	}.DefinitionJSON())
	if below.When.Op != "lt" {
		t.Errorf("Op OpLt rendered %q; a fuel rule meant to fire BELOW %v%% would instead alarm "+
			"on a full tank and stay silent on an empty one", below.When.Op, below.When.Threshold)
	}

	// And the two must not render the same thing. A builder that ignored the field
	// entirely would satisfy exactly one of the checks above, depending which way it was
	// hardcoded; this fails whichever way that is.
	if above.When.Op == below.When.Op {
		t.Errorf("both directions rendered %q, so the Op field is not reaching the document",
			above.When.Op)
	}
}

// An unset or unknown Op must fail LOUDLY. This is the whole reason the field is required:
// the operator was hardcoded precisely because a wrong direction "looks configured", and a
// field that quietly defaults to gt hands that failure back with an author who believes
// they set it. A scenario is compiled in, so the panic is a developer error every test run
// catches — not a runtime condition.
func TestThresholdAlarmRuleWithNoOpPanicsRatherThanDefaultingToGt(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   ThresholdOp
	}{
		{"unset", ""},
		{"unknown", "gte"},       // a plausible near-miss for the real "ge"
		{"wrong case", "GT"},     // the compiler rejects any non-lowercase spelling
		{"a CEL operator", ">"},  // what someone writing the predicate by hand reaches for
		{"a Go comparison", "<"}, // ...and its lt-direction twin
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("Op %q rendered a document instead of panicking; whatever direction "+
						"it silently chose, half the scenarios using it alarm on the wrong side "+
						"of their threshold", tc.op)
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, "op") {
					t.Errorf("panic value %v does not name the offending field, so an author sees "+
						"a stack trace and no cause", r)
				}
			}()
			_ = ThresholdAlarmRule{
				Token: "t-rule", Metric: "m", Op: tc.op, Threshold: 1,
				Severity: SeverityMajor, AlarmKey: "a",
			}.DefinitionJSON()
		})
	}
}

// ---- The action chain ----------------------------------------------------------

// BOTH halves, and the second is the one that matters: a builder that always appends a
// sendCommand would pass any test that only ever configured a command. An unwanted
// sendCommand is not inert — it enqueues a real command at every rising edge — and for a
// rule whose profile declares no command vocabulary it would fail Validate's cross-check
// on a manifest whose author never asked for a command at all.
func TestThresholdAlarmRuleRendersTheCommandActionOnlyWhenACommandIsConfigured(t *testing.T) {
	// With a command: raiseAlarm AND sendCommand, each under the key its own type names.
	both := decodeThresholdRule(t, ThresholdAlarmRule{
		Token: "low-fuel", Metric: "fuel_pct", Op: OpLt, Threshold: 15,
		Severity: SeverityMajor, AlarmKey: "low-fuel", CommandKey: "goto-refuel",
	}.DefinitionJSON())
	if len(both.Actions) != 2 {
		t.Fatalf("a rule with a command rendered %d actions, want 2 (raiseAlarm + sendCommand): %+v",
			len(both.Actions), both.Actions)
	}
	if both.Actions[0].Type != "raiseAlarm" || both.Actions[0].RaiseAlarm.AlarmKey != "low-fuel" {
		t.Errorf("first action is %+v, want a raiseAlarm keyed on %q", both.Actions[0], "low-fuel")
	}
	if both.Actions[1].Type != "sendCommand" {
		t.Errorf("second action type %q, want \"sendCommand\"", both.Actions[1].Type)
	}
	if both.Actions[1].SendCommand.Command != "goto-refuel" {
		t.Errorf("sendCommand names command %q, want %q — the platform reads this key and nothing "+
			"else, so an empty one dead-letters at the first firing",
			both.Actions[1].SendCommand.Command, "goto-refuel")
	}
	// No payload key at all — asserted on the key's PRESENCE, not on its value, because
	// nothing downstream would object to an empty one. The publish gate skips its
	// is-it-an-object test when the payload is "", so `"payload":""` publishes as happily
	// as an omitted key; this is the only place that difference is ever looked at.
	if p := both.Actions[1].SendCommand.Payload; p != nil {
		t.Errorf("sendCommand emitted a payload key (%q). Nothing renders one, and no gate "+
			"downstream would say so: the publish gate treats an empty payload as absent, so "+
			"this assertion is the only thing standing behind the claim", *p)
	}

	// Without one: exactly the single-action document every existing scenario publishes.
	alone := decodeThresholdRule(t, ThresholdAlarmRule{
		Token: "over-temp", Metric: "temperature_c", Op: OpGt, Threshold: 30,
		Severity: SeverityMajor, AlarmKey: "over-temp",
	}.DefinitionJSON())
	if len(alone.Actions) != 1 {
		t.Fatalf("a rule with no CommandKey rendered %d actions, want exactly 1: %+v",
			len(alone.Actions), alone.Actions)
	}
	if alone.Actions[0].Type != "raiseAlarm" {
		t.Errorf("the sole action is %q, want \"raiseAlarm\"", alone.Actions[0].Type)
	}
	if alone.Actions[0].SendCommand.Command != "" {
		t.Errorf("the raiseAlarm action carries a sendCommand payload %q",
			alone.Actions[0].SendCommand.Command)
	}
}
