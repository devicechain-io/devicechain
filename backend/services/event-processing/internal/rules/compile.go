// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// Limits are the per-tenant compile-time ceilings. The caller resolves them BEFORE Compile
// from the tenant's overrides falling back to the platform default — a missing or zero
// override must resolve here to the platform default, NEVER to "unlimited" (the ADR-023
// fail-safe posture). Compile treats a zero field as "unset" and substitutes the built-in
// floor so it can never accidentally run uncapped.
type Limits struct {
	// PredicateCostCeiling is the maximum static worst-case CEL cost a leaf may estimate
	// to at publish (and the runtime CostLimit on the compiled program).
	PredicateCostCeiling uint64
	// DefaultCorrelationMemberCap is the retained-member backstop applied to a correlation
	// rule that does not set its own MemberCap.
	DefaultCorrelationMemberCap int
}

// Built-in floors used when a Limits field is left zero, so Compile is never uncapped.
const (
	defaultPredicateCostCeiling      uint64 = 100
	defaultCorrelationMemberCapFloor        = 1024
)

// DefaultLimits is the platform-default compile budget applied to a published detection
// rule. It is the SINGLE source both compile sites must share: the ADR-044 publish gate
// (graphql.ValidateDetectionRules, slice 4b-2) and the runtime fact consumer that loads a
// published rule into the engine (runtime.CompilePublishedRules, slice 4b-3). If the two
// diverged, a rule could pass the publish gate and then be rejected when the engine
// consumes it (or the reverse). Zero fields floor to the built-in caps inside Compile, so
// this is never uncapped (ADR-023 never-unlimited). When per-tenant governance overrides
// land (ADR-023, slice 6) BOTH sites resolve the caller's tenant limits from one source,
// replacing this.
func DefaultLimits() Limits { return Limits{} }

func (l Limits) withDefaults() Limits {
	if l.PredicateCostCeiling == 0 {
		l.PredicateCostCeiling = defaultPredicateCostCeiling
	}
	if l.DefaultCorrelationMemberCap <= 0 {
		// <= 0, not == 0: a negative caller misconfig must also floor, else every
		// default-cap correlation rule is rejected downstream with a confusing message.
		l.DefaultCorrelationMemberCap = defaultCorrelationMemberCapFloor
	}
	return l
}

// CompiledRule is a rule lowered to its runnable form: the keyed-streaming core config
// plus the compiled leaf predicate. It is produced once at publish (compile-once) and
// reused for every event — the runtime evaluates Predicate to set the core Event's Match
// (and reads ValueMetric for its Value), then feeds the event to the engine keyed by Core.
//
// THE METRIC-SCOPED FEED CONTRACT (the runtime, slice 4, MUST honor this). Match is
// binary, but "condition false" and "condition not measurable" are different facts, and
// exactly one kind — Duration — acts on false (a non-matching event CANCELS the running
// hold, engine.go). So a Duration/Threshold/Repeating rule keyed on a metric must be fed
// ONLY events that carry that metric; otherwise a device interleaving other telemetry
// (a battery reading between temperature readings) evaluates the leaf to false and, for
// Duration, resets the hold forever. The compiler surfaces the relevance metric so the
// runtime can scope the feed without re-parsing:
//
//   - GateMetric set  → feed only events carrying GateMetric (structured threshold/
//     duration/repeating). The presence guard in the predicate is defense-in-depth.
//   - ValueMetric set → feed only events carrying ValueMetric (deltaRate, non-count
//     aggregate); the predicate's presence guard also enforces it.
//   - both empty      → feed every in-scope event. Absence is deliberately here (every
//     event is a heartbeat, device-scoped not metric-scoped); so are match-every leaves,
//     count aggregates, and correlation. A RAW-CEL leaf is also here, and a raw-CEL DURATION
//     rule on a mixed-telemetry device is a genuine trap: there is no expressible total leaf
//     that metric-scopes it. A presence-guarded leaf (`"m" in metrics && …`) evaluates to a
//     clean FALSE on an off-metric event — which for Duration CANCELS the hold. An unguarded
//     leaf ERRORS on the missing key, and the runtime treats an eval error as a SKIP (no
//     event, hold preserved) — so the "sloppy" leaf accidentally behaves while spamming the
//     eval-error counter. Neither is a metric scope. A structured leaf (which gets GateMetric)
//     is the only correct way to metric-scope a Duration rule; the publish gate should steer
//     authors off raw-CEL Duration (slice 7/console).
type CompiledRule struct {
	ID   string
	Type RuleType

	// Severity is the detection's significance tier (ADR-041), carried through from the authored
	// rule so the publisher can stamp it onto the derived event without re-decoding. Empty for a
	// rule that declares none. The REACT action chain itself is NOT carried here — the dispatcher
	// (slice 5b) resolves it from the durable rule projection by id, keeping the engine's compiled
	// form about detection only.
	Severity Severity

	// Core is the keyed-streaming engine rule (Kind + temporal params). Its ID equals ID,
	// so the engine keys this rule's series state by the same id.
	Core core.Rule

	// Predicate is the compiled leaf. It is never nil: a match-every-event leaf compiles
	// to the constant `true`. For value-consuming kinds it also AND-guards the presence of
	// ValueMetric, so a folded Value is always a real reading, never a missing-key zero.
	Predicate *predicate.Predicate

	// ValueMetric is the measurement whose numeric value feeds core Event.Value (deltaRate,
	// and aggregate with a non-count Agg). Empty when the core folds no value.
	ValueMetric string

	// GateMetric is the measurement whose PRESENCE makes an event relevant to a
	// structured-leaf rule that folds no value (threshold, duration, repeating). It is the
	// hook for the runtime's metric-scoped feed contract (see below); empty for a raw-CEL
	// or match-every leaf, and for value-consuming kinds (where ValueMetric plays the same
	// role).
	GateMetric string

	// AnchorType is the anchor a correlation rule keys its series on; the runtime resolves
	// the event's anchor of this type to the series token and uses the device as the
	// distinct member. Empty for every non-correlation rule (series = the device token).
	AnchorType string
}

// KeyedByAnchor reports whether the rule's series is an anchor token (correlation) rather
// than the source device token (every other kind).
func (cr *CompiledRule) KeyedByAnchor() bool { return cr.AnchorType != "" }

// Compile validates a structured rule, lowers it to a keyed-streaming core rule, and
// compiles + cost-gates its leaf predicate — the publish-time gate. It fails closed:
// any structural error, an unsupported field for the rule's type, a predicate that does
// not type-check, or a predicate whose worst-case cost exceeds the ceiling rejects the
// rule with a console-surfaceable message. On success the returned CompiledRule is
// immutable and reusable for every event.
func Compile(r Rule, limits Limits) (*CompiledRule, error) {
	limits = limits.withDefaults()
	if r.ID == "" {
		return nil, invalid(r.ID, "id", "a rule id is required")
	}
	if r.Name == "" {
		return nil, invalid(r.ID, "name", "a rule name is required")
	}

	cr := &CompiledRule{ID: r.ID, Type: r.Type}
	cr.Core.ID = r.ID

	var leafSrc string
	var err error
	switch r.Type {
	case TypeThreshold:
		leafSrc, err = compileThreshold(r, cr)
	case TypeDeltaRate:
		leafSrc, err = compileDeltaRate(r, cr)
	case TypeRepeating:
		leafSrc, err = compileRepeating(r, cr)
	case TypeDuration:
		leafSrc, err = compileDuration(r, cr)
	case TypeAbsence:
		leafSrc, err = compileAbsence(r, cr)
	case TypeAggregate:
		leafSrc, err = compileAggregate(r, cr)
	case TypeCorrelation:
		leafSrc, err = compileCorrelation(r, cr, limits)
	default:
		return nil, invalid(r.ID, "type", "unknown rule type %q", r.Type)
	}
	if err != nil {
		return nil, err
	}

	pred, err := predicate.Compile(leafSrc, limits.PredicateCostCeiling)
	if err != nil {
		// Anchor the leaf error to the rule + its `when` input for the console, while
		// preserving the underlying predicate.CompileError/CostError for errors.As.
		return nil, &ValidationError{RuleID: r.ID, Field: "when", Msg: err.Error(), Err: err}
	}
	cr.Predicate = pred

	// Validate the REACT layer (severity + action chain) and carry the severity through. This is
	// the same publish-time gate the detection fields pass: a malformed action or an alarm without
	// a tier is rejected here (fail-closed) so it can never reach the dispatcher.
	if err := validateReact(r); err != nil {
		return nil, err
	}
	cr.Severity = r.Severity
	return cr, nil
}

// validateReact enforces the REACT contract on an authored rule (ADR-051 REACT stage): a bounded
// action chain, each action well-formed for its type, and a severity that is present when an alarm
// is raised and always within the known set. It is separate from the per-type detection lowering
// because actions are orthogonal to the detection shape — any rule type may carry any action.
func validateReact(r Rule) error {
	if r.Severity != "" && !r.Severity.Valid() {
		return invalid(r.ID, "severity", "unknown severity %q", r.Severity)
	}
	if len(r.Actions) > MaxActionsPerRule {
		return invalid(r.ID, "actions", "a rule may declare at most %d actions, got %d", MaxActionsPerRule, len(r.Actions))
	}
	seen := make(map[string]struct{}, len(r.Actions))
	for i, a := range r.Actions {
		if err := validateAction(r.ID, i, a); err != nil {
			return err
		}
		if a.Type == ActionRaiseAlarm && !r.Severity.Valid() {
			// A raiseAlarm action needs a tier to raise at; the tier lives on the rule (one rule,
			// one severity), so an alarm action without a valid rule severity is rejected.
			return invalid(r.ID, "severity", "a raiseAlarm action requires a valid rule severity")
		}
		// Reject an exact-duplicate action (fail-closed). The dispatcher keys idempotency on the
		// detection identity PLUS the action index, so two identical actions are distinct keys and
		// BOTH fire — a double alarm-raise (harmless: an in-place upsert) but a genuine DOUBLE
		// command send. An author never means to send the same command twice per detection, so a
		// textual duplicate is treated as a mistake here rather than dispatched twice downstream.
		key := actionDedupKey(a)
		if _, dup := seen[key]; dup {
			return invalid(r.ID, "actions", "action %d duplicates an earlier action", i)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// isJSONObject reports whether s is a syntactically valid JSON object (`{...}`), not a bare
// scalar, array, or null. A command payload is an argument map, so only an object is well-formed.
func isJSONObject(s string) bool {
	if !json.Valid([]byte(s)) {
		return false
	}
	// `null` unmarshals into a map without error but leaves it nil, and an array/scalar errors;
	// only a real object yields a non-nil map — so a nil map after a clean unmarshal rejects `null`.
	var m map[string]json.RawMessage
	return json.Unmarshal([]byte(s), &m) == nil && m != nil
}

// actionDedupKey is a textual identity for exact-duplicate detection. Two sendCommand actions with
// the same command but different payloads are legitimately distinct (send two different commands),
// so the payload is part of the key; the payload is compared verbatim (it is not canonicalized —
// see SendCommandAction.Payload), so this catches identical authored bytes, the mistake case.
func actionDedupKey(a Action) string {
	switch a.Type {
	case ActionRaiseAlarm:
		return "raiseAlarm|" + a.RaiseAlarm.AlarmKey
	case ActionSendCommand:
		return "sendCommand|" + a.SendCommand.Command + "|" + a.SendCommand.Payload
	default:
		return string(a.Type)
	}
}

// validateAction checks one action: its type is known, exactly the matching variant is populated,
// and that variant's fields are well-formed. The index anchors the error to the offending action
// for the console.
func validateAction(ruleID string, i int, a Action) error {
	switch a.Type {
	case ActionRaiseAlarm:
		if a.RaiseAlarm == nil || a.SendCommand != nil {
			return invalid(ruleID, "actions", "action %d is type %q but its raiseAlarm payload is missing or a foreign payload is set", i, a.Type)
		}
		if a.RaiseAlarm.AlarmKey != "" {
			if err := validateMetric(a.RaiseAlarm.AlarmKey); err != nil {
				return invalid(ruleID, "actions", "action %d alarmKey: %v", i, err)
			}
		}
	case ActionSendCommand:
		if a.SendCommand == nil || a.RaiseAlarm != nil {
			return invalid(ruleID, "actions", "action %d is type %q but its sendCommand payload is missing or a foreign payload is set", i, a.Type)
		}
		if err := validateMetric(a.SendCommand.Command); err != nil {
			return invalid(ruleID, "actions", "action %d command: %v", i, err)
		}
		if a.SendCommand.Payload != "" && !isJSONObject(a.SendCommand.Payload) {
			// A command payload is an argument MAP, so it must be a JSON object — not a bare
			// scalar or array. Rejecting `42`/`[1,2]`/`"x"` here closes the one statically-
			// checkable payload defect at the publish gate (the command-key existence and the
			// payload-vs-parameter-schema cross-check are NOT done here — see the note on
			// SendCommandAction — so this is the only payload guard until that lands).
			return invalid(ruleID, "actions", "action %d command payload must be a JSON object", i)
		}
	default:
		return invalid(ruleID, "actions", "action %d has unknown type %q", i, a.Type)
	}
	return nil
}

// --- per-type lowering. Each validates the fields it uses, rejects fields it does not,
//     writes the core config into cr.Core, and returns the leaf CEL source. ---

func compileThreshold(r Rule, cr *CompiledRule) (string, error) {
	if err := forbid(r, "threshold", forbidden{value: true, window: true, hold: true, timeout: true, gap: true, count: true, rate: true, mode: true, agg: true, op: true, threshold: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	cr.Core.Kind = core.Threshold
	cr.GateMetric = structuredGate(r)
	return requireLeaf(r) // the comparison IS the rule
}

func compileDeltaRate(r Rule, cr *CompiledRule) (string, error) {
	if err := forbid(r, "deltaRate", forbidden{window: true, hold: true, timeout: true, gap: true, count: true, mode: true, agg: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	metric, err := requireValueMetric(r)
	if err != nil {
		return "", err
	}
	kop, err := orderedOp(r, "op")
	if err != nil {
		return "", err
	}
	thresh, err := requireThreshold(r)
	if err != nil {
		return "", err
	}
	cr.Core.Kind = core.DeltaRate
	cr.Core.Op = kop
	cr.Core.Thresh = thresh
	cr.Core.Rate = r.Rate
	cr.ValueMetric = metric
	return valueGuardedLeaf(r, metric)
}

func compileRepeating(r Rule, cr *CompiledRule) (string, error) {
	if err := forbid(r, "repeating", forbidden{value: true, hold: true, timeout: true, gap: true, rate: true, mode: true, agg: true, op: true, threshold: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	if r.Count < 1 {
		return "", invalid(r.ID, "count", "repeating requires a positive count")
	}
	if r.Window.D() <= 0 {
		return "", invalid(r.ID, "window", "repeating requires a positive window")
	}
	cr.Core.Kind = core.Repeating
	cr.Core.Count = r.Count
	cr.Core.Window = r.Window.D()
	cr.GateMetric = structuredGate(r)
	return optionalLeaf(r) // the per-event condition (empty ⇒ count every event)
}

func compileDuration(r Rule, cr *CompiledRule) (string, error) {
	if err := forbid(r, "duration", forbidden{value: true, window: true, timeout: true, gap: true, count: true, rate: true, mode: true, agg: true, op: true, threshold: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	if r.Hold.D() <= 0 {
		return "", invalid(r.ID, "hold", "duration requires a positive hold")
	}
	cr.Core.Kind = core.Duration
	cr.Core.Hold = r.Hold.D()
	cr.GateMetric = structuredGate(r)
	return requireLeaf(r) // the sustained condition
}

func compileAbsence(r Rule, cr *CompiledRule) (string, error) {
	// A leaf gate is deliberately disallowed: the core treats every event as a heartbeat
	// (Match is ignored for Absence), so accepting a `when` would silently do nothing.
	if err := forbid(r, "absence", forbidden{leaf: true, value: true, window: true, hold: true, gap: true, count: true, rate: true, mode: true, agg: true, op: true, threshold: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	if r.Ttl.D() <= 0 {
		return "", invalid(r.ID, "timeout", "absence requires a positive timeout")
	}
	cr.Core.Kind = core.Absence
	cr.Core.Timeout = r.Ttl.D()
	return matchAll, nil // heartbeats: every event counts
}

func compileAggregate(r Rule, cr *CompiledRule) (string, error) {
	if err := forbid(r, "aggregate", forbidden{hold: true, timeout: true, rate: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	aggOp, err := aggFunc(r)
	if err != nil {
		return "", err
	}
	kop, err := orderedOp(r, "op")
	if err != nil {
		return "", err
	}
	thresh, err := requireThreshold(r)
	if err != nil {
		return "", err
	}
	cr.Core.Agg = aggOp
	cr.Core.Op = kop
	cr.Core.Thresh = thresh

	// The value metric is required unless the aggregate is a pure event count. Note: an
	// event-count aggregate with an lt/le op cannot observe its most interesting case — the
	// empty (zero-event) window — because a window with no matching events produces no pane
	// (tumbling) and never satisfies (sliding). Such a rule still fires for a window with
	// 1..N-1 events, so it is not rejected, but "traffic dropped to zero" is an Absence rule,
	// not a count aggregate; the console should steer authors accordingly (slice 7).
	var metric string
	if aggOp != core.AggCount {
		if metric, err = requireValueMetric(r); err != nil {
			return "", err
		}
		cr.ValueMetric = metric
	} else if r.Metric != "" {
		return "", invalid(r.ID, "metric", "a count aggregate takes no value metric")
	}

	switch r.Mode {
	case ModeTumbling:
		if err := onlyWindow(r, "tumbling"); err != nil {
			return "", err
		}
		cr.Core.Kind = core.Aggregate
		cr.Core.Window = r.Window.D()
	case ModeSliding:
		if err := onlyWindow(r, "sliding"); err != nil {
			return "", err
		}
		cr.Core.Kind = core.SlidingAgg
		cr.Core.Window = r.Window.D()
	case ModeSession:
		if r.Gap.D() <= 0 {
			return "", invalid(r.ID, "gap", "a session aggregate requires a positive gap")
		}
		if r.Window.D() != 0 || r.Count != 0 {
			return "", invalid(r.ID, "windowMode", "a session aggregate uses gap, not window/count")
		}
		cr.Core.Kind = core.Session
		cr.Core.Gap = r.Gap.D()
	case ModeCount:
		if r.Count < 1 {
			return "", invalid(r.ID, "count", "a count-window aggregate requires a positive count")
		}
		if r.Window.D() != 0 || r.Gap.D() != 0 {
			return "", invalid(r.ID, "windowMode", "a count-window aggregate uses count, not window/gap")
		}
		if aggOp == core.AggCount {
			// A count aggregate over a count window is a constant: the window is evaluated
			// exactly when its event count reaches Count, so `count <op> thresh` is decidable
			// at publish (dead, or fires unconditionally every Count events) — never a
			// detection. Reject it rather than ship a silently-dead or always-firing rule.
			return "", invalid(r.ID, "agg", "counting events over a count window is a constant; use a time window or a value aggregate")
		}
		cr.Core.Kind = core.CountWindow
		cr.Core.Count = r.Count
	case "":
		return "", invalid(r.ID, "windowMode", "an aggregate requires a window mode (tumbling/sliding/session/count)")
	default:
		return "", invalid(r.ID, "windowMode", "unknown window mode %q", r.Mode)
	}

	if metric != "" {
		return valueGuardedLeaf(r, metric) // only fold events carrying the value metric
	}
	return optionalLeaf(r)
}

func compileCorrelation(r Rule, cr *CompiledRule, limits Limits) (string, error) {
	if err := forbid(r, "correlation", forbidden{value: true, hold: true, timeout: true, gap: true, rate: true, mode: true, agg: true, op: true, threshold: true}); err != nil {
		return "", err
	}
	if r.AnchorType == "" {
		return "", invalid(r.ID, "anchorType", "correlation requires an anchor type")
	}
	if err := validateMetric(r.AnchorType); err != nil {
		return "", invalid(r.ID, "anchorType", "%v", err)
	}
	if r.Count < 1 {
		return "", invalid(r.ID, "count", "correlation requires a positive distinct-member count")
	}
	if r.Window.D() <= 0 {
		return "", invalid(r.ID, "window", "correlation requires a positive window")
	}
	memberCap := r.MemberCap
	if memberCap == 0 {
		memberCap = limits.DefaultCorrelationMemberCap
	}
	if memberCap < r.Count {
		return "", invalid(r.ID, "memberCap", "member cap %d is below the distinct-member count %d", memberCap, r.Count)
	}
	cr.Core.Kind = core.Correlation
	cr.Core.Count = r.Count
	cr.Core.Window = r.Window.D()
	cr.Core.MemberCap = memberCap
	cr.AnchorType = r.AnchorType
	return optionalLeaf(r) // the per-member gate
}

// matchAll is the leaf every event passes; it is what a zero Condition lowers to.
const matchAll = "true"

// requireLeaf returns the leaf source for a rule whose condition is mandatory (the
// comparison defines the rule: threshold, duration).
func requireLeaf(r Rule) (string, error) {
	if r.When.isZero() {
		return "", invalid(r.ID, "when", "this rule type requires a condition")
	}
	return leafSource(r)
}

// optionalLeaf returns the leaf source for a rule whose condition is an optional gate; a
// zero condition matches every event.
func optionalLeaf(r Rule) (string, error) {
	if r.When.isZero() {
		return matchAll, nil
	}
	return leafSource(r)
}

// valueGuardedLeaf AND-guards the optional leaf with the presence of the value metric, so
// only events actually carrying the folded measurement participate — a missing-metric
// event becomes a clean non-match rather than folding a spurious zero into the aggregate.
func valueGuardedLeaf(r Rule, metric string) (string, error) {
	guard, err := presenceGuard(metric)
	if err != nil {
		return "", err
	}
	leaf, err := optionalLeaf(r)
	if err != nil {
		return "", err
	}
	if leaf == matchAll {
		return guard, nil
	}
	return fmt.Sprintf("(%s) && (%s)", guard, leaf), nil
}

// leafSource renders the leaf (structured comparison or raw CEL) to CEL source. A rule may
// set exactly one form.
func leafSource(r Rule) (string, error) {
	c := r.When
	if c.isRaw() && c.isStructured() {
		return "", invalid(r.ID, "when", "a condition sets either a structured comparison or raw CEL, not both")
	}
	if c.isRaw() {
		return c.CEL, nil
	}
	// Structured: `<metric> <op> <bound>`, where the bound is EITHER a literal threshold OR a
	// device-attribute reference (a dynamic, per-device threshold) — exactly one.
	if c.Metric == "" || c.Op == "" {
		return "", invalid(r.ID, "when", "a structured comparison needs a metric and an operator")
	}
	hasLit, hasAttr := c.Threshold != nil, c.ThresholdAttr != ""
	var (
		src string
		err error
	)
	switch {
	case hasLit && hasAttr:
		return "", invalid(r.ID, "when", "a comparison sets either a literal threshold or a threshold attribute, not both")
	case hasLit:
		src, err = generateComparison(c.Metric, c.Op, *c.Threshold)
	case hasAttr:
		src, err = generateDynamicComparison(c.Metric, c.Op, c.ThresholdAttr)
	default:
		return "", invalid(r.ID, "when", "a structured comparison needs a threshold or a threshold attribute")
	}
	if err != nil {
		return "", invalid(r.ID, "when", "%v", err)
	}
	return src, nil
}

// presenceGuard renders `"<metric>" in m` for a grammar-validated metric.
func presenceGuard(metric string) (string, error) {
	if err := validateMetric(metric); err != nil {
		return "", err
	}
	return fmt.Sprintf("%q in %s", metric, predicate.VarM), nil
}

// requireThreshold returns the engine-side comparison threshold, requiring it to be set.
// Threshold is a pointer precisely so a value of exactly 0 is distinguishable from unset —
// a missing threshold is a fail-closed rejection, not a silent comparison against 0.
func requireThreshold(r Rule) (float64, error) {
	if r.Threshold == nil {
		return 0, invalid(r.ID, "threshold", "a threshold is required")
	}
	return *r.Threshold, nil
}

// structuredGate returns the metric a structured leaf gates on — its presence is what makes
// an event relevant to the rule (the runtime's metric-scoped feed hook). Empty for a
// raw-CEL or match-every leaf, where the runtime feeds every in-scope event.
func structuredGate(r Rule) string {
	if r.When.isStructured() {
		return r.When.Metric
	}
	return ""
}

// requireValueMetric validates and returns the value-selector metric.
func requireValueMetric(r Rule) (string, error) {
	if r.Metric == "" {
		return "", invalid(r.ID, "metric", "this rule type requires a value metric")
	}
	if err := validateMetric(r.Metric); err != nil {
		return "", invalid(r.ID, "metric", "%v", err)
	}
	return r.Metric, nil
}

// orderedOp maps a schema operator to a core CmpOp, rejecting the unordered eq/ne (the
// core's aggregate/delta comparison is defined only for the four ordered operators).
func orderedOp(r Rule, field string) (core.CmpOp, error) {
	switch r.Op {
	case OpGt:
		return core.GT, nil
	case OpGe:
		return core.GE, nil
	case OpLt:
		return core.LT, nil
	case OpLe:
		return core.LE, nil
	case "":
		return 0, invalid(r.ID, field, "an operator is required")
	default:
		return 0, invalid(r.ID, field, "operator %q is not valid here (use gt/ge/lt/le)", r.Op)
	}
}

// aggFunc maps a schema aggregate function to a core AggOp.
func aggFunc(r Rule) (core.AggOp, error) {
	switch r.Agg {
	case AggCount:
		return core.AggCount, nil
	case AggSum:
		return core.AggSum, nil
	case AggAvg:
		return core.AggAvg, nil
	case AggMin:
		return core.AggMin, nil
	case AggMax:
		return core.AggMax, nil
	case "":
		return 0, invalid(r.ID, "agg", "an aggregate function is required")
	default:
		return 0, invalid(r.ID, "agg", "unknown aggregate function %q", r.Agg)
	}
}

// onlyWindow requires a positive Window and rejects the other window-shaping fields for a
// time-windowed aggregate mode.
func onlyWindow(r Rule, mode string) error {
	if r.Window.D() <= 0 {
		return invalid(r.ID, "window", "a %s aggregate requires a positive window", mode)
	}
	if r.Gap.D() != 0 || r.Count != 0 {
		return invalid(r.ID, "windowMode", "a %s aggregate uses window, not gap/count", mode)
	}
	return nil
}
