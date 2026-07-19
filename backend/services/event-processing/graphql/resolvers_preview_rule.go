// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
	tracepkg "github.com/devicechain-io/dc-event-processing/internal/preview/trace"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// maxConcurrentPreviewsPerTenant bounds how many replay previews one tenant may run at once (ADR-023):
// a preview is a bounded but non-trivial replay job, so "replay 24h per keystroke" must not be a
// self-inflicted DoS. Excess previews are refused with a degraded result rather than queued.
const maxConcurrentPreviewsPerTenant = 2

// previewGate is a tiny per-tenant concurrency limiter for previewRule.
type previewGate struct {
	mu    sync.Mutex
	inUse map[string]int
}

func (g *previewGate) acquire(tenant string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inUse[tenant] >= maxConcurrentPreviewsPerTenant {
		return false
	}
	g.inUse[tenant]++
	return true
}

func (g *previewGate) release(tenant string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inUse[tenant] > 0 {
		g.inUse[tenant]--
		if g.inUse[tenant] == 0 {
			delete(g.inUse, tenant)
		}
	}
}

var globalPreviewGate = &previewGate{inUse: map[string]int{}}

// previewRuleInput is the previewRule query input (a GraphQL input object). Exactly one of Graph (a
// CanvasDefinition JSON) or RuleDefinition (a rules.Rule JSON) names the DRAFT to preview.
type previewRuleInput struct {
	Graph          *string
	RuleDefinition *string
	ProfileToken   string
	Start          string // RFC3339 occurred-time window lower bound
	End            string // RFC3339 occurred-time window upper bound
	Trace          *bool  // when true (and a graph draft), attach a per-firing node trace (slice 9e)
}

// PreviewRule runs a DRAFT detection rule against replayed history and reports the RAISE/RESOLVE edges
// it WOULD have produced (ADR-053 slice 9d) — the "what would this rule have done?" headline. It builds
// a THROWAWAY in-memory DETECT core over an ephemeral replay consumer and collects edges instead of
// publishing, so it never perturbs the live singleton (see internal/preview for the isolation contract).
//
// It gates on device:read (a read-only replay over the profile's own history) and is tenant-scoped to
// the caller. A compile failure returns ok=false with node-anchored diagnostics (reusing the canvas
// diagnostic shape); an unpublished profile, an aged-out window, or the scan cap returns a degraded but
// non-error result — the console shows partial history rather than an error.
func (r *SchemaResolver) PreviewRule(ctx context.Context, args struct{ Input previewRuleInput }) (*PreviewResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	if r.Profiles == nil {
		return nil, fmt.Errorf("preview is unavailable: the profile projection is not configured")
	}
	in := args.Input

	// Compile the draft to a single compiled rule + its definition, through the SAME compiler + cost
	// gate the live path uses — so a preview runs exactly what a publish would run. tracePlan is the
	// canvas node-id map for the 9e node trace (nil for a ruleDefinition draft, which has no canvas).
	compiled, definition, tracePlan, cerr := r.compileDraft(in)
	if cerr != nil {
		return failedPreview(cerr...), nil
	}

	// Parse and bound the occurred-time window.
	start, err := time.Parse(time.RFC3339, in.Start)
	if err != nil {
		return failedPreview(graph.Diagnostic{Severity: "error", Message: fmt.Sprintf("invalid start time: %v", err)}), nil
	}
	end, err := time.Parse(time.RFC3339, in.End)
	if err != nil {
		return failedPreview(graph.Diagnostic{Severity: "error", Message: fmt.Sprintf("invalid end time: %v", err)}), nil
	}
	if !end.After(start) {
		return failedPreview(graph.Diagnostic{Severity: "error", Message: "the end of the window must be after its start"}), nil
	}
	var clampNote string
	if end.Sub(start) > preview.DefaultMaxWindow {
		start = end.Add(-preview.DefaultMaxWindow)
		clampNote = fmt.Sprintf("the window was capped to the most recent %s", preview.DefaultMaxWindow)
	}

	// Resolve the profile's active published version — the scope the draft previews against (the
	// events the currently-live rules saw). An unpublished profile has no history to preview against.
	active, found, err := r.Profiles.Load(ctx, tenant, in.ProfileToken)
	if err != nil {
		return nil, err
	}
	if !found || active.ActiveVersionToken == "" {
		return degradedPreview("this profile has no published version yet, so there is no history to preview against"), nil
	}

	// Per-tenant concurrency guard.
	if !globalPreviewGate.acquire(tenant) {
		return degradedPreview("the maximum number of previews are already running for this tenant; try again in a moment"), nil
	}
	defer globalPreviewGate.release(tenant)

	// Build a registry holding ONLY the draft rule, filed under the tenant + active version so the
	// live fan-out (Plan) selects it exactly as it would in production.
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{{
		Tenant:              tenant,
		ProfileVersionToken: active.ActiveVersionToken,
		Compiled:            compiled,
		Definition:          definition,
	}})

	startedAt := time.Now()
	res, err := preview.Run(ctx, r.GetNats(ctx), streams.ResolvedEvents, reg,
		tenant, active.ActiveVersionToken, preview.TimeRange{Start: start, End: end}, 0, preview.DefaultMaxScan, preview.DefaultMaxRead)
	if err != nil {
		return nil, err
	}
	wallMs := int32(time.Since(startedAt).Milliseconds())

	degraded := res.Degraded
	if clampNote != "" {
		if degraded != "" {
			degraded = clampNote + "; " + degraded
		} else {
			degraded = clampNote
		}
	}

	// Reconstruct the per-firing node trace (9e) when the request asked for it and the draft is a
	// canvas graph (a ruleDefinition draft has no node map). The builder compiles the plan's guards
	// once and reuses them across firings; the reconstruction is off the engine (see internal/preview/
	// trace), so it never re-runs or perturbs the replay.
	var tb *tracepkg.Builder
	if tracePlan != nil && in.Trace != nil && *in.Trace {
		tb = tracepkg.NewBuilder(*tracePlan)
	}
	return newPreviewResult(res, degraded, wallMs, tb), nil
}

// compileDraft lowers the draft (a canvas graph OR a rules.Rule definition) to one compiled rule and
// its canonical definition, returning node-anchored diagnostics on failure. Exactly one input source
// must be set. The returned *graph.NodeTracePlan is the canvas node-id map for the 9e node trace; it
// is nil for a ruleDefinition draft (which has no canvas nodes to attribute a firing back to).
func (r *SchemaResolver) compileDraft(in previewRuleInput) (*rules.CompiledRule, string, *graph.NodeTracePlan, []graph.Diagnostic) {
	hasGraph := in.Graph != nil && *in.Graph != ""
	hasDef := in.RuleDefinition != nil && *in.RuleDefinition != ""
	switch {
	case hasGraph == hasDef:
		return nil, "", nil, []graph.Diagnostic{{Severity: "error", Message: "provide exactly one of graph or ruleDefinition"}}
	case hasGraph:
		def, err := graph.Decode([]byte(*in.Graph))
		if err != nil {
			return nil, "", nil, []graph.Diagnostic{{Severity: "error", Message: err.Error()}}
		}
		res, cerr := graph.Compile(def, in.ProfileToken, rules.DefaultLimits())
		if cerr != nil {
			var ce *graph.CompileError
			if errors.As(cerr, &ce) {
				return nil, "", nil, ce.Diagnostics
			}
			return nil, "", nil, []graph.Diagnostic{{Severity: "error", Message: cerr.Error()}}
		}
		if len(res.Rules) != 1 {
			return nil, "", nil, []graph.Diagnostic{{Severity: "error", Message: "a canvas previews exactly one detection rule — this graph has more than one condition node"}}
		}
		lr := res.Rules[0]
		plan := lr.Trace
		return lr.Compiled, lr.Definition, &plan, nil
	default: // hasDef
		rule, err := rules.Decode([]byte(*in.RuleDefinition))
		if err != nil {
			return nil, "", nil, []graph.Diagnostic{{Severity: "error", Message: err.Error()}}
		}
		// Assign a stable preview id so the compiler (which requires a non-empty id) and the registry
		// (which requires uniqueness) are satisfied; the id never leaves the ephemeral engine.
		rule.ID = runtime.PublishedRuleID("preview", in.ProfileToken, "draft")
		compiled, err := rules.Compile(rule, rules.DefaultLimits())
		if err != nil {
			return nil, "", nil, []graph.Diagnostic{{Severity: "error", Message: err.Error()}}
		}
		return compiled, *in.RuleDefinition, nil, nil
	}
}

// ── Resolvers ──────────────────────────────────────────────────────────────

// PreviewResultResolver resolves a preview outcome.
type PreviewResultResolver struct {
	ok         bool
	firings    []*PreviewFiringResolver
	scanned    int32
	fireCnt    int32
	evalErrors int32
	wallMs     int32
	degraded   *string
	diags      []*CanvasDiagnosticResolver
}

// newPreviewResult maps a completed preview to its resolver. When tb is non-nil (the request asked
// for a trace on a canvas draft), each firing carries its reconstructed per-node trace.
func newPreviewResult(res preview.Result, degraded string, wallMs int32, tb *tracepkg.Builder) *PreviewResultResolver {
	firings := make([]*PreviewFiringResolver, 0, len(res.Firings))
	for _, f := range res.Firings {
		signal := "resolved"
		if f.Raise {
			signal = "raised"
		}
		fr := &PreviewFiringResolver{occurredAt: f.OccurredAt.UTC().Format(time.RFC3339), series: f.Series, signal: signal, trace: []*NodeTraceStepResolver{}}
		if tb != nil {
			steps := tb.Build(tracepkg.Firing{Raise: f.Raise, Series: f.Series, Value: f.Value, HasValue: f.HasValue})
			fr.trace = make([]*NodeTraceStepResolver, 0, len(steps))
			for _, s := range steps {
				fr.trace = append(fr.trace, newNodeTraceStep(s))
			}
		}
		firings = append(firings, fr)
	}
	out := &PreviewResultResolver{
		ok:         true,
		firings:    firings,
		scanned:    int32(res.Stats.EventsScanned),
		fireCnt:    int32(res.Stats.FiringCount),
		evalErrors: int32(res.Stats.EvalErrors),
		wallMs:     wallMs,
		diags:      []*CanvasDiagnosticResolver{},
	}
	if degraded != "" {
		out.degraded = &degraded
	}
	return out
}

// degradedPreview is a successful-but-empty preview carrying only a degraded reason (unpublished
// profile, concurrency refusal) — ok=true so the console shows the reason, not an error.
func degradedPreview(reason string) *PreviewResultResolver {
	return &PreviewResultResolver{ok: true, firings: []*PreviewFiringResolver{}, degraded: &reason, diags: []*CanvasDiagnosticResolver{}}
}

// failedPreview is an unsuccessful preview carrying compile diagnostics (ok=false).
func failedPreview(diags ...graph.Diagnostic) *PreviewResultResolver {
	rs := make([]*CanvasDiagnosticResolver, 0, len(diags))
	for _, d := range diags {
		rs = append(rs, newCanvasDiagnostic(d))
	}
	return &PreviewResultResolver{ok: false, firings: []*PreviewFiringResolver{}, diags: rs}
}

func (r *PreviewResultResolver) Ok() bool                          { return r.ok }
func (r *PreviewResultResolver) Firings() []*PreviewFiringResolver { return r.firings }
func (r *PreviewResultResolver) Stats() *PreviewStatsResolver {
	return &PreviewStatsResolver{scanned: r.scanned, fireCnt: r.fireCnt, evalErrors: r.evalErrors, wallMs: r.wallMs}
}
func (r *PreviewResultResolver) Degraded() *string                        { return r.degraded }
func (r *PreviewResultResolver) Diagnostics() []*CanvasDiagnosticResolver { return r.diags }

// PreviewFiringResolver resolves one firing on the timeline.
type PreviewFiringResolver struct {
	occurredAt string
	series     string
	signal     string
	trace      []*NodeTraceStepResolver // empty unless the request asked for a trace on a canvas draft
}

func (r *PreviewFiringResolver) OccurredAt() string              { return r.occurredAt }
func (r *PreviewFiringResolver) Series() string                  { return r.series }
func (r *PreviewFiringResolver) Signal() string                  { return r.signal }
func (r *PreviewFiringResolver) Trace() []*NodeTraceStepResolver { return r.trace }

// NodeTraceStepResolver resolves one canvas node's disposition for a firing (slice 9e node trace).
type NodeTraceStepResolver struct {
	nodeID      string
	kind        string
	disposition string
	detail      *string
}

func newNodeTraceStep(s tracepkg.Step) *NodeTraceStepResolver {
	r := &NodeTraceStepResolver{nodeID: s.NodeID, kind: s.Kind, disposition: s.Disposition}
	if s.Detail != "" {
		d := s.Detail
		r.detail = &d
	}
	return r
}

func (r *NodeTraceStepResolver) NodeId() string      { return r.nodeID }
func (r *NodeTraceStepResolver) Kind() string        { return r.kind }
func (r *NodeTraceStepResolver) Disposition() string { return r.disposition }
func (r *NodeTraceStepResolver) Detail() *string     { return r.detail }

// PreviewStatsResolver resolves the coverage counters.
type PreviewStatsResolver struct {
	scanned    int32
	fireCnt    int32
	evalErrors int32
	wallMs     int32
}

func (r *PreviewStatsResolver) EventsScanned() int32 { return r.scanned }
func (r *PreviewStatsResolver) FiringCount() int32   { return r.fireCnt }
func (r *PreviewStatsResolver) EvalErrors() int32    { return r.evalErrors }
func (r *PreviewStatsResolver) WallMs() int32        { return r.wallMs }
