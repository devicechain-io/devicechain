// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The four DETECT rule kinds no scenario authored before harborpulse: held-for-duration,
// rate-of-change, repeating occurrence and area correlation. They follow ThresholdAlarmRule's
// and AggregateAlarmRule's shape — a typed builder that panics on a definition no gate would
// accept, and a Spec — for the same reason those exist: the definition is opaque JSON this
// module cannot decode with the owning type, so the builder is where its shape is pinned, and
// the authored-rules fixture is where event-processing's REAL publish gate judges it.
const (
	ruleTypeDuration    = "duration"
	ruleTypeDeltaRate   = "deltaRate"
	ruleTypeRepeating   = "repeating"
	ruleTypeCorrelation = "correlation"
)

// structuredLeaf is a `when` comparison: `<metric> <op> <threshold>`.
func structuredLeaf(token, metric string, op ThresholdOp, threshold float64) map[string]any {
	if !validThresholdOps[op] {
		panic(fmt.Sprintf("sim: rule %q compares %q with op %q, want %q or %q — the direction "+
			"has no default", token, metric, op, OpGt, OpLt))
	}
	return map[string]any{"metric": metric, "op": string(op), "threshold": threshold}
}

func marshalRule(token string, doc map[string]any) string {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("sim: marshal rule %q: %v", token, err))
	}
	return string(raw)
}

// DurationAlarmRule raises an alarm when its condition stays true continuously for Hold.
//
// The condition is EITHER a structured comparison (Metric/Op/Threshold) OR raw CEL, never both:
// a held condition over two metrics of one event ("running AND drawing no current") has no
// structured form. With CEL, Metric still names the metric the rule is about, because the
// manifest's Validate checks that metric against the profile; a scenario test must check every
// other metric the expression reads (harborpulse's does).
type DurationAlarmRule struct {
	Token string
	Name  string

	Metric    string
	Op        ThresholdOp
	Threshold float64
	// CEL, when set, replaces the structured comparison.
	CEL string

	Hold     time.Duration
	Severity string
	AlarmKey string
	Enabled  bool
}

// DefinitionJSON is the rules.Rule document.
func (r DurationAlarmRule) DefinitionJSON() string {
	if r.Hold <= 0 {
		panic(fmt.Sprintf("sim: duration rule %q has hold %s; it needs a positive span to hold for", r.Token, r.Hold))
	}
	var when map[string]any
	if r.CEL != "" {
		if r.Op != "" || r.Threshold != 0 {
			panic(fmt.Sprintf("sim: duration rule %q has both CEL and a structured comparison; the gate refuses both", r.Token))
		}
		when = map[string]any{"cel": r.CEL}
	} else {
		when = structuredLeaf(r.Token, r.Metric, r.Op, r.Threshold)
	}
	return marshalRule(r.Token, map[string]any{
		"name":     r.Name,
		"type":     ruleTypeDuration,
		"severity": r.Severity,
		"hold":     r.Hold.String(),
		"when":     when,
		"actions":  ruleActions(r.Token, r.AlarmKey, "", "", nil),
	})
}

// Spec is the manifest form.
func (r DurationAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{Token: r.Token, Name: r.Name, Definition: r.DefinitionJSON(), Metric: r.Metric, Enabled: r.Enabled}
}

// DeltaRateAlarmRule raises an alarm when the change of Metric between a device's consecutive
// samples, divided by the seconds between them, satisfies Op against Threshold: a level rising
// faster than Threshold units per second.
type DeltaRateAlarmRule struct {
	Token     string
	Name      string
	Metric    string
	Op        ThresholdOp
	Threshold float64
	Severity  string
	AlarmKey  string
	Enabled   bool
}

// DefinitionJSON is the rules.Rule document. It always sets rate: a raw delta between samples
// would make the threshold depend on the emit cadence, which an operator may change.
func (r DeltaRateAlarmRule) DefinitionJSON() string {
	if !validThresholdOps[r.Op] {
		panic(fmt.Sprintf("sim: deltaRate rule %q has op %q, want %q or %q", r.Token, r.Op, OpGt, OpLt))
	}
	return marshalRule(r.Token, map[string]any{
		"name":      r.Name,
		"type":      ruleTypeDeltaRate,
		"severity":  r.Severity,
		"metric":    r.Metric,
		"op":        string(r.Op),
		"threshold": r.Threshold,
		"rate":      true,
		"when":      map[string]any{},
		"actions":   ruleActions(r.Token, r.AlarmKey, "", "", nil),
	})
}

// Spec is the manifest form.
func (r DeltaRateAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{Token: r.Token, Name: r.Name, Definition: r.DefinitionJSON(), Metric: r.Metric, Enabled: r.Enabled}
}

// RepeatingAlarmRule raises an alarm when Count events matching its comparison fall within a
// sliding Window: an intermittent fault, which neither a threshold (it clears between pulses)
// nor a duration (it never holds) can see.
type RepeatingAlarmRule struct {
	Token     string
	Name      string
	Metric    string
	Op        ThresholdOp
	Threshold float64
	Count     int
	Window    time.Duration
	Severity  string
	AlarmKey  string
	Enabled   bool
}

// DefinitionJSON is the rules.Rule document.
func (r RepeatingAlarmRule) DefinitionJSON() string {
	if r.Count < 1 || r.Window <= 0 {
		panic(fmt.Sprintf("sim: repeating rule %q needs a positive count and window, got %d in %s", r.Token, r.Count, r.Window))
	}
	return marshalRule(r.Token, map[string]any{
		"name":     r.Name,
		"type":     ruleTypeRepeating,
		"severity": r.Severity,
		"count":    r.Count,
		"window":   r.Window.String(),
		"when":     structuredLeaf(r.Token, r.Metric, r.Op, r.Threshold),
		"actions":  ruleActions(r.Token, r.AlarmKey, "", "", nil),
	})
}

// Spec is the manifest form.
func (r RepeatingAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{Token: r.Token, Name: r.Name, Definition: r.DefinitionJSON(), Metric: r.Metric, Enabled: r.Enabled}
}

// CorrelationSignalRule fires when Count DISTINCT devices anchored to one entity of AnchorType
// (an area) match its comparison within Window.
//
// 🔴 IT IS A SIGNAL, NOT AN ALARM, AND THAT IS THE PLATFORM'S RULE, NOT THIS BUILDER'S CHOICE. A
// correlation's series is the anchor, not a device, and every REACT action targets a device, so
// the publish gate refuses any action on it. It therefore carries no severity and no action,
// raises nothing, and is observed through a replay preview of the rule rather than through an
// alarm. A builder that let a caller attach an alarm would produce a rule the gate refuses.
type CorrelationSignalRule struct {
	Token      string
	Name       string
	AnchorType string
	Metric     string
	Op         ThresholdOp
	Threshold  float64
	Count      int
	Window     time.Duration
	Enabled    bool
}

// DefinitionJSON is the rules.Rule document.
func (r CorrelationSignalRule) DefinitionJSON() string {
	if r.AnchorType == "" {
		panic(fmt.Sprintf("sim: correlation rule %q names no anchor type", r.Token))
	}
	if r.Count < 1 || r.Window <= 0 {
		panic(fmt.Sprintf("sim: correlation rule %q needs a positive distinct count and window, got %d in %s", r.Token, r.Count, r.Window))
	}
	return marshalRule(r.Token, map[string]any{
		"name":       r.Name,
		"type":       ruleTypeCorrelation,
		"anchorType": r.AnchorType,
		"count":      r.Count,
		"window":     r.Window.String(),
		"when":       structuredLeaf(r.Token, r.Metric, r.Op, r.Threshold),
	})
}

// Spec is the manifest form.
func (r CorrelationSignalRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{Token: r.Token, Name: r.Name, Definition: r.DefinitionJSON(), Metric: r.Metric, Enabled: r.Enabled}
}

// FenceAlarmRule raises an alarm while a device's reported position is inside a geofence, and
// clears it when a later position is outside: a threshold rule whose condition is the platform's
// one containment function, `geo.inFence("<fence token>")`.
//
// It reads NO measurement, and must not: a position arrives on a location event, which carries
// none, and a measurement on a measurement event, which carries no position, so a condition
// reading both could never be true and the publish gate refuses it. Its Spec therefore declares
// ReadsNoMetric rather than a Metric.
type FenceAlarmRule struct {
	Token    string
	Name     string
	Fence    string
	Severity string
	AlarmKey string
	Enabled  bool
}

// DefinitionJSON is the rules.Rule document.
func (r FenceAlarmRule) DefinitionJSON() string {
	if r.Fence == "" || strings.ContainsAny(r.Fence, `"\`) {
		panic(fmt.Sprintf("sim: fence rule %q names fence %q, which cannot be written into its condition", r.Token, r.Fence))
	}
	return marshalRule(r.Token, map[string]any{
		"name":     r.Name,
		"type":     "threshold",
		"severity": r.Severity,
		"when":     map[string]any{"cel": `geo.inFence("` + r.Fence + `")`},
		"actions":  ruleActions(r.Token, r.AlarmKey, "", "", nil),
	})
}

// Spec is the manifest form: a rule that reads no metric, and says so.
func (r FenceAlarmRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{Token: r.Token, Name: r.Name, Definition: r.DefinitionJSON(), Enabled: r.Enabled, ReadsNoMetric: true}
}

// DeliveryRule is a threshold rule whose only action delivers OUT of the tenant: a signed webhook
// (httpCall) or a publish through an outbound connector. It raises no alarm. Both are one-shot
// effects, dispatched on the rising edge only, like sendCommand.
//
// The body is a CEL template over the detection naming its series (the device token), so the
// receiver can tell which device fired: without one the platform sends an empty body.
type DeliveryRule struct {
	Token     string
	Name      string
	Metric    string
	Op        ThresholdOp
	Threshold float64
	// Exactly one of the two destinations.
	Webhook *WebhookDelivery
	Publish *PublishDelivery
	Enabled bool
}

// WebhookDelivery is an httpCall action. SigningHandle, when set, signs every delivery with the
// tenant's secret under that handle (Standard Webhooks headers).
type WebhookDelivery struct {
	URL           string
	SigningHandle string
}

// PublishDelivery is a publish action through the connector with this token.
type PublishDelivery struct {
	Connector string
}

// DeliveryBodyTemplate is the body every DeliveryRule sends: {"device": <series>}. It names only the
// device because the gate prices a template's worst case and refuses one over its ceiling, and
// the value too was over it.
const DeliveryBodyTemplate = `'{"device":"' + series + '"}'`

// DefinitionJSON is the rules.Rule document.
func (r DeliveryRule) DefinitionJSON() string {
	var action map[string]any
	switch {
	case r.Webhook != nil && r.Publish == nil:
		call := map[string]any{"url": r.Webhook.URL, "bodyTemplate": DeliveryBodyTemplate}
		if r.Webhook.SigningHandle != "" {
			call["signingSecretRef"] = r.Webhook.SigningHandle
		}
		action = map[string]any{"type": actionTypeHTTPCall, actionTypeHTTPCall: call}
	case r.Publish != nil && r.Webhook == nil:
		action = map[string]any{"type": actionTypePublish, actionTypePublish: map[string]any{
			"connectorRef": r.Publish.Connector, "payloadTemplate": DeliveryBodyTemplate,
		}}
	default:
		panic(fmt.Sprintf("sim: delivery rule %q needs exactly one of a webhook and a publish", r.Token))
	}
	return marshalRule(r.Token, map[string]any{
		"name":    r.Name,
		"type":    "threshold",
		"when":    structuredLeaf(r.Token, r.Metric, r.Op, r.Threshold),
		"actions": []any{action},
	})
}

// Spec is the manifest form.
func (r DeliveryRule) Spec() DetectionRuleSpec {
	return DetectionRuleSpec{Token: r.Token, Name: r.Name, Definition: r.DefinitionJSON(), Metric: r.Metric, Enabled: r.Enabled}
}
