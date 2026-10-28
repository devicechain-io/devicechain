// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"time"
)

// ruleTypeAggregate / ruleTypeAbsence are event-processing's rules.RuleType discriminators,
// mirrored by hand like every other piece of wire vocabulary in this module (see
// detectionrule.go for why the sim cannot import the owning type).
const (
	ruleTypeAggregate = "aggregate"
	ruleTypeAbsence   = "absence"

	windowModeSliding = "sliding"
	aggAvg            = "avg"
)

// ruleActions renders the REACT chain every builder in this file shares: the alarm, then
// the optional command. The shapes are exactly ThresholdAlarmRule.DefinitionJSON's, so a
// rule with a command renders the same two actions whichever predicate it carries.
//
// payload is the command's static argument object, rendered as the JSON-object-in-a-string
// the platform stores. It is emitted ONLY when there is an object to put in it: an empty
// string is accepted by the publish gate as "absent", so rendering it unconditionally would
// publish a `"payload":""` that looks configured and carries nothing (see
// ThresholdAlarmRule.CommandKey).
func ruleActions(token, alarmKey, commandKey, expiresAfter string, payload map[string]any) []any {
	actions := []any{
		map[string]any{
			"type":               actionTypeRaiseAlarm,
			actionTypeRaiseAlarm: map[string]any{"alarmKey": alarmKey},
		},
	}
	if commandKey == "" {
		return actions
	}
	send := map[string]any{"command": commandKey}
	if expiresAfter != "" {
		send["expiresAfter"] = expiresAfter
	}
	if len(payload) > 0 {
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(fmt.Sprintf("sim: marshal the command payload of rule %q: %v", token, err))
		}
		send["payload"] = string(raw)
	}
	return append(actions, map[string]any{
		"type":                actionTypeSendCommand,
		actionTypeSendCommand: send,
	})
}

// AggregateAlarmRule is "alarm when the sliding AVERAGE of this metric over a window crosses
// a number", optionally only over the events that pass a gate on another metric: a zone is
// hot when its five-minute average temperature is above 26 C while the room is occupied.
//
// It exists beside ThresholdAlarmRule because the two differ in what they are robust to. A
// threshold rule fires on one sample, so a door opening or a sensor spike is an alarm; an
// average over a window needs the condition to HOLD. It carries a command like its sibling
// does (the REACT chain is independent of the detection kind), and unlike the sibling it
// carries the command's arguments, because a boost needs a duration.
//
// The Op is required with no default, for the reason ThresholdAlarmRule gives.
type AggregateAlarmRule struct {
	Token string
	Name  string
	// Metric is the measurement whose average is folded.
	Metric    string
	Op        ThresholdOp
	Threshold float64
	// Window is the trailing span the average covers.
	Window time.Duration
	// Gate, when set, admits only the events whose GateMetric compares true against
	// GateThreshold; an event that fails it does not enter the window at all. The gate reads
	// the SAME event as the folded metric, so a device must report both in one message.
	GateMetric    string
	GateOp        ThresholdOp
	GateThreshold float64

	Severity string
	AlarmKey string
	// CommandKey, CommandPayload and CommandExpiresAfter chain a sendCommand after the
	// alarm; see ThresholdAlarmRule.CommandKey for why it is the profile command's KEY.
	CommandKey          string
	CommandPayload      map[string]any
	CommandExpiresAfter string
	Enabled             bool
}

// DefinitionJSON renders the opaque rules.Rule document.
func (r AggregateAlarmRule) DefinitionJSON() string {
	if !validThresholdOps[r.Op] {
		panic(fmt.Sprintf("sim: aggregate rule %q has op %q, want %q or %q — the direction is required "+
			"and has no default", r.Token, r.Op, OpGt, OpLt))
	}
	if r.Window <= 0 {
		panic(fmt.Sprintf("sim: aggregate rule %q has window %s; a sliding average needs a positive span", r.Token, r.Window))
	}
	doc := map[string]any{
		"name":       r.Name,
		"type":       ruleTypeAggregate,
		"severity":   r.Severity,
		"windowMode": windowModeSliding,
		"agg":        aggAvg,
		"metric":     r.Metric,
		"op":         string(r.Op),
		"threshold":  r.Threshold,
		"window":     r.Window.String(),
		"actions":    ruleActions(r.Token, r.AlarmKey, r.CommandKey, r.CommandExpiresAfter, r.CommandPayload),
	}
	if r.GateMetric != "" {
		if !validThresholdOps[r.GateOp] {
			panic(fmt.Sprintf("sim: aggregate rule %q gates on %q with op %q, want %q or %q",
				r.Token, r.GateMetric, r.GateOp, OpGt, OpLt))
		}
		doc["when"] = map[string]any{
			"metric":    r.GateMetric,
			"op":        string(r.GateOp),
			"threshold": r.GateThreshold,
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("sim: marshal aggregate rule %q: %v", r.Token, err))
	}
	return string(raw)
}

// Spec renders the manifest entry. The declared Metric is the folded one; Validate also
// checks the gate's metric against the profile (ruleReadMetrics).
func (r AggregateAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{
		Token:      r.Token,
		Name:       r.Name,
		Definition: r.DefinitionJSON(),
		Metric:     r.Metric,
		Enabled:    r.Enabled,
	}
}

// AbsenceAlarmRule is "alarm when a device sends nothing for this long": the data-silence
// fallback to a presence rule, and the only kind that notices a device that stopped without
// saying goodbye. Every event is a heartbeat (the engine ignores any condition on an absence
// rule), so it reads no metric and the manifest's Metric field names the one a heartbeat is
// expected to carry, which Validate holds to the profile.
type AbsenceAlarmRule struct {
	Token string
	Name  string
	// Metric is a measurement the device reports, declared so Validate can prove the profile
	// has something to send a heartbeat with. It does not appear in the document.
	Metric   string
	Timeout  time.Duration
	Severity string
	AlarmKey string
	Enabled  bool
}

// DefinitionJSON renders the opaque rules.Rule document.
func (r AbsenceAlarmRule) DefinitionJSON() string {
	if r.Timeout <= 0 {
		panic(fmt.Sprintf("sim: absence rule %q has timeout %s; it needs a positive silence to wait for", r.Token, r.Timeout))
	}
	raw, err := json.Marshal(map[string]any{
		"name":     r.Name,
		"type":     ruleTypeAbsence,
		"severity": r.Severity,
		"timeout":  r.Timeout.String(),
		"when":     map[string]any{},
		"actions":  ruleActions(r.Token, r.AlarmKey, "", "", nil),
	})
	if err != nil {
		panic(fmt.Sprintf("sim: marshal absence rule %q: %v", r.Token, err))
	}
	return string(raw)
}

// Spec renders the manifest entry.
func (r AbsenceAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{
		Token:      r.Token,
		Name:       r.Name,
		Definition: r.DefinitionJSON(),
		Metric:     r.Metric,
		Enabled:    r.Enabled,
	}
}
