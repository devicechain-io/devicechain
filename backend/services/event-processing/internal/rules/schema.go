// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package rules is DETECT's user-facing rule model and its lowering (ADR-051 slice 3).
//
// The Rule type here is the load-bearing authoring contract: the console form builder
// (slice 7) and the visual automation canvas (slice 9, ADR-053) both emit exactly this
// structured schema, and Compile lowers it to a (CEL predicate + keyed-streaming core
// config) pair the DETECT engine runs. Because both authoring surfaces target the same
// schema, a form-authored rule and a canvas-authored rule that express the same logic
// serialize byte-identically (after canonical marshalling) — the schema, not the engine,
// is the contract the UI is bolted to.
//
// No raw SQL and no raw expressions by default: the common path is a structured
// comparison the compiler renders into injection-safe CEL (see cel_gen.go); a raw-CEL
// leaf is an advanced, still-cost-gated escape hatch.
package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Decode parses a rule from its JSON representation, failing closed on any unknown field
// (per the project's "reject unknown/invalid keys" convention). A plain json.Unmarshal
// would silently drop a mistyped key — e.g. a `when_` typo would discard the author's gate
// and leave a rule that over-fires — so every consumer that reads an authored rule from the
// wire or from storage (slices 4/7/9) must decode through here, not through json.Unmarshal.
func Decode(data []byte) (Rule, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Rule
	if err := dec.Decode(&r); err != nil {
		return Rule{}, fmt.Errorf("decode rule: %w", err)
	}
	// Reject trailing content after the object (two concatenated rules, stray bytes).
	if _, err := dec.Token(); err != io.EOF {
		return Rule{}, fmt.Errorf("decode rule: unexpected trailing content after the rule object")
	}
	return r, nil
}

// RuleType is the v1 detection taxonomy discriminator (ADR-051). Each maps to one or
// more keyed-streaming core kinds via the compiler.
type RuleType string

const (
	// TypeThreshold fires on every event whose leaf comparison holds (level-triggered).
	TypeThreshold RuleType = "threshold"
	// TypeDeltaRate fires on the change between a series' consecutive matching samples,
	// optionally per-second (Rate).
	TypeDeltaRate RuleType = "deltaRate"
	// TypeRepeating fires when Count matching events fall within a sliding Window.
	TypeRepeating RuleType = "repeating"
	// TypeDuration fires when the leaf comparison stays true continuously for Hold.
	TypeDuration RuleType = "duration"
	// TypeAbsence fires when no event arrives for a series within Timeout of the last.
	TypeAbsence RuleType = "absence"
	// TypeAggregate fires when an aggregate (Agg) of the value metric over a window
	// (Mode: tumbling/sliding/session/count) satisfies Op vs Threshold.
	TypeAggregate RuleType = "aggregate"
	// TypeCorrelation fires when the number of distinct members (devices) reporting under
	// an anchor (AnchorType) within Window reaches Count — area/fleet correlation.
	TypeCorrelation RuleType = "correlation"
)

// WindowMode selects the windowed-aggregate shape for TypeAggregate.
type WindowMode string

const (
	// ModeTumbling is a fixed, non-overlapping time window (core.Aggregate).
	ModeTumbling WindowMode = "tumbling"
	// ModeSliding is a trailing time window re-evaluated per event (core.SlidingAgg).
	ModeSliding WindowMode = "sliding"
	// ModeSession is a gap-delimited window closed after Gap of silence (core.Session).
	ModeSession WindowMode = "session"
	// ModeCount is a tumbling window measured in Count events, not time (core.CountWindow).
	ModeCount WindowMode = "count"
)

// CompareOp is a comparison operator. The four ordered operators map to the core's CmpOp
// for engine-side aggregate/delta thresholds; Eq/Ne are additionally allowed only in a
// predicate-side leaf comparison (which lowers to CEL, where equality is well-defined).
type CompareOp string

const (
	OpGt CompareOp = "gt"
	OpGe CompareOp = "ge"
	OpLt CompareOp = "lt"
	OpLe CompareOp = "le"
	OpEq CompareOp = "eq"
	OpNe CompareOp = "ne"
)

// AggFunc is a windowed aggregate function (maps to core.AggOp).
type AggFunc string

const (
	AggCount AggFunc = "count"
	AggSum   AggFunc = "sum"
	AggAvg   AggFunc = "avg"
	AggMin   AggFunc = "min"
	AggMax   AggFunc = "max"
)

// Condition is a rule's leaf: the boolean test one event must pass. Exactly one form is
// populated — a structured comparison (the no-CEL default the form builder emits) or a
// raw CEL string (advanced escape hatch) — or neither, which means "match every event"
// (valid where the temporal shape carries the logic: absence, aggregate, correlation).
type Condition struct {
	// Structured comparison `<Metric> <Op> <bound>`, where the bound is EITHER a literal
	// Threshold OR a device-attribute reference ThresholdAttr (mutually exclusive). Metric
	// must satisfy the ADR-042 token grammar; it is rendered into CEL as a validated
	// identifier, never raw-spliced.
	Metric    string    `json:"metric,omitempty"`
	Op        CompareOp `json:"op,omitempty"`
	Threshold *float64  `json:"threshold,omitempty"`

	// ThresholdAttr names a device attribute whose numeric value is the comparison bound — a
	// DYNAMIC, per-device threshold (ADR-051 slice 4c-3b): "temperature > the device's own
	// tempLimit attribute". It is mutually exclusive with the literal Threshold. The key must
	// satisfy the ADR-042 token grammar (letters/digits/-/_ , <= core.MaxTokenLen), which makes
	// it injection-safe as a CEL map key AND matches the durable projection's key column. When
	// set, the leaf lowers to a presence-guarded dynamic comparison (see generateDynamicComparison)
	// that reads the value from the `attr` CEL var the runtime populates from the device's durable
	// attribute projection (SERVER/SHARED scope, ADR-012); a device with no such attribute is a
	// clean non-match, never an evaluation error.
	ThresholdAttr string `json:"thresholdAttr,omitempty"`

	// CEL is the raw escape hatch, mutually exclusive with the structured fields. It is
	// type-checked and cost-gated like any predicate; it references only the declared
	// event vocabulary (predicate.VarM etc.).
	CEL string `json:"cel,omitempty"`
}

// isZero reports the match-every-event leaf (no structured comparison, no raw CEL).
func (c Condition) isZero() bool {
	return c.Metric == "" && c.Op == "" && c.Threshold == nil && c.ThresholdAttr == "" && c.CEL == ""
}

// isRaw reports the raw-CEL escape hatch.
func (c Condition) isRaw() bool { return c.CEL != "" }

// isStructured reports a structured comparison.
func (c Condition) isStructured() bool {
	return c.Metric != "" || c.Op != "" || c.Threshold != nil || c.ThresholdAttr != ""
}

// Rule is the structured detection rule. Only the fields relevant to Type are used; the
// compiler validates that no irrelevant field is set (fail-closed on an ill-formed rule,
// matching the "reject unknown/invalid keys" convention) and lowers the rest.
type Rule struct {
	// ID is the rule's stable identifier (tenant-token-prefixed by the runtime, slice 4);
	// Name/Description are authoring metadata. The versioned draft/publish/rollback
	// envelope (ADR-045) lives above this schema.
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	Type RuleType `json:"type"`

	// When is the per-event leaf. For threshold/duration it is the fire comparison; for
	// repeating it is an optional per-event condition (empty ⇒ count every event); for
	// aggregate/correlation it is an optional participation gate (empty ⇒ every event
	// participates). Absence takes NO leaf: the core treats every event as a heartbeat and
	// ignores Match, so a gate would be silently inert — the compiler rejects one.
	When Condition `json:"when"`

	// Metric is the value selector — the measurement whose numeric value the core folds —
	// for deltaRate and for aggregate when Agg is not count. Must satisfy the ADR-042
	// token grammar.
	Metric string `json:"metric,omitempty"`

	// Temporal shape. Only the fields relevant to Type (and Mode) are used.
	Window Duration   `json:"window,omitempty"`     // repeating · aggregate(time modes) · correlation
	Hold   Duration   `json:"hold,omitempty"`       // duration
	Ttl    Duration   `json:"timeout,omitempty"`    // absence
	Gap    Duration   `json:"gap,omitempty"`        // aggregate(session)
	Count  int        `json:"count,omitempty"`      // repeating(N) · aggregate(count mode N) · correlation(distinct N)
	Rate   bool       `json:"rate,omitempty"`       // deltaRate
	Mode   WindowMode `json:"windowMode,omitempty"` // aggregate

	// Aggregate / deltaRate engine-side comparison. Threshold is a pointer so a value of
	// exactly 0 is distinguishable from "unset" (a bare float64 with omitempty would omit a
	// legitimate `> 0` threshold and silently default a missing one to 0) — it is required
	// wherever Op is, and rejected where the engine folds no value.
	Agg       AggFunc   `json:"agg,omitempty"`
	Op        CompareOp `json:"op,omitempty"`
	Threshold *float64  `json:"threshold,omitempty"`

	// Correlation.
	AnchorType string `json:"anchorType,omitempty"` // the anchor (area) distinct members roll up to
	MemberCap  int    `json:"memberCap,omitempty"`  // retained-member backstop
}

// Duration is a time.Duration that (de)serializes as a Go duration string ("5m0s") so the
// authored schema is human-readable and canonical. Unmarshalling accepts any Go duration
// string; marshalling always emits the canonical form, so a re-marshal canonicalizes —
// the property the form/canvas byte-identity check (ADR-053) relies on.
type Duration time.Duration

// D is the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
