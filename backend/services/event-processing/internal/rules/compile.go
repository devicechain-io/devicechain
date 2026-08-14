// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/devicechain-io/dc-microservice/httpsink"
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
	// MaxRuleDuration is the longest temporal extent an authored rule may declare — the
	// ceiling on window, hold, timeout and gap alike.
	//
	// It exists because a window-shaped rule retains ONE RECORD PER SAMPLE for the whole
	// window: slidingState.buf (SlidingAgg) and the Repeating times slice both keep every
	// qualifying sample until it ages out. state.go's comment bounds peak memory at "~2× the
	// live window", which is only a bound if the WINDOW is bounded — and until this field
	// nothing bounded it. The arithmetic is why the ceiling is not cosmetic: a device
	// reporting every 10s produces 8,640 samples/day, a sample is ~32 bytes, so ONE series on
	// a 7-day window retains ~2MB — and a rule runs per series. A thousand devices on that one
	// rule is ~2GB of live heap in a process shared by every tenant. The authored duration is
	// the only knob that bounds it before the memory is allocated.
	//
	// The timer-shaped kinds (Duration's hold, Absence's timeout, Session's gap) retain no
	// samples — but the ceiling is NOT a courtesy extension to them. Absence and Session reset
	// their deadline on every qualifying event, and a reset PUSHES a new entry onto the timer
	// wheel's heap while the superseded one lingers there until its deadline passes (it is
	// invalidated by a generation bump, not removed). So a series holds about
	// timeout/reporting-interval stale heap entries in steady state: a 3-day absence timeout on
	// a device reporting every 10s is ~26k of them, per device. A timer-shaped duration costs
	// heap in DIRECT PROPORTION to its length, exactly like a window — the allocation just lives
	// in the wheel instead of a sample buffer, which is why neither map-based gauge sees it and
	// core.PendingTimerCount exists.
	MaxRuleDuration time.Duration
}

// Built-in floors used when a Limits field is left zero, so Compile is never uncapped.
const (
	defaultPredicateCostCeiling      uint64 = 100
	defaultCorrelationMemberCapFloor        = 1024
	// defaultMaxRuleDuration is a day. It is deliberately the same order as the 24h cap the
	// PREVIEW path has always enforced (preview.DefaultMaxWindow): previewing a rule over more
	// than a day was already refused as too expensive, while PUBLISHING one — the path that
	// allocates retained heap for as long as the rule lives — was unbounded. That asymmetry,
	// not a memory target, is what fixes the value here; the platform operator raises it for a
	// genuinely long-window tenant via maxRuleDurationSeconds.
	defaultMaxRuleDuration = 24 * time.Hour
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
func DefaultLimits() Limits {
	return Limits{MaxRuleDuration: time.Duration(platformMaxRuleDuration.Load())}
}

// platformMaxRuleDuration is the operator-configured MaxRuleDuration DefaultLimits hands to
// every compile site, in nanoseconds.
//
// It is process-global ON PURPOSE. DefaultLimits' entire job is to be the ONE budget the
// publish gate and the runtime fact consumer share, and threading a config value through the
// nine call sites — one of which (runtime.CompilePublishedRules) is a free function on the
// fact-consume path with no config in scope — is exactly how those two drift apart and a rule
// starts passing publish only to be refused on load. A zero value (never configured: every
// unit test, and any process that fails to call the setter) floors to defaultMaxRuleDuration
// inside withDefaults, so an unconfigured process is capped at a day, never uncapped.
var platformMaxRuleDuration atomic.Int64

// SetPlatformMaxRuleDuration installs the operator-configured maximum rule duration
// (maxRuleDurationSeconds). main calls it once during startup, before the GraphQL schema is
// served or the fact consumer binds, so every compile in the process sees the same ceiling.
// A non-positive value leaves the built-in day in force (ADR-023: never unlimited).
func SetPlatformMaxRuleDuration(d time.Duration) { platformMaxRuleDuration.Store(int64(d)) }

// WithDefaults returns the limits with every zero field floored to its built-in cap — the same
// resolution Compile applies internally, exported so a caller that must cost-gate against the
// EFFECTIVE ceiling before Compile runs (the canvas lowering gates unwired branch guards up front)
// resolves it from one place rather than re-deriving the floor and drifting.
func (l Limits) WithDefaults() Limits { return l.withDefaults() }

func (l Limits) withDefaults() Limits {
	if l.PredicateCostCeiling == 0 {
		l.PredicateCostCeiling = defaultPredicateCostCeiling
	}
	if l.DefaultCorrelationMemberCap <= 0 {
		// <= 0, not == 0: a negative caller misconfig must also floor, else every
		// default-cap correlation rule is rejected downstream with a confusing message.
		l.DefaultCorrelationMemberCap = defaultCorrelationMemberCapFloor
	}
	if l.MaxRuleDuration <= 0 {
		// <= 0 for the same reason as the member cap: a negative override is a caller
		// misconfiguration, and flooring it to the platform default is the ADR-023 fail-safe
		// (never unlimited). A negative CONFIGURED value is rejected earlier, at config Validate.
		l.MaxRuleDuration = defaultMaxRuleDuration
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
// TWO kinds now act on false: Duration (a non-matching event CANCELS the running hold) and,
// since ADR-057's two-edge model, Threshold (a non-matching event RESOLVES the raised alarm —
// engine.go apply). So a Duration/Threshold/Repeating rule keyed on a metric must be fed
// ONLY events that carry that metric; otherwise a device interleaving other telemetry
// (a battery reading between temperature readings) evaluates the leaf to false and, for
// Duration, resets the hold forever — and for Threshold, spuriously resolves the alarm
// (review D4). The compiler surfaces the relevance metric so the runtime can scope the feed
// without re-parsing:
//
//   - GateMetric set  → feed only events carrying GateMetric (structured threshold/
//     duration/repeating). The presence guard in the predicate is defense-in-depth.
//   - ValueMetric set → feed only events carrying ValueMetric (deltaRate, non-count
//     aggregate); the predicate's presence guard also enforces it.
//   - FeedMetrics set → feed only events carrying at least one referenced metric. This is the
//     review-D4 fix for a RAW-CEL threshold/duration leaf, which the structured lowering could
//     not scope (GateMetric would be empty). Without it a presence-guarded raw leaf
//     (`"temp" in m && …`) evaluates to a clean FALSE on an off-metric event — which for Duration
//     CANCELS the hold and for Threshold RESOLVES the alarm (ADR-057), so unrelated telemetry (a
//     battery reading between temperature readings) spuriously clears it. Deriving the leaf's
//     metric references from its compiled AST (predicate.MetricRefs) lets the runtime skip the
//     off-metric event entirely, preserving the level. It is set only for threshold/duration —
//     the kinds where one non-matching event immediately flips a per-series latch.
//   - all empty       → feed every in-scope event. Absence is deliberately here (every event is a
//     heartbeat, device-scoped not metric-scoped); so are match-every leaves, count aggregates,
//     and correlation. A raw-CEL threshold/duration leaf whose reference set is NOT statically
//     knowable (a dynamic key `m[expr]`, or a whole-map op like `size(m)`) also lands here — the
//     scope cannot be derived, so it falls back to feed-everything and the author owns totality;
//     the publish gate steering authors off raw-CEL threshold/duration remains a console concern
//     (slice 7).
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

	// FeedMetrics is the metric-scoped feed set derived from a RAW-CEL threshold/duration leaf
	// that the structured lowering could not scope (GateMetric would be empty). The runtime feeds
	// the rule only events carrying AT LEAST ONE of these measurements, so off-metric telemetry
	// never evaluates the leaf to the FALSE that resolves the threshold alarm / cancels the
	// duration hold (review D4). It is set from predicate.ScopableMetrics, which returns a set
	// ONLY when the leaf is PROVABLY FALSE without those metrics — so skipping a carrying-none
	// event drops no raise. It is empty (feed every event) when the leaf references no measurement,
	// references `m` opaquely (not statically knowable), or can be true without a measurement (via
	// attr/device/a disjunction) — the raw-CEL-author-owns-totality fallback.
	//
	// It is set ONLY for threshold and duration: those are the kinds where a single non-matching
	// event immediately flips a per-series latch, so an off-metric false is acutely wrong. The
	// windowed/counting kinds (repeating/aggregate/correlation) are left feed-everything: their
	// falling edge is window-granular, driven by off-metric traffic advancing eviction rather than
	// by an immediate non-match — a milder residual accepted here, steered by the console (slice 7).
	FeedMetrics []string

	// RequiresPosition is the POSITION-SCOPED FEED (ADR-078): true when the leaf calls the
	// geofence containment function, in which case the runtime feeds the rule only samples that
	// carry a reported position. It is the geofence sibling of FeedMetrics and exists for the same
	// hazard — a fence leaf evaluated against a positionless event would either claim the device
	// is outside (cancelling a Duration hold) or error on every routine measurement. Unlike
	// FeedMetrics it needs no soundness proof and applies to EVERY rule type: a leaf that calls
	// inFence cannot be true for an event with no position under any assignment, so a skipped
	// positionless event could never have raised. See predicate.ReferencesFences.
	RequiresPosition bool

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
	// Bound the rule's temporal extent BEFORE lowering, once, for every kind — see
	// checkDurationCeiling. A per-kind check would have to be remembered by each new kind,
	// and the per-kind lowerings below are exactly where it was forgotten.
	if err := checkDurationCeiling(r, limits.MaxRuleDuration); err != nil {
		return nil, err
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
	case TypeConnectivity:
		leafSrc, err = compileConnectivity(r, cr)
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

	// Metric-scope a RAW-CEL threshold/duration leaf the structured lowering left unscoped
	// (GateMetric empty). ScopableMetrics returns the metrics the runtime may safely gate the feed
	// on — the leaf's referenced measurements, but ONLY when the leaf is provably false without
	// them, so an off-metric event that would otherwise evaluate the leaf to the false that
	// resolves/cancels it (review D4) is skipped, and a leaf that could raise via attr/device/a
	// disjunction is left feed-everything (no dropped raise). A structured leaf already carries
	// GateMetric; every other kind observes its falling edge by aging, not an immediate non-match.
	if (cr.Type == TypeThreshold || cr.Type == TypeDuration) && cr.GateMetric == "" {
		if metrics, ok := pred.ScopableMetrics(); ok {
			cr.FeedMetrics = metrics
		}
	}

	// Position-scope a geofence leaf (ADR-078). Unconditional on rule type, unlike the metric
	// scope above: the argument for it is not "this kind flips a latch on one non-match" but "this
	// leaf cannot answer at all without a position", which is true for every kind.
	cr.RequiresPosition = pred.ReferencesFences()

	// ...and now that BOTH scopes are known, refuse the one combination that scopes the rule down
	// to nothing at all.
	if err := errFenceAndMetricLeaf(r, cr, pred); err != nil {
		return nil, err
	}

	// Validate the REACT layer (severity + action chain) and carry the severity through. This is
	// the same publish-time gate the detection fields pass: a malformed action or an alarm without
	// a tier is rejected here (fail-closed) so it can never reach the dispatcher.
	if err := validateReact(r, limits); err != nil {
		return nil, err
	}
	cr.Severity = r.Severity
	return cr, nil
}

// errFenceAndMetricLeaf refuses a rule whose leaf tests a GEOFENCE and a MEASUREMENT together,
// because the runtime can never feed it a single event.
//
// 🔴 THE TWO SCOPES INTERSECT AT THE EMPTY SET, AND NOTHING BELOW THIS POINT CAN NOTICE. The
// position scope feeds the leaf only samples reporting a position; the metric scope feeds it only
// samples carrying a measurement. Both are individually sound. But BuildInputs constructs the two
// kinds from disjoint payloads — a location entry sets Position and leaves M nil, a measurement
// entry sets M and leaves Position nil, and a heartbeat has neither — so no Input has ever
// satisfied both gates and none can. The rule then publishes clean, reports healthy, evaluates
// zero samples and raises nothing, which is indistinguishable from "the condition never happened".
// That is the worst failure this package has: a silent one that looks like a correct negative.
//
// Refusing at compile is the only place the distinction survives. At runtime the two skips are
// ordinary scope decisions on separate lines of the fan-out loop, taken against different events;
// nothing there holds both facts at once, so no counter and no log could be attributed to this.
//
// The test is ONE question — does the compiled leaf read `m` at all — and not an enumeration of
// the three metric-scope fields, because every one of them implies that read. FeedMetrics is
// derived from the leaf's own `m` references; a structured GateMetric lowers to a comparison on
// `m[metric]`; a ValueMetric lowers to the `metric in m` presence guard valueGuardedLeaf prepends.
// Asking the leaf covers all three plus the case none of them catch: a raw reference that earned
// no scope at all (`size(m) > 3`, a dynamic key). That last one is not fed nothing — it is fed
// location samples and errors on each, which the runtime counts as an eval error and skips. It is
// louder, but it is the same dead rule, and knowing at publish beats discovering from a counter.
//
// The gate fields are then read only to PHRASE the refusal, so an author is told which half of
// their rule is the measurement. Note there is no GateMetric case among them: a structured `when`
// and raw CEL are mutually exclusive (leafSource refuses both), and a fence call can only arrive
// as raw CEL, so a structured gate can never be the conflicting half. A branch for it would read
// like a guard and could never fire.
//
// What this deliberately does NOT refuse: a fence rule with no measurement (the ordinary case), or
// a measurement rule with no fence. A rule that wants both conditions is two rules, or one rule
// whose measurement condition is expressed as an attribute the location event also carries.
func errFenceAndMetricLeaf(r Rule, cr *CompiledRule, pred *predicate.Predicate) error {
	if !cr.RequiresPosition || !pred.ReferencesMetrics() {
		return nil
	}
	because := "the condition reads `m`"
	switch {
	case cr.ValueMetric != "":
		because = fmt.Sprintf("the rule reads its value from measurement %q", cr.ValueMetric)
	case len(cr.FeedMetrics) > 0:
		because = fmt.Sprintf("the condition reads measurement(s) %s", strings.Join(cr.FeedMetrics, ", "))
	}
	return invalid(r.ID, "when",
		"a condition cannot test a geofence and a measurement together: %s, and it also calls "+
			"geo.inFence(). A location event carries no measurements and a measurement event "+
			"carries no position, so no event could satisfy both and the rule would never fire. "+
			"Split it into a fence rule and a measurement rule", because)
}

// validateReact enforces the REACT contract on an authored rule (ADR-051 REACT stage): a bounded
// action chain, each action well-formed for its type, and a severity that is present when an alarm
// is raised and always within the known set. It is separate from the per-type detection lowering
// because actions are orthogonal to the detection shape — any rule type may carry any action.
func validateReact(r Rule, limits Limits) error {
	if r.Severity != "" && !r.Severity.Valid() {
		return invalid(r.ID, "severity", "unknown severity %q", r.Severity)
	}
	if len(r.Actions) > 0 && r.Type == TypeCorrelation {
		// Every REACT action targets the detection's DEVICE (raiseAlarm keys a device-originated
		// alarm; sendCommand enqueues to a device). For every rule kind the detection series IS the
		// device token — except correlation, whose series is the ANCHOR (area) token. Dispatching a
		// device action against an anchor is nonsensical, so an action chain on a correlation rule is
		// rejected at the gate rather than mis-targeted at dispatch. Area-level reactions are a future
		// concern with their own originator model (ADR-041).
		return invalid(r.ID, "actions", "a correlation rule cannot carry actions (its series is an area anchor, not a device)")
	}
	if len(r.Actions) > MaxActionsPerRule {
		return invalid(r.ID, "actions", "a rule may declare at most %d actions, got %d", MaxActionsPerRule, len(r.Actions))
	}
	seen := make(map[string]struct{}, len(r.Actions))
	for i, a := range r.Actions {
		if err := validateAction(r.ID, i, a); err != nil {
			return err
		}
		if a.Guard != "" {
			// Cost-gate the per-action guard at the SAME tenant ceiling as the leaf predicate (a guard
			// is another cost-bearing CEL expression, ADR-023). A parse/type error, non-boolean, or
			// over-cost guard rejects the rule at publish — fail-closed, so a runaway guard never
			// reaches the dispatcher's hot path. The guard env is the derived event's scalars, NOT the
			// resolved event's map (guard.go).
			if _, err := CompileGuard(a.Guard, limits.PredicateCostCeiling); err != nil {
				return invalid(r.ID, "actions", "action %d guard: %v", i, err)
			}
		}
		if a.Type == ActionRaiseAlarm && !r.Severity.Valid() {
			// A raiseAlarm action needs a tier to raise at; the tier lives on the rule (one rule,
			// one severity), so an alarm action without a valid rule severity is rejected.
			return invalid(r.ID, "severity", "a raiseAlarm action requires a valid rule severity")
		}
		// Cost-gate a connector action's CEL payload template at the SAME tenant ceiling as the leaf
		// predicate/guard (ADR-023): a bad or runaway template rejects the rule at publish, so REACT
		// renders only a proven template on its hot path. Empty ⇒ no body, nothing to compile.
		if tmpl := actionTemplate(a); tmpl != "" {
			if _, err := CompileTemplate(tmpl, limits.PredicateCostCeiling); err != nil {
				return invalid(r.ID, "actions", "action %d payload template: %v", i, err)
			}
		}
		// Reject an exact-duplicate action (fail-closed). The dispatcher keys idempotency on the
		// detection identity PLUS the action's CONTENT (this same key's shape), so two identical
		// actions collapse to ONE idempotency token and only one would ever dispatch — a silent
		// swallow. An author never means to declare the same action twice per detection, so a textual
		// duplicate is rejected here as a mistake rather than silently deduped downstream.
		key := ActionDedupKey(a)
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

// ActionDedupKey is a textual identity for exact-duplicate detection. Two sendCommand actions with
// the same command but different payloads are legitimately distinct (send two different commands),
// so the payload is part of the key; the payload is compared verbatim (it is not canonicalized —
// see SendCommandAction.Payload), so this catches identical authored bytes, the mistake case. It is
// exported as the canonical action identity so the REACT dispatcher's content-addressed idempotency
// token (react.actionContentKey) can be asserted to induce the SAME equivalence (the lockstep that
// keeps a redelivery deduping rather than double-dispatching); both share rules.MethodOrPost /
// rules.HeaderKey so the normalization cannot drift.
func ActionDedupKey(a Action) string {
	// The guard is part of the identity: two actions with the same target but different guards route
	// on different conditions (raise-if-hot vs raise-if-cold off one detection), so they are distinct,
	// not duplicates — and the dispatcher's content-addressed idempotency token must likewise separate
	// them (react.actionContentKey mirrors this) or one would swallow the other's dispatch. The guard
	// segment is appended only when non-empty, so an unguarded action's key is unchanged from pre-9c
	// (kept in lockstep with actionContentKey, whose token is durable).
	guardSeg := ""
	if a.Guard != "" {
		guardSeg = "|" + a.Guard
	}
	switch a.Type {
	case ActionRaiseAlarm:
		return "raiseAlarm|" + a.RaiseAlarm.AlarmKey + guardSeg
	case ActionSendCommand:
		return "sendCommand|" + a.SendCommand.Command + "|" + a.SendCommand.Payload + guardSeg
	case ActionHTTPCall:
		h := a.HTTPCall
		// URL + method + body template + secret handle + a stable header serialization identify the
		// dispatch. Method and header names are NORMALIZED (empty method ⇒ POST, names canonicalized)
		// so two actions that render the identical request are ONE identity — else they would pass the
		// duplicate gate yet get distinct idempotency tokens at dispatch and double-fire.
		// react.actionContentKey (slice C2b) calls the SAME exported MethodOrPost / HeaderKey so the
		// durable idempotency token can never drift from this gate's identity (one implementation, not
		// two mirrored copies).
		return "httpCall|" + h.URL + "|" + MethodOrPost(h.Method) + "|" + h.BodyTemplate + "|" + h.SecretRef + "|" + HeaderKey(h.Headers) + guardSeg
	case ActionPublish:
		p := a.Publish
		return "publish|" + p.ConnectorRef + "|" + p.PayloadTemplate + guardSeg
	default:
		return string(a.Type)
	}
}

// HeaderKey renders a header map into a stable, order-independent string for the action identity
// keys. Names are CANONICALIZED (http.CanonicalHeaderKey — the same form the wire uses) so a
// case-only difference is one identity; map iteration order is non-deterministic, so the canonical
// names are sorted; each name/value is length-prefixed so no set can collide with a different set.
// validateHTTPCall rejects two names that canonicalize to the same header, so the canonical names
// here are unique (no value is lost to a canonicalization clash). Exported so the REACT dispatcher's
// idempotency token (react.actionContentKey) shares this ONE normalization rather than a drift-prone copy.
func HeaderKey(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	names := make([]string, 0, len(h))
	canon := make(map[string]string, len(h))
	for k, v := range h {
		c := http.CanonicalHeaderKey(k)
		names = append(names, c)
		canon[c] = v
	}
	sort.Strings(names)
	var b strings.Builder
	for _, c := range names {
		fmt.Fprintf(&b, "%d:%s=%d:%s;", len(c), c, len(canon[c]), canon[c])
	}
	return b.String()
}

// MethodOrPost normalizes an httpCall method for the identity key: an empty method means POST (the
// dispatch default), so "" and "POST" resolve to one identity rather than two colliding dispatches.
// Exported so react.actionContentKey normalizes identically to this gate (shared, not mirrored).
func MethodOrPost(m string) string {
	if m == "" {
		return http.MethodPost
	}
	return m
}

// actionTemplate returns the CEL payload-template source of a connector action (empty for
// non-connector actions or an action with no template), the single field validateReact cost-gates.
func actionTemplate(a Action) string {
	switch a.Type {
	case ActionHTTPCall:
		if a.HTTPCall != nil {
			return a.HTTPCall.BodyTemplate
		}
	case ActionPublish:
		if a.Publish != nil {
			return a.Publish.PayloadTemplate
		}
	}
	return ""
}

// validateAction checks one action: its type is known, exactly the matching variant is populated,
// and that variant's fields are well-formed. The index anchors the error to the offending action
// for the console.
func validateAction(ruleID string, i int, a Action) error {
	// Known type first, for a clear message on a forged/typo'd type.
	switch a.Type {
	case ActionRaiseAlarm, ActionSendCommand, ActionHTTPCall, ActionPublish:
	default:
		return invalid(ruleID, "actions", "action %d has unknown type %q", i, a.Type)
	}
	// Exactly the variant matching the declared type must be populated — no missing, foreign, or
	// multiply-populated payload (fail-closed).
	if pv := a.populatedVariants(); len(pv) != 1 || pv[0] != a.Type {
		return invalid(ruleID, "actions", "action %d is type %q but its matching payload is missing or a foreign payload is set", i, a.Type)
	}
	switch a.Type {
	case ActionRaiseAlarm:
		if a.RaiseAlarm.AlarmKey != "" {
			if err := validateMetric(a.RaiseAlarm.AlarmKey); err != nil {
				return invalid(ruleID, "actions", "action %d alarmKey: %v", i, err)
			}
		}
	case ActionSendCommand:
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
	case ActionHTTPCall:
		return validateHTTPCall(ruleID, i, a.HTTPCall)
	case ActionPublish:
		return validatePublish(ruleID, i, a.Publish)
	}
	return nil
}

// validateHTTPCall checks an httpCall action's inline config (ADR-060). The CEL body template is
// cost-gated separately in validateReact (it needs the tenant ceiling); this checks the structural
// fields: an http/https URL, POST-only method, non-reserved headers, a well-formed secret handle,
// and a bounded timeout.
func validateHTTPCall(ruleID string, i int, h *HTTPCallAction) error {
	if _, err := httpsink.ValidateURL(h.URL); err != nil {
		return invalid(ruleID, "actions", "action %d httpCall url: %v", i, err)
	}
	if h.Method != "" && h.Method != http.MethodPost {
		return invalid(ruleID, "actions", "action %d httpCall method %q is not supported (POST only)", i, h.Method)
	}
	// Validate headers at publish so a malformed/reserved/colliding header is rejected here rather
	// than failing every dispatch (net/http would reject it at send time, post-detection). Two names
	// that canonicalize to the same header would render last-write-wins in nondeterministic map order
	// under one idempotency token, so a post-canonicalization duplicate is rejected too.
	seenHeader := make(map[string]struct{}, len(h.Headers))
	for k, v := range h.Headers {
		if httpsink.IsReservedHeader(k) {
			return invalid(ruleID, "actions", "action %d httpCall header %q is reserved (Authorization / X-DC-* are set by the sink)", i, k)
		}
		if err := httpsink.ValidateHeader(k, v); err != nil {
			return invalid(ruleID, "actions", "action %d httpCall %v", i, err)
		}
		canonical := http.CanonicalHeaderKey(k)
		if _, dup := seenHeader[canonical]; dup {
			return invalid(ruleID, "actions", "action %d httpCall has two headers that canonicalize to %q", i, canonical)
		}
		seenHeader[canonical] = struct{}{}
	}
	if err := validateSecretHandle(h.SecretRef); err != nil {
		return invalid(ruleID, "actions", "action %d httpCall secretRef: %v", i, err)
	}
	if err := validateTimeout(h.TimeoutMs); err != nil {
		return invalid(ruleID, "actions", "action %d httpCall %v", i, err)
	}
	return nil
}

// validatePublish checks a publish action (ADR-060): a grammar-valid connector token and a bounded
// timeout. The connector's EXISTENCE is not checked here (cross-service — deferred to slice C4, see
// PublishAction.ConnectorRef); the payload template is cost-gated in validateReact.
func validatePublish(ruleID string, i int, p *PublishAction) error {
	if p.ConnectorRef == "" {
		return invalid(ruleID, "actions", "action %d publish requires a connectorRef", i)
	}
	if err := validateMetric(p.ConnectorRef); err != nil {
		return invalid(ruleID, "actions", "action %d publish connectorRef: %v", i, err)
	}
	if err := validateTimeout(p.TimeoutMs); err != nil {
		return invalid(ruleID, "actions", "action %d publish %v", i, err)
	}
	return nil
}

// validateTimeout bounds a connector action's timeout: non-negative and at most MaxActionTimeoutMs
// (0 ⇒ the dispatcher default). An unbounded outbound wait is a self-DoS the publish gate refuses.
func validateTimeout(ms int) error {
	if ms < 0 || ms > MaxActionTimeoutMs {
		return fmt.Errorf("timeoutMs %d out of range [0,%d]", ms, MaxActionTimeoutMs)
	}
	return nil
}

// validateSecretHandle checks an authored secret handle (HTTPCallAction.SecretRef). Empty is
// allowed (an unauthenticated call). A handle is a core/secrets ref Name, which may contain '/',
// so it is not an ADR-042 token; it is validated as a bounded, safe-character path so it can never
// carry an injection or an unbounded value into the store lookup.
func validateSecretHandle(h string) error {
	if h == "" {
		return nil
	}
	if len(h) > maxSecretHandleLen {
		return fmt.Errorf("handle exceeds %d bytes", maxSecretHandleLen)
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '/' || r == '.') {
			return fmt.Errorf("handle contains an invalid character %q (allowed: alphanumeric - _ / .)", r)
		}
	}
	// Reject path-traversal shapes so the handle is a clean key regardless of the store backend a
	// future ADR-059 slice plugs in (filesystem/Vault paths): no leading slash, no empty '.' or '..'
	// segment. The default Postgres store matches Name exactly, so this is defense-in-depth.
	if strings.HasPrefix(h, "/") {
		return fmt.Errorf("handle must not start with '/'")
	}
	for _, seg := range strings.Split(h, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("handle has an empty or dot path segment")
		}
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

func compileConnectivity(r Rule, cr *CompiledRule) (string, error) {
	// A leaf-less, config-less edge trigger: the presence StateChange IS the signal, so every
	// authorable field is forbidden (a leaf would silently do nothing — the engine reads the typed
	// edge, never Match). It carries no CEL, but the pipeline still compiles matchAll below; the
	// leaf is never evaluated (planConnectivity builds the event directly, not via BuildEvent).
	if err := forbid(r, "connectivity", forbidden{leaf: true, value: true, window: true, hold: true, timeout: true, gap: true, count: true, rate: true, mode: true, agg: true, op: true, threshold: true, anchor: true, memberCap: true}); err != nil {
		return "", err
	}
	cr.Core.Kind = core.Connectivity
	return matchAll, nil
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
