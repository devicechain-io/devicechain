// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
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
	if doc.When.Op != "gt" {
		t.Errorf("rendered op %q, want \"gt\" — a scenario alarms on a value running HIGH", doc.When.Op)
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
		Metric: "m", Severity: SeverityMajor, AlarmKey: "a",
	}.DefinitionJSON())
	if doc.Severity == AlarmSeverityMajorWire {
		t.Errorf("the rendered document carries the WIRE severity %q; the rule compiler rejects "+
			"it and the profile would not publish", doc.Severity)
	}
}
