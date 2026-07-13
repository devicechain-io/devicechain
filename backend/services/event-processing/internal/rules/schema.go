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

// Severity labels how significant a detection is (ADR-041 vocabulary): the alarm tier a
// raiseAlarm action raises at, and a first-class field on the derived event for ADR-037
// subscribers. It reuses the alarm severity levels so a rule and the alarm it raises speak
// one set of tiers; the values are lowercase to match the schema's JSON-value style, and the
// REACT dispatcher (slice 5c) maps them to the alarm wire form. Empty is a valid "unclassified
// signal" — allowed on a rule with no raiseAlarm action, rejected on one that has (an alarm
// cannot be raised without a tier).
type Severity string

const (
	SeverityCritical      Severity = "critical"
	SeverityMajor         Severity = "major"
	SeverityMinor         Severity = "minor"
	SeverityWarning       Severity = "warning"
	SeverityIndeterminate Severity = "indeterminate"
)

// Valid reports whether the severity names one of the known levels.
func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityMajor, SeverityMinor, SeverityWarning, SeverityIndeterminate:
		return true
	default:
		return false
	}
}

// ActionType discriminates a REACT action (ADR-051 REACT stage / ADR-054). The set is
// deliberately small and closed: REACT is a bounded, non-Turing action chain, not a
// scripting surface.
type ActionType string

const (
	// ActionRaiseAlarm raises/escalates an alarm for the detection's device (ADR-041). The
	// alarm object, ack/clear, graph rollup, and notify last-mile all stay in device-management;
	// REACT only signals the raise (slice 5c, gated off until slice 6).
	ActionRaiseAlarm ActionType = "raiseAlarm"
	// ActionSendCommand enqueues a command to the detection's device via command-delivery
	// (ADR-043). It has no legacy twin, so it goes live in slice 5b.
	ActionSendCommand ActionType = "sendCommand"
	// ActionHTTPCall posts a CEL-shaped payload to an external HTTP endpoint (ADR-060 Tier 1:
	// the hand-rolled webhook sink). Config + optional secret handle are inline on the action —
	// no registered resource for a one-off webhook. Execution lives in the outbound-connectors
	// service (ADR-060 §4); REACT only shapes the payload and publishes a connector-dispatch
	// request (slice C2b).
	ActionHTTPCall ActionType = "httpCall"
	// ActionPublish sends a CEL-shaped payload to a registered, versioned Connector (ADR-060
	// Tier 2: the embedded-Bento breadth multiplier — MQTT/Kafka/SNS/SQS/Pub-Sub). The action
	// references the connector by token; the connector carries the transport type, target, and
	// secret handle, so credentials never live in the rule/graph. Execution + the Connector
	// resource are owned by outbound-connectors (ADR-060 §4).
	ActionPublish ActionType = "publish"
)

// MaxActionTimeoutMs bounds a connector action's per-dispatch timeout (ADR-060). 0 ⇒ the
// dispatcher applies its default; a value above this cap is rejected at publish so a rule cannot
// author an unbounded outbound wait.
const MaxActionTimeoutMs = 60_000

// maxSecretHandleLen bounds an authored secret handle (HTTPCallAction.SecretRef). The handle is a
// core/secrets ref Name, which legitimately contains '/', so it is not an ADR-042 token; it is
// validated as a bounded, safe-character path (validateSecretHandle) instead.
const maxSecretHandleLen = 256

// MaxActionsPerRule bounds a rule's action chain. REACT is declarative and finite (ADR-054):
// a fixed, capped list of typed actions with no control flow. The cap is a fail-closed backstop
// against a forged/buggy definition, generous for any real authored rule.
const MaxActionsPerRule = 8

// Action is one bounded REACT action a detection dispatches. Type selects exactly one populated
// variant (the compiler rejects a mismatch or a multiply-populated action); the variant carries
// that action's declarative parameters. There is no ordering dependency beyond list index and no
// data flow between actions — each is dispatched independently and idempotently by the REACT
// dispatcher (slice 5b), keyed on the detection's dedup identity (RuleID, Series, Kind,
// OccurredTime) plus the action's CONTENT (content-addressed, not its list index — so reordering a
// chain is a no-op; see react.actionContentKey / actionDedupKey).
type Action struct {
	Type        ActionType         `json:"type"`
	RaiseAlarm  *RaiseAlarmAction  `json:"raiseAlarm,omitempty"`
	SendCommand *SendCommandAction `json:"sendCommand,omitempty"`
	HTTPCall    *HTTPCallAction    `json:"httpCall,omitempty"`
	Publish     *PublishAction     `json:"publish,omitempty"`

	// Guard is an optional per-action CEL boolean the REACT dispatcher evaluates against the fired
	// detection (the derived event) to decide whether THIS action runs — the runtime form of a
	// canvas "branch" node (ADR-053 slice 9c), which folds its predicate onto the guard of every
	// action downstream of it. Empty ⇒ unconditional (the pre-9c behaviour). It is bounded and
	// stateless (ADR-054): a pure per-message boolean over the guard vocabulary (value / hasValue /
	// series — see guard.go), cost-gated at publish like the leaf predicate. It gates the RISING-edge
	// raise/send only; the dispatcher NEVER consults it for a raiseAlarm action's structural
	// falling-edge clear, so a guard can never strand an active alarm (react.Dispatcher). Two actions
	// that differ only by guard are distinct (the guard is part of the dedup/idempotency identity),
	// so a raise-if-hot / raise-if-cold pair off one detection is well-formed, not a duplicate.
	Guard string `json:"guard,omitempty"`
}

// RaiseAlarmAction raises or escalates an alarm for the detection's device at the rule's
// Severity (ADR-041). It carries no severity of its own: the rule's Severity is the single
// tier, so a rule and its alarm never disagree. The tier the dispatcher raises at is read from
// the rule as resolved from the durable projection AT DISPATCH (authoritative), not from the
// derived event's snapshot severity — see DerivedEvent.Severity.
type RaiseAlarmAction struct {
	// AlarmKey is the (originator, key) the alarm is keyed on (ADR-041 dec 3): repeated firings
	// of the same rule escalate ONE alarm in place rather than spawning duplicates. Empty ⇒ the
	// dispatcher defaults it to the rule's token, so a rule that omits it still keys stably. When
	// set it must satisfy the ADR-042 token grammar.
	AlarmKey string `json:"alarmKey,omitempty"`
}

// SendCommandAction enqueues a command to the detection's device (ADR-043). The device is the
// detection's series (device token).
//
// KNOWN VALIDATION GAP (deferred): the compiler validates only that Command is a grammar-valid
// token and Payload is a JSON object. It does NOT check that Command names a command actually
// declared on the profile being published, nor that Payload satisfies that command's parameter
// schema — both are static facts device-management owns on the same profile aggregate at publish,
// but the DETECT compiler is deliberately state-free (it never reads device state). command-delivery
// verifies the device EXISTS at enqueue but likewise does not validate the payload against the
// command's parameter schema. So a typo'd Command or a schema-invalid Payload passes the publish
// gate today and fails (silently, post-detection) at dispatch. Closing it is a device-mgmt-side
// publish cross-check against the profile's command definitions — a follow-up, not this slice.
type SendCommandAction struct {
	// Command names the command to send — the command definition's CommandKey on the device's
	// active profile (ADR-043), NOT its free-form display name. Required; must satisfy the ADR-042
	// token grammar.
	Command string `json:"command"`
	// Payload is the static invocation payload — a JSON object of arguments. Non-Turing: no
	// templating and no expressions, a fixed argument set frozen at authoring time. Must be a
	// JSON OBJECT when present (a bare scalar/array/null is rejected); empty ⇒ a no-argument
	// command. NOTE: the payload bytes are stored verbatim and NOT canonicalized, so two authoring
	// surfaces emitting the same arguments with different whitespace/key-order produce different
	// rule bytes — the ADR-053 byte-identity contract holds only if the surfaces emit canonical
	// JSON (a console/canvas concern, since no code compares rule bytes yet).
	Payload string `json:"payload,omitempty"`
}

// HTTPCallAction posts a CEL-shaped payload to an external HTTP endpoint (ADR-060 Tier 1). The
// config is inline — a one-off webhook needs no registered resource. Credentials are an ADR-059
// handle (SecretRef), resolved server-side in outbound-connectors, never carried in the rule.
type HTTPCallAction struct {
	// URL is the target endpoint. Required; must be an http/https URL (httpsink.ValidateURL) — the
	// same scheme guard the notification webhook uses. Post-resolution SSRF hardening (no-redirect,
	// reserved-header drop, response suppression) is applied by outbound-connectors via core/httpsink.
	URL string `json:"url"`
	// Method is the HTTP method. Empty ⇒ POST. Only POST is accepted in v1 (an unbounded method
	// widens the SSRF surface for no benefit), matching the notification webhook.
	Method string `json:"method,omitempty"`
	// Headers are static request headers. Reserved names (Authorization, X-DC-*) are rejected at
	// publish (they are the sink's to set / internal identity), so an author sees the error rather
	// than a silent drop at dispatch.
	Headers map[string]string `json:"headers,omitempty"`
	// BodyTemplate is a CEL expression evaluating to the request body STRING, shaped against the
	// derived event (value / hasValue / series — the guard vocabulary). Empty ⇒ no body. It is
	// cost-gated at publish like a guard (CompileTemplate) and rendered once in REACT at dispatch
	// (the outbound-connectors service receives the rendered bytes, ADR-060 §3). CEL only — no JS
	// (the ADR-053/056 determinism boundary).
	BodyTemplate string `json:"bodyTemplate,omitempty"`
	// SecretRef is an optional ADR-059 secret handle (a core/secrets ref Name) presented as the
	// auth header at dispatch. Never the cleartext. Empty ⇒ an unauthenticated call.
	SecretRef string `json:"secretRef,omitempty"`
	// TimeoutMs bounds the outbound call. 0 ⇒ the dispatcher default; capped at MaxActionTimeoutMs.
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// PublishAction sends a CEL-shaped payload to a registered, versioned Connector (ADR-060 Tier 2).
// The action names the connector by token; the connector resource (owned by outbound-connectors)
// carries the transport type, target/config, and secret handle — so one connector serves many
// rules and credentials never live in the rule/graph.
type PublishAction struct {
	// ConnectorRef is the token of the Connector to publish through. Required; must satisfy the
	// ADR-042 token grammar. Its EXISTENCE is validated best-effort at dispatch (a dangling ref
	// drops-and-logs, the notification "missing channel" precedent); the sync publish-time
	// cross-service existence check (ADR-044) lands with the Connector resource in slice C4.
	ConnectorRef string `json:"connectorRef"`
	// PayloadTemplate is a CEL expression evaluating to the message body STRING, shaped against the
	// derived event, cost-gated at publish and rendered once in REACT (see HTTPCallAction.BodyTemplate).
	// Empty ⇒ an empty payload. CEL only.
	PayloadTemplate string `json:"payloadTemplate,omitempty"`
	// TimeoutMs bounds the outbound publish. 0 ⇒ the dispatcher default; capped at MaxActionTimeoutMs.
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// populatedVariants returns the action-type discriminants whose variant pointer is non-nil. A
// well-formed action populates exactly the one matching its Type; validateAction rejects anything
// else (a missing or a foreign/multiply-populated payload).
func (a Action) populatedVariants() []ActionType {
	var v []ActionType
	if a.RaiseAlarm != nil {
		v = append(v, ActionRaiseAlarm)
	}
	if a.SendCommand != nil {
		v = append(v, ActionSendCommand)
	}
	if a.HTTPCall != nil {
		v = append(v, ActionHTTPCall)
	}
	if a.Publish != nil {
		v = append(v, ActionPublish)
	}
	return v
}

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

	// Severity labels the significance of a detection this rule produces (ADR-041 tiers). It is
	// stamped onto the derived event for ADR-037 subscribers and is the tier a raiseAlarm action
	// raises at. Optional in general; the compiler requires it when the rule has a raiseAlarm
	// action (an alarm needs a tier) and rejects any non-empty value outside the known set.
	Severity Severity `json:"severity,omitempty"`

	// Actions is the bounded, declarative REACT action chain the detection dispatches (ADR-051
	// REACT stage / ADR-054). Empty ⇒ a pure signal (published as a derived event, no side
	// effect). The compiler validates each action and caps the list at MaxActionsPerRule.
	Actions []Action `json:"actions,omitempty"`

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
