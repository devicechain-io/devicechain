// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
)

// ThresholdAlarmRule is a scenario's "alarm when this metric goes above that number"
// DETECT rule, declared as fields instead of hand-assembled JSON.
//
// 🔴 WHY THIS EXISTS AT ALL, GIVEN THE SIM MAY NOT IMPORT event-processing. The rule
// document is event-processing's rules.Rule, which decodes with DisallowUnknownFields —
// so a misspelled key is rejected at publish, and a rule that never compiles leaves the
// scenario's alarm widgets permanently empty. dc-simulator is deliberately an untrusted
// external client and cannot import that type, so the wire shape has to be written by
// hand SOMEWHERE. It does not have to be written by hand once per scenario. Every
// scenario that grew one of these copied the same map literal, the same json tags and
// the same panic; by the time buildingpulse gained a rule there were four copies of the
// grammar in this module, and a tag change meant four edits.
//
// Sharing the BUILDER is safe in a way sharing the CONSTANTS would not be. The
// authored-rules fixture's oracles all read the produced DOCUMENT — the tests decode
// this builder's output and hold it to each scenario's own constants, and
// event-processing runs that output through its real compiler — so a wrong shape from a
// shared builder still fails both. What must stay per-scenario is the curve, the
// threshold, the tokens: those are independent design decisions that happen to agree
// today (widgetlab's 15-35 triangle and buildingpulse's 16-32 sine both alarm at 30 by
// coincidence, not by a shared choice).
//
// The op is fixed at "gt" rather than being a field: every scenario alarms on a value
// running HIGH, and an inverted operator is the kind of thing that looks configured and
// silently alarms on the wrong half of the cycle. A scenario that genuinely needs "lt"
// should add the field then, with a test that says which direction it means.
type ThresholdAlarmRule struct {
	// Token is the rule's stable authoring id. It survives every definition change and
	// is what ruleHealth reports, so it must not encode anything about the predicate.
	Token string
	// Name is the human label shown in the console.
	Name string
	// Metric is the measurement key the predicate reads. It lands in BOTH the rule
	// document and the DetectionRuleSpec — see Spec, which is the whole point of this
	// type.
	Metric string
	// Threshold is the value the metric must EXCEED. Strictly: "gt" is exclusive, so a
	// sample landing exactly here does not fire.
	Threshold float64
	// Severity is the rule's AUTHORING severity, which is lowercase — see SeverityMajor
	// in wirevocabulary.go. The uppercase form is a different constant and belongs
	// nowhere near this field.
	Severity string
	// AlarmKey is what the raiseAlarm action keys its durable alarm on, per originator.
	AlarmKey string
	// Enabled gates the rule at publish AND at load. A disabled rule is inert by design
	// (the publish gate does not submit it and the liveness check does not require it),
	// which also means it is published UNCHECKED — so a scenario parking one is
	// deliberately giving up the compiler's opinion of it.
	Enabled bool
}

// DefinitionJSON renders the opaque rules.Rule document the platform stores. The keys
// are exactly the rules.Rule / Condition / Action json tags.
func (r ThresholdAlarmRule) DefinitionJSON() string {
	raw, err := json.Marshal(map[string]any{
		"name":     r.Name,
		"type":     "threshold",
		"severity": r.Severity,
		"when": map[string]any{
			"metric":    r.Metric,
			"op":        "gt",
			"threshold": r.Threshold,
		},
		"actions": []any{
			map[string]any{
				"type":       "raiseAlarm",
				"raiseAlarm": map[string]any{"alarmKey": r.AlarmKey},
			},
		},
	})
	if err != nil {
		// Marshaling a static map of strings and floats cannot fail; a failure here is a
		// programming error, not a runtime condition.
		panic(fmt.Sprintf("sim: marshal threshold rule %q: %v", r.Token, err))
	}
	return string(raw)
}

// Spec renders the manifest entry.
//
// 🔑 THIS IS WHY THE TYPE EARNS ITS KEEP: DetectionRuleSpec.Metric and the document's
// own `when.metric` are two statements of one fact, and they used to be written
// separately at every call site. Manifest.Validate cross-checks them precisely because
// they could disagree — a rule DECLARING temperature while READING humidity passes every
// JSON check and never fires on the curve the scenario drives. Filling both from one
// field closes that seam by construction for anything built this way; Validate stays as
// the gate for rules built any other way.
func (r ThresholdAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{
		Token:      r.Token,
		Name:       r.Name,
		Definition: r.DefinitionJSON(),
		Metric:     r.Metric,
		Enabled:    r.Enabled,
	}
}
