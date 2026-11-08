// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// evaluateRulesInput is the evaluateRules query input (EvaluateRulesInput).
type evaluateRulesInput struct {
	ProfileToken   string
	RuleSet        string
	Graph          *string
	RuleDefinition *string
	Events         []evaluateEventInput
	AdvanceTo      *string
	Limits         *evaluateLimitsInput
}

type evaluateEventInput struct {
	DeviceToken  string
	OccurredAt   string
	Measurements []evaluateMeasurementInput
}

type evaluateMeasurementInput struct {
	Name  string
	Value float64
}

type evaluateLimitsInput struct {
	MaxEvents  *int32
	MaxIntents *int32
	MaxEdges   *int32
}

const (
	ruleSetPublished = "PUBLISHED"
	ruleSetDraft     = "DRAFT"

	// draftRuleToken names a ruleDefinition draft in the result.
	draftRuleToken = "draft"
)

// Refusal reasons (EvaluateRefusalReason). Each is also the code of the refusal's diagnostic, so a
// client that reads only diagnostics can still tell them apart.
const (
	refusalBusy            = "BUSY"
	refusalTimeout         = "TIMEOUT"
	refusalLimitExceeded   = "LIMIT_EXCEEDED"
	refusalInvalidInput    = "INVALID_INPUT"
	refusalRuleRejected    = "RULE_REJECTED"
	refusalNoPublished     = "NO_PUBLISHED_VERSION"
	refusalNoRules         = "NO_RULES"
	refusalAllRulesSkipped = "ALL_RULES_SKIPPED"

	skipReasonGroupScoped   = "GROUP_SCOPED"
	skipReasonCorrelation   = "CORRELATION"
	diagnosticMetricMissing = "METRIC_NOT_SUPPLIED"

	// maxConcurrentEvaluations bounds the evaluations running in this process, all tenants together.
	maxConcurrentEvaluations = 3
)

// evaluationSlots bounds the evaluations running in this PROCESS, whatever their tenants. The
// per-tenant preview gate keeps one tenant from taking every slot; this keeps many tenants together
// from taking the CPU live DETECT runs on. A request that finds no slot is refused as BUSY at once
// rather than queued, so a burst cannot pile up goroutines behind it.
var evaluationSlots = make(chan struct{}, maxConcurrentEvaluations)

func acquireEvaluationSlot() bool {
	select {
	case evaluationSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseEvaluationSlot() { <-evaluationSlots }

// EvaluateRules runs a profile's rules over the events in the request and reports the edges they
// derive and the REACT intents those edges would request, executing none of them. It is the
// engine behind `dcctl test`: the rule unit test the replay preview cannot be, because a preview
// replays whatever history happens to be in the stream and a test supplies its own.
//
// It reuses the preview's whole envelope (internal/evaluate shares the detection core with
// internal/preview): the same per-tenant concurrency gate, the same time budget, the same
// authorization, the same tenant scope taken from the request's context. What differs is the
// input (events in the request, not a stream) and the clock (an exact advance to a named instant,
// not a window ending now), and the bound (a size cap, not 24 hours).
//
// 🔴 A RUN THAT EVALUATED NOTHING IS NEVER A SUCCESS. Every way of evaluating nothing (no rule, no
// event, no free slot, out of time) is a refusal with ok false and a typed reason, because an empty
// success reports no edges and every neverRaised expectation would pass on it.
//
// 🔴 THE PUBLISHED RULES ARE READ FROM THE TENANT'S OWN ROWS UNDER THE CALLER'S TENANT SCOPE.
// DetectRuleStore.LoadByProfileVersion is a tenant-scoped read like every other, so a caller
// cannot evaluate, and so cannot learn the definitions of, another tenant's rules by naming
// their profile.
func (r *SchemaResolver) EvaluateRules(ctx context.Context, args struct{ Input evaluateRulesInput }) (*EvaluateRulesResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	in := args.Input

	// Cheap structural checks first: they refuse a malformed request before any rule is read or
	// compiled.
	if in.ProfileToken == "" {
		return refused(refusalInvalidInput, "a profileToken is required"), nil
	}
	events, advanceTo, diag := parseEvaluationEvents(in)
	if diag != "" {
		return refused(refusalInvalidInput, diag), nil
	}

	var (
		ruleSet  []evaluate.Rule
		version  string
		skipped  []skippedRule
		warnings []graph.Diagnostic
	)
	switch in.RuleSet {
	case ruleSetDraft:
		var diags []graph.Diagnostic
		ruleSet, warnings, diags = draftEvaluationRules(in)
		if len(diags) > 0 {
			return refusedDiags(refusalRuleRejected, diags...), nil
		}
		// A draft is evaluated over events the request supplies, so the profile need not be
		// published: the version only has to be the one the events are stamped with.
		version = in.ProfileToken + "@draft"
	case ruleSetPublished:
		if in.Graph != nil && *in.Graph != "" || in.RuleDefinition != nil && *in.RuleDefinition != "" {
			return refused(refusalInvalidInput, "graph and ruleDefinition are for ruleSet DRAFT; to evaluate a draft, set ruleSet to DRAFT"), nil
		}
		if r.Profiles == nil || r.DetectRules == nil {
			return nil, fmt.Errorf("evaluation of published rules is unavailable: the rule projection is not configured")
		}
		active, found, err := r.Profiles.Load(ctx, tenant, in.ProfileToken)
		if err != nil {
			return nil, err
		}
		if !found || active.ActiveVersionToken == "" {
			return refused(refusalNoPublished, fmt.Sprintf("profile %q has no published version, so it has no published rules; "+
				"evaluate a draft with ruleSet DRAFT, or publish the profile", in.ProfileToken)), nil
		}
		version = active.ActiveVersionToken
		rows, err := r.DetectRules.LoadByProfileVersion(ctx, tenant, version)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.EntityGroupToken != "" {
				group := fmt.Sprintf("%s@%d", row.EntityGroupToken, row.EntityGroupVersion)
				skipped = append(skipped, skippedRule{rule: row.RuleToken, reason: skipReasonGroupScoped, group: group,
					message: fmt.Sprintf("%s: scoped to the group %s, and an evaluated event carries no group memberships", row.RuleToken, group)})
				continue
			}
			def, err := rules.Decode([]byte(row.Definition))
			if err != nil {
				return refused(refusalRuleRejected, fmt.Sprintf("published rule %q does not decode: %v", row.RuleToken, err)), nil
			}
			if def.Type == rules.TypeCorrelation {
				skipped = append(skipped, skippedRule{rule: row.RuleToken, reason: skipReasonCorrelation,
					message: fmt.Sprintf("%s: correlates devices under an anchor, and an evaluated event carries no anchor memberships", row.RuleToken)})
				continue
			}
			ruleSet = append(ruleSet, evaluate.Rule{Token: row.RuleToken, Def: def})
		}
		if len(ruleSet) == 0 {
			res := refused(refusalNoRules, fmt.Sprintf("profile %q has no published rules to evaluate", in.ProfileToken))
			if len(skipped) > 0 {
				res = refused(refusalAllRulesSkipped, fmt.Sprintf("every published rule of profile %q was skipped", in.ProfileToken))
			}
			res.skipped = skipped
			return res, nil
		}
	default:
		return refused(refusalInvalidInput, fmt.Sprintf("ruleSet must be PUBLISHED or DRAFT, not %q", in.RuleSet)), nil
	}

	params := evaluate.Params{
		Tenant:         tenant,
		ProfileVersion: version,
		Rules:          ruleSet,
		Events:         events,
		AdvanceTo:      advanceTo,
		AppliedGuard:   r.AppliedGuard,
		Lateness:       r.EvaluationLateness,
	}
	if in.Limits != nil {
		for _, l := range []struct {
			in  *int32
			out *int
		}{{in.Limits.MaxEvents, &params.MaxEvents}, {in.Limits.MaxIntents, &params.MaxIntents}, {in.Limits.MaxEdges, &params.MaxEdges}} {
			if l.in == nil {
				continue
			}
			if *l.in < 0 {
				return refused(refusalInvalidInput, "limits must not be negative"), nil
			}
			*l.out = int(*l.in)
		}
	}

	// The preview's per-tenant concurrency gate, shared on purpose: an evaluation and a preview are
	// both bounded replays of the same detection core, and a tenant's allowance of that work is one
	// number, not one per door. Then the process-wide evaluation bound.
	const busy = "the maximum number of previews and evaluations are already running; try again in a moment"
	if !globalPreviewGate.acquire(tenant) {
		res := refused(refusalBusy, busy)
		res.skipped = skipped
		return res, nil
	}
	defer globalPreviewGate.release(tenant)
	if !acquireEvaluationSlot() {
		res := refused(refusalBusy, busy)
		res.skipped = skipped
		return res, nil
	}
	defer releaseEvaluationSlot()

	runCtx, cancel := context.WithTimeout(ctx, previewBudget(ctx))
	defer cancel()
	startedAt := time.Now()
	out, err := evaluate.Run(runCtx, params)
	if err != nil {
		var ruleErr *evaluate.RuleError
		var limitErr *evaluate.LimitError
		var inputErr *evaluate.InputError
		switch {
		case errors.As(err, &ruleErr):
			return refusedDiags(refusalRuleRejected, graph.Diagnostic{Severity: "error", Message: ruleErr.Error()}), nil
		case errors.As(err, &limitErr):
			return refused(refusalLimitExceeded, err.Error()), nil
		case errors.As(err, &inputErr):
			return refused(refusalInvalidInput, err.Error()), nil
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			// Our own budget ran out, not the caller's: a result that stops partway is not an
			// answer a test can judge, so this is a refusal with the way out in it.
			return refused(refusalTimeout, "the evaluation ran out of time before it finished; send fewer events"), nil
		}
		log.Warn().Err(err).Str("profile", in.ProfileToken).Msg("Rule evaluation failed")
		return nil, err
	}
	res := newEvaluationResult(out, int32(time.Since(startedAt).Milliseconds()))
	res.skipped = skipped
	for _, d := range warnings {
		res.diags = append(res.diags, newCanvasDiagnostic(d))
	}
	for _, w := range out.Warnings {
		res.diags = append(res.diags, newCanvasDiagnostic(graph.Diagnostic{Severity: "warning", Code: diagnosticMetricMissing, Message: w}))
	}
	return res, nil
}

// parseEvaluationEvents turns the request's events into the evaluator's, with the first problem as a
// message ("" when there is none).
func parseEvaluationEvents(in evaluateRulesInput) ([]evaluate.Event, time.Time, string) {
	if len(in.Events) == 0 {
		return nil, time.Time{}, "no events were sent: an evaluation of no events can never fail an expectation"
	}
	if len(in.Events) > evaluate.HardMaxEvents {
		return nil, time.Time{}, fmt.Sprintf("%d events were sent, above the maximum of %d", len(in.Events), evaluate.HardMaxEvents)
	}
	events := make([]evaluate.Event, 0, len(in.Events))
	for i, e := range in.Events {
		at, err := time.Parse(time.RFC3339Nano, e.OccurredAt)
		if err != nil {
			return nil, time.Time{}, fmt.Sprintf("events[%d]: invalid occurredAt: %v", i, err)
		}
		ev := evaluate.Event{Device: e.DeviceToken, At: at.UTC(), Metrics: make([]evaluate.Metric, len(e.Measurements))}
		for j, m := range e.Measurements {
			ev.Metrics[j] = evaluate.Metric{Name: m.Name, Value: m.Value}
		}
		events = append(events, ev)
	}
	var advanceTo time.Time
	if in.AdvanceTo != nil && *in.AdvanceTo != "" {
		t, err := time.Parse(time.RFC3339Nano, *in.AdvanceTo)
		if err != nil {
			return nil, time.Time{}, fmt.Sprintf("invalid advanceTo: %v", err)
		}
		advanceTo = t.UTC()
	}
	return events, advanceTo, ""
}

// draftEvaluationRules decodes the draft the request carries into rules to evaluate, through the same
// canvas lowering and rule decoder the preview and the publish gate use. Rules are compiled by
// evaluate.Run under the production limits, so a draft that could not be published is refused here
// with the same message.
func draftEvaluationRules(in evaluateRulesInput) ([]evaluate.Rule, []graph.Diagnostic, []graph.Diagnostic) {
	hasGraph := in.Graph != nil && *in.Graph != ""
	hasDef := in.RuleDefinition != nil && *in.RuleDefinition != ""
	switch {
	case hasGraph == hasDef:
		return nil, nil, []graph.Diagnostic{{Severity: "error", Message: "provide exactly one of graph and ruleDefinition for a draft"}}
	case hasGraph:
		def, err := graph.Decode([]byte(*in.Graph))
		if err != nil {
			return nil, nil, []graph.Diagnostic{{Severity: "error", Message: err.Error()}}
		}
		res, cerr := graph.Compile(def, in.ProfileToken, rules.DefaultLimits())
		if cerr != nil {
			var ce *graph.CompileError
			if errors.As(cerr, &ce) {
				return nil, nil, ce.Diagnostics
			}
			return nil, nil, []graph.Diagnostic{{Severity: "error", Message: cerr.Error()}}
		}
		if len(res.Rules) == 0 {
			return nil, nil, []graph.Diagnostic{{Severity: "error", Message: "the graph holds no detection rule to evaluate"}}
		}
		out := make([]evaluate.Rule, 0, len(res.Rules))
		var warns []graph.Diagnostic
		for _, lr := range res.Rules {
			out = append(out, evaluate.Rule{Token: lr.NodeID, Def: lr.Rule})
			warns = append(warns, graph.WarningDiagnostics(lr.Trace.ConditionID, lr.Compiled.Warnings)...)
		}
		return out, warns, nil
	default:
		def, err := rules.Decode([]byte(*in.RuleDefinition))
		if err != nil {
			return nil, nil, []graph.Diagnostic{{Severity: "error", Message: err.Error()}}
		}
		return []evaluate.Rule{{Token: draftRuleToken, Def: def}}, nil, nil
	}
}

// ── Resolvers ──────────────────────────────────────────────────────────────

// skippedRule is a published rule the evaluation did not run.
type skippedRule struct {
	rule, reason, group, message string
}

// SkippedRuleResolver resolves one SkippedRule.
type SkippedRuleResolver struct{ s skippedRule }

func (r *SkippedRuleResolver) Rule() string    { return r.s.rule }
func (r *SkippedRuleResolver) Reason() string  { return r.s.reason }
func (r *SkippedRuleResolver) Group() *string  { return strPtr(r.s.group) }
func (r *SkippedRuleResolver) Message() string { return r.s.message }

// EvaluateDegradationResolver resolves one reason a result is partial.
type EvaluateDegradationResolver struct{ d evaluate.Degradation }

func (r *EvaluateDegradationResolver) Reason() string { return r.d.Reason }
func (r *EvaluateDegradationResolver) Count() int32   { return int32(r.d.Count) }

// EvaluateRulesResultResolver resolves an evaluation outcome.
type EvaluateRulesResultResolver struct {
	ok           bool
	refusal      *string
	evaluated    []string
	edges        []*EvaluatedEdgeResolver
	intents      []*EvaluatedIntentResolver
	stats        EvaluateStatsResolver
	skipped      []skippedRule
	degradations []evaluate.Degradation
	degraded     *string
	diags        []*CanvasDiagnosticResolver
}

func newEvaluationResult(out evaluate.Result, wallMs int32) *EvaluateRulesResultResolver {
	res := &EvaluateRulesResultResolver{
		ok:           true,
		evaluated:    append([]string{}, out.Evaluated...),
		edges:        make([]*EvaluatedEdgeResolver, 0, len(out.Edges)),
		intents:      make([]*EvaluatedIntentResolver, 0, len(out.Intents)),
		degradations: append([]evaluate.Degradation{}, out.Degradations...),
		diags:        []*CanvasDiagnosticResolver{},
		stats: EvaluateStatsResolver{
			eventsFed: int32(out.Stats.EventsFed), eventsDropped: int32(out.Stats.EventsDropped),
			evalErrors: int32(out.Stats.EvalErrors), lateReadings: int32(out.Stats.LateReadings),
			rulesEvaluated: int32(out.Stats.RulesEvaluated), edgeCount: int32(len(out.Edges)),
			intentCount: int32(len(out.Intents)), wallMs: wallMs,
		},
	}
	for _, e := range out.Edges {
		res.edges = append(res.edges, &EvaluatedEdgeResolver{e: e})
	}
	for _, in := range out.Intents {
		res.intents = append(res.intents, &EvaluatedIntentResolver{in: in})
	}
	if out.Degraded != "" {
		d := out.Degraded
		res.degraded = &d
	}
	return res
}

// refused is an evaluation that did not run, with its reason and the message that says why.
func refused(reason, msg string) *EvaluateRulesResultResolver {
	return refusedDiags(reason, graph.Diagnostic{Severity: "error", Message: msg})
}

func refusedDiags(reason string, diags ...graph.Diagnostic) *EvaluateRulesResultResolver {
	res := newEvaluationResult(evaluate.Result{}, 0)
	res.ok = false
	res.refusal = &reason
	for _, d := range diags {
		if d.Code == "" {
			d.Code = reason
		}
		res.diags = append(res.diags, newCanvasDiagnostic(d))
	}
	return res
}

func (r *EvaluateRulesResultResolver) Ok() bool                            { return r.ok }
func (r *EvaluateRulesResultResolver) Refusal() *string                    { return r.refusal }
func (r *EvaluateRulesResultResolver) EvaluatedRules() []string            { return r.evaluated }
func (r *EvaluateRulesResultResolver) Edges() []*EvaluatedEdgeResolver     { return r.edges }
func (r *EvaluateRulesResultResolver) Intents() []*EvaluatedIntentResolver { return r.intents }
func (r *EvaluateRulesResultResolver) Stats() *EvaluateStatsResolver       { return &r.stats }
func (r *EvaluateRulesResultResolver) SkippedRules() []*SkippedRuleResolver {
	out := make([]*SkippedRuleResolver, len(r.skipped))
	for i, s := range r.skipped {
		out[i] = &SkippedRuleResolver{s: s}
	}
	return out
}
func (r *EvaluateRulesResultResolver) Degradations() []*EvaluateDegradationResolver {
	out := make([]*EvaluateDegradationResolver, len(r.degradations))
	for i, d := range r.degradations {
		out[i] = &EvaluateDegradationResolver{d: d}
	}
	return out
}
func (r *EvaluateRulesResultResolver) Degraded() *string                        { return r.degraded }
func (r *EvaluateRulesResultResolver) Diagnostics() []*CanvasDiagnosticResolver { return r.diags }

// signalOf maps an edge's direction to the EvaluatedSignal enum.
func signalOf(raise bool) string {
	if raise {
		return "RAISED"
	}
	return "RESOLVED"
}

// evaluatedIntentKinds maps the evaluator's intent kinds to the EvaluatedIntentKind enum.
var evaluatedIntentKinds = map[string]string{
	evaluate.KindRaiseAlarm:  "RAISE_ALARM",
	evaluate.KindClearAlarm:  "CLEAR_ALARM",
	evaluate.KindSendCommand: "SEND_COMMAND",
	evaluate.KindHTTPCall:    "HTTP_CALL",
	evaluate.KindPublish:     "PUBLISH",
}

func evalTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EvaluatedEdgeResolver resolves one derived edge.
type EvaluatedEdgeResolver struct{ e evaluate.Edge }

func (r *EvaluatedEdgeResolver) OccurredAt() string { return evalTime(r.e.At) }
func (r *EvaluatedEdgeResolver) Series() string     { return r.e.Series }
func (r *EvaluatedEdgeResolver) Rule() string       { return r.e.RuleToken }
func (r *EvaluatedEdgeResolver) Kind() string       { return r.e.Kind }
func (r *EvaluatedEdgeResolver) Severity() *string  { return strPtr(r.e.Severity) }
func (r *EvaluatedEdgeResolver) Signal() string     { return signalOf(r.e.Raise) }
func (r *EvaluatedEdgeResolver) Value() *float64 {
	if !r.e.HasValue {
		return nil
	}
	v := r.e.Value
	return &v
}

// EvaluatedIntentResolver resolves one requested side effect.
type EvaluatedIntentResolver struct{ in evaluate.Intent }

func (r *EvaluatedIntentResolver) OccurredAt() string { return evalTime(r.in.At) }
func (r *EvaluatedIntentResolver) Series() string     { return r.in.Series }
func (r *EvaluatedIntentResolver) Rule() string       { return r.in.RuleToken }
func (r *EvaluatedIntentResolver) Signal() string     { return signalOf(r.in.Raise) }

// Kind is the EvaluatedIntentKind. An intent kind the map does not know is a build defect (a new
// REACT action with no enum value); it is reported as a GraphQL error rather than mislabelled.
func (r *EvaluatedIntentResolver) Kind() (string, error) {
	k, ok := evaluatedIntentKinds[r.in.Kind]
	if !ok {
		return "", fmt.Errorf("intent kind %q has no EvaluatedIntentKind value", r.in.Kind)
	}
	return k, nil
}
func (r *EvaluatedIntentResolver) Token() string     { return r.in.Token }
func (r *EvaluatedIntentResolver) AlarmKey() *string { return strPtr(r.in.AlarmKey) }
func (r *EvaluatedIntentResolver) Severity() *string { return strPtr(r.in.Severity) }
func (r *EvaluatedIntentResolver) Value() *float64   { return r.in.Value }
func (r *EvaluatedIntentResolver) Command() *string  { return strPtr(r.in.Command) }
func (r *EvaluatedIntentResolver) Payload() *string {
	if r.in.Kind != evaluate.KindSendCommand {
		return nil
	}
	// An authored no-argument command has an empty payload; report the empty object it means, so
	// a consumer always reads JSON.
	if r.in.Payload == "" {
		p := "{}"
		return &p
	}
	return &r.in.Payload
}
func (r *EvaluatedIntentResolver) ExpiresAt() *string {
	if r.in.ExpiresAt == nil {
		return nil
	}
	s := evalTime(*r.in.ExpiresAt)
	return &s
}
func (r *EvaluatedIntentResolver) Connector() *string { return strPtr(r.in.Connector) }
func (r *EvaluatedIntentResolver) Url() *string       { return strPtr(r.in.URL) }
func (r *EvaluatedIntentResolver) Method() *string    { return strPtr(r.in.Method) }
func (r *EvaluatedIntentResolver) Body() *string {
	if r.in.Kind != evaluate.KindHTTPCall && r.in.Kind != evaluate.KindPublish {
		return nil
	}
	return &r.in.Body
}
func (r *EvaluatedIntentResolver) TimeoutMs() *int32 {
	if r.in.Kind != evaluate.KindHTTPCall && r.in.Kind != evaluate.KindPublish {
		return nil
	}
	t := int32(r.in.TimeoutMs)
	return &t
}

// EvaluateStatsResolver resolves the counters.
type EvaluateStatsResolver struct {
	eventsFed, eventsDropped, evalErrors, lateReadings int32
	rulesEvaluated, edgeCount, intentCount, wallMs     int32
}

func (r *EvaluateStatsResolver) EventsFed() int32      { return r.eventsFed }
func (r *EvaluateStatsResolver) EventsDropped() int32  { return r.eventsDropped }
func (r *EvaluateStatsResolver) EvalErrors() int32     { return r.evalErrors }
func (r *EvaluateStatsResolver) LateReadings() int32   { return r.lateReadings }
func (r *EvaluateStatsResolver) RulesEvaluated() int32 { return r.rulesEvaluated }
func (r *EvaluateStatsResolver) EdgeCount() int32      { return r.edgeCount }
func (r *EvaluateStatsResolver) IntentCount() int32    { return r.intentCount }
func (r *EvaluateStatsResolver) WallMs() int32         { return r.wallMs }
