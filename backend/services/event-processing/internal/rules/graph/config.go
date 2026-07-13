// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"fmt"
	"math"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// maxDurationMs is the largest millisecond count that fits a time.Duration (int64 ns)
// without overflow. A larger value would wrap to a small — even positive — nanosecond
// count, sneaking past the compiler's `<= 0` guards as a valid-looking but wrong duration,
// so ms() rejects it (fail-closed) rather than silently mis-lowering.
const maxDurationMs = int64(math.MaxInt64) / int64(time.Millisecond)

// The canvas config vocabulary is DELIBERATELY the rules schema's own vocabulary — the same
// operator tokens ("gt"/"ge"/…), aggregate names, and window-mode names rules.Rule uses —
// not a second symbol set. A canvas node is a projection of a rules.Rule field group, so
// sharing one vocabulary keeps the graph→schema mapping a rename-free copy and makes the
// byte-identity contract (§3.2) hold without a translation table to drift. The frontend
// renders friendly symbols; the wire form is rules-native. Durations cross the wire as
// integer milliseconds (JS-friendly) and lower to rules.Duration.

// ruleMeta is the rule-level identity a condition node carries: one condition node IS one
// rule, so its name/description/severity are the rule's. Severity is rule-level (one tier
// per rule — an alarm and its rule never disagree), so it lives here, NOT on an action node.
type ruleMeta struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Severity    string `json:"severity,omitempty"`
}

func (m ruleMeta) applyTo(r *rules.Rule) {
	r.Name = m.Name
	r.Description = m.Description
	r.Severity = rules.Severity(m.Severity)
}

// bound is a comparison's right-hand side: either a literal numeric threshold or a
// device-attribute reference (a dynamic, per-device threshold, ADR-051 slice 4c-3b). The
// two are mutually exclusive; the kind discriminates.
type bound struct {
	Kind      string   `json:"kind"` // "literal" | "attribute"
	Value     *float64 `json:"value,omitempty"`
	Attribute string   `json:"attribute,omitempty"`
}

// leaf is a canvas predicate leaf, projecting onto rules.Condition. Exactly one of a
// structured comparison, a raw-CEL string, or nothing (match-every) is populated; the
// bound picks literal vs attribute. Validation of coherence is delegated to rules.Compile
// (the single authority), so this only maps fields.
type leaf struct {
	Metric    string `json:"metric,omitempty"`
	Op        string `json:"op,omitempty"`
	Threshold *bound `json:"threshold,omitempty"`
	CEL       string `json:"cel,omitempty"`
}

// toCondition maps the leaf onto a rules.Condition. It resolves only the literal/attribute
// discriminator locally (a malformed bound kind is a shape error the compiler cannot see);
// every other coherence check — structured-vs-raw exclusivity, metric grammar, operator
// validity — is left to rules.Compile so the canvas and the form fail identically.
func (l leaf) toCondition() (rules.Condition, error) {
	c := rules.Condition{Metric: l.Metric, Op: rules.CompareOp(l.Op), CEL: l.CEL}
	if l.Threshold != nil {
		// Reject an incoherent bound (both a literal value AND an attribute set) rather than
		// silently keeping the kind-selected one — the lowering would otherwise turn a config
		// the author botched into a valid rule that may invert their intent (they meant the
		// per-device attribute; they'd get the stale literal). Fail closed, like the compiler.
		hasLit, hasAttr := l.Threshold.Value != nil, l.Threshold.Attribute != ""
		switch l.Threshold.Kind {
		case "literal":
			if hasAttr {
				return rules.Condition{}, fmt.Errorf("a literal threshold bound must not also set an attribute")
			}
			c.Threshold = l.Threshold.Value
		case "attribute":
			if hasLit {
				return rules.Condition{}, fmt.Errorf("an attribute threshold bound must not also set a value")
			}
			c.ThresholdAttr = l.Threshold.Attribute
		default:
			return rules.Condition{}, fmt.Errorf("threshold bound kind must be \"literal\" or \"attribute\", got %q", l.Threshold.Kind)
		}
	}
	return c, nil
}

// ms converts a millisecond count from the wire to a rules.Duration, rejecting a negative
// or overflowing value (which would wrap to a small positive nanosecond count and slip past
// the compiler's `<= 0` guards). field names the offending config field for the error.
func ms(field string, v int64) (rules.Duration, error) {
	if v < 0 {
		return 0, fmt.Errorf("%s must not be negative", field)
	}
	if v > maxDurationMs {
		return 0, fmt.Errorf("%s is too large", field)
	}
	return rules.Duration(time.Duration(v) * time.Millisecond), nil
}

// --- per-condition-node config structs. Each embeds ruleMeta and carries only the rules
//     fields that node type uses; DisallowUnknownFields rejects a stray field at decode,
//     and rules.Compile's forbid() is the authoritative backstop. Each builds a partial
//     rules.Rule (type + fields); id/actions are attached by the lowering. ---

type thresholdConfig struct {
	ruleMeta
	When leaf `json:"when"`
}

func (c thresholdConfig) build() (rules.Rule, error) {
	when, err := c.When.toCondition()
	if err != nil {
		return rules.Rule{}, err
	}
	r := rules.Rule{Type: rules.TypeThreshold, When: when}
	c.applyTo(&r)
	return r, nil
}

type durationConfig struct {
	ruleMeta
	When   leaf  `json:"when"`
	HoldMs int64 `json:"holdMs"`
}

func (c durationConfig) build() (rules.Rule, error) {
	when, err := c.When.toCondition()
	if err != nil {
		return rules.Rule{}, err
	}
	hold, err := ms("holdMs", c.HoldMs)
	if err != nil {
		return rules.Rule{}, err
	}
	r := rules.Rule{Type: rules.TypeDuration, When: when, Hold: hold}
	c.applyTo(&r)
	return r, nil
}

type absenceConfig struct {
	ruleMeta
	TimeoutMs int64 `json:"timeoutMs"`
}

func (c absenceConfig) build() (rules.Rule, error) {
	ttl, err := ms("timeoutMs", c.TimeoutMs)
	if err != nil {
		return rules.Rule{}, err
	}
	r := rules.Rule{Type: rules.TypeAbsence, Ttl: ttl}
	c.applyTo(&r)
	return r, nil
}

type aggregateConfig struct {
	ruleMeta
	Agg        string   `json:"agg"`
	WindowMode string   `json:"windowMode"`
	Metric     string   `json:"metric,omitempty"`
	WindowMs   int64    `json:"windowMs,omitempty"`
	GapMs      int64    `json:"gapMs,omitempty"`
	Count      int      `json:"count,omitempty"`
	Op         string   `json:"op"`
	Threshold  *float64 `json:"threshold"`
	When       leaf     `json:"when,omitempty"`
}

func (c aggregateConfig) build() (rules.Rule, error) {
	when, err := c.When.toCondition()
	if err != nil {
		return rules.Rule{}, err
	}
	r := rules.Rule{
		Type:      rules.TypeAggregate,
		Agg:       rules.AggFunc(c.Agg),
		Mode:      rules.WindowMode(c.WindowMode),
		Metric:    c.Metric,
		Count:     c.Count,
		Op:        rules.CompareOp(c.Op),
		Threshold: c.Threshold,
		When:      when,
	}
	// Only set the temporal durations a given mode uses so a zero ms does not surface as a
	// forbidden non-zero field to the compiler (window/gap are mode-specific).
	if c.WindowMs != 0 {
		if r.Window, err = ms("windowMs", c.WindowMs); err != nil {
			return rules.Rule{}, err
		}
	}
	if c.GapMs != 0 {
		if r.Gap, err = ms("gapMs", c.GapMs); err != nil {
			return rules.Rule{}, err
		}
	}
	c.applyTo(&r)
	return r, nil
}

type deltaRateConfig struct {
	ruleMeta
	Metric    string   `json:"metric"`
	Rate      bool     `json:"rate,omitempty"`
	Op        string   `json:"op"`
	Threshold *float64 `json:"threshold"`
	When      leaf     `json:"when,omitempty"`
}

func (c deltaRateConfig) build() (rules.Rule, error) {
	when, err := c.When.toCondition()
	if err != nil {
		return rules.Rule{}, err
	}
	// NB: deltaRate takes NO window — compileDeltaRate forbids it (the slice-9 spec table was
	// wrong on this point; the compiler is the authority). The change is per consecutive
	// matching sample, optionally per-second (Rate).
	r := rules.Rule{
		Type:      rules.TypeDeltaRate,
		Metric:    c.Metric,
		Rate:      c.Rate,
		Op:        rules.CompareOp(c.Op),
		Threshold: c.Threshold,
		When:      when,
	}
	c.applyTo(&r)
	return r, nil
}

type repeatingConfig struct {
	ruleMeta
	When     leaf  `json:"when,omitempty"`
	Count    int   `json:"count"`
	WindowMs int64 `json:"windowMs"`
}

func (c repeatingConfig) build() (rules.Rule, error) {
	when, err := c.When.toCondition()
	if err != nil {
		return rules.Rule{}, err
	}
	window, err := ms("windowMs", c.WindowMs)
	if err != nil {
		return rules.Rule{}, err
	}
	r := rules.Rule{Type: rules.TypeRepeating, When: when, Count: c.Count, Window: window}
	c.applyTo(&r)
	return r, nil
}

type correlationConfig struct {
	ruleMeta
	AnchorType string `json:"anchorType"`
	Count      int    `json:"count"`
	WindowMs   int64  `json:"windowMs"`
	MemberCap  int    `json:"memberCap,omitempty"`
	When       leaf   `json:"when,omitempty"`
}

func (c correlationConfig) build() (rules.Rule, error) {
	when, err := c.When.toCondition()
	if err != nil {
		return rules.Rule{}, err
	}
	window, err := ms("windowMs", c.WindowMs)
	if err != nil {
		return rules.Rule{}, err
	}
	r := rules.Rule{
		Type:       rules.TypeCorrelation,
		AnchorType: c.AnchorType,
		Count:      c.Count,
		Window:     window,
		MemberCap:  c.MemberCap,
		When:       when,
	}
	c.applyTo(&r)
	return r, nil
}

// buildRule decodes a condition node's config against its type struct and produces the
// partial rules.Rule (type + fields + meta). The lowering attaches the id and the REACT
// actions and runs it through rules.Compile.
func buildRule(n Node) (rules.Rule, error) {
	switch n.Type {
	case NodeThreshold:
		var c thresholdConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	case NodeDuration:
		var c durationConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	case NodeAbsence:
		var c absenceConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	case NodeAggregate:
		var c aggregateConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	case NodeDeltaRate:
		var c deltaRateConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	case NodeRepeating:
		var c repeatingConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	case NodeCorrelation:
		var c correlationConfig
		if err := decodeConfig(n.Config, &c); err != nil {
			return rules.Rule{}, err
		}
		return c.build()
	default:
		return rules.Rule{}, fmt.Errorf("node type %q is not a condition node", n.Type)
	}
}

// actionConfig is a REACT Action node, projecting onto rules.Action. Type picks the
// variant; the rule's severity (from the condition) is the alarm tier, so no severity here.
type actionConfig struct {
	Action   string `json:"action"` // "raiseAlarm" | "sendCommand"
	AlarmKey string `json:"alarmKey,omitempty"`
	Command  string `json:"command,omitempty"`
	Payload  string `json:"payload,omitempty"`
}

// buildAction decodes an action node and maps it to a rules.Action. Field coherence (a
// raiseAlarm carrying a command, an alarm without a rule severity, a non-object payload) is
// left to rules.validateReact inside rules.Compile, so a canvas action and a form action are
// rejected identically.
func buildAction(n Node) (rules.Action, error) {
	var c actionConfig
	if err := decodeConfig(n.Config, &c); err != nil {
		return rules.Action{}, err
	}
	switch rules.ActionType(c.Action) {
	case rules.ActionRaiseAlarm:
		return rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: c.AlarmKey}}, nil
	case rules.ActionSendCommand:
		return rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: c.Command, Payload: c.Payload}}, nil
	default:
		return rules.Action{}, fmt.Errorf("action node has unknown action %q (want raiseAlarm or sendCommand)", c.Action)
	}
}

// branchConfig is a REACT branch node (slice 9c): a signal→signal router carrying one CEL boolean
// (When) that gates the actions downstream of it. Name is authoring-only (a human label for the
// route); it never reaches the compiled rule. When is a guard-env CEL expression (rules guard
// vocabulary: value / hasValue / series) — the lowering folds it onto the Guard of every action
// reachable through this branch, and rules.Compile cost-gates the composed guard. An empty When is
// a meaningless branch (it would gate nothing), rejected up front like a poisoned source.
type branchConfig struct {
	Name string `json:"name,omitempty"`
	When string `json:"when"`
}

// computeConfig is a compute node (slice 9a-2): a NAMED CEL value expression the compiler folds into
// a consumer's raw-CEL leaf or branch guard. Name is the CEL identifier the consumer references it by
// (a simple identifier — letters/digits/underscore, not leading-digit — validated at compile so it is
// a safe cel.bind variable and cannot shadow an env var); Expr is a CEL expression over the CONSUMER's
// env (the predicate env when it feeds a condition leaf, the guard env when it feeds a branch — the
// port typing decides which). Neither is spliced as raw text: the fold is a cel.bind binding whose
// composed result is re-compiled through the same cost gate.
type computeConfig struct {
	Name string `json:"name"`
	Expr string `json:"expr"`
}

// sourceConfig is the Source node: it sets the rule's SCOPE (the profile the canvas authors
// against), not any rules.Rule field — a source contributes nothing to the compiled rule
// bytes, which is what lets a canvas rule stay byte-identical to a form rule (a form rule
// has no source-scope concept; its scope is the profile it is homed on). metricFilter is an
// authoring hint (the runtime scopes the feed from the compiled predicate), not a rule field.
type sourceConfig struct {
	Scope        sourceScope `json:"scope"`
	MetricFilter []string    `json:"metricFilter,omitempty"`
}

type sourceScope struct {
	Kind         string `json:"kind"` // "profile" (GA); "derivedSubject" reserved (§4.2, deferred)
	ProfileToken string `json:"profileToken,omitempty"`
}
