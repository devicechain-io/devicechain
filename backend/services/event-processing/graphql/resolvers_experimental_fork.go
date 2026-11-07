// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/perturb"
	"github.com/devicechain-io/dc-event-processing/internal/preview"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// ExperimentalForkTimelineEnv turns the experimental experimentalForkTimeline query on. It is OFF
// unless this is exactly "true".
const ExperimentalForkTimelineEnv = "DC_EXPERIMENTAL_FORK_TIMELINE"

// errForkDisabled is what the experimental query answers while its flag is off.
var errForkDisabled = errors.New("experimentalForkTimeline is an experimental feature and is not enabled on this deployment (set " + ExperimentalForkTimelineEnv + "=true on the event-processing service)")

type experimentalPerturbationInput struct {
	Op     string
	Device *string
	Metric *string
	Offset *float64
	Scale  *float64
	Value  *float64
	From   *string
	To     *string
}

type experimentalForkTimelineInput struct {
	ProfileToken          string
	Start                 string
	End                   string
	Perturbations         []experimentalPerturbationInput
	ShiftToleranceSeconds *int32
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func parseOptTime(label string, p *string) (time.Time, error) {
	if p == nil || *p == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, *p)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s time: %v", label, err)
	}
	return t, nil
}

// specsFromInput converts and validates the wire perturbations.
func specsFromInput(in []experimentalPerturbationInput) ([]perturb.Spec, error) {
	if len(in) == 0 {
		return nil, errors.New("give at least one perturbation")
	}
	if len(in) > perturb.MaxSpecs {
		return nil, fmt.Errorf("%d perturbations, above the maximum of %d", len(in), perturb.MaxSpecs)
	}
	specs := make([]perturb.Spec, 0, len(in))
	for i, p := range in {
		from, err := parseOptTime("from", p.From)
		if err != nil {
			return nil, fmt.Errorf("perturbation %d: %w", i+1, err)
		}
		to, err := parseOptTime("to", p.To)
		if err != nil {
			return nil, fmt.Errorf("perturbation %d: %w", i+1, err)
		}
		s := perturb.Spec{Op: p.Op, Device: str(p.Device), Metric: str(p.Metric),
			Scale: p.Scale, Value: p.Value, From: from, To: to}
		if p.Offset != nil {
			s.Offset = *p.Offset
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("perturbation %d: %w", i+1, err)
		}
		specs = append(specs, s)
	}
	return specs, nil
}

// ExperimentalForkTimeline replays the profile's LIVE rules over recorded history twice, once as
// recorded and once through the given perturbations, and diffs the firings: a measured
// counterfactual under the exact rule set the tenant runs today.
//
// EXPERIMENTAL and read-only. It goes through preview.Run exactly as previewRule does (so it never
// publishes, snapshots or touches the live engine) with the same authority, per-tenant concurrency
// gate, window cap and time budget; the only difference is the opener it reads history through.
func (r *SchemaResolver) ExperimentalForkTimeline(ctx context.Context, args struct {
	Input experimentalForkTimelineInput
}) (*ForkTimelineResultResolver, error) {
	if !r.ForkTimelineEnabled {
		return nil, errForkDisabled
	}
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	if r.Profiles == nil || r.DetectRules == nil {
		return nil, fmt.Errorf("fork preview is unavailable: the profile and rule projections are not configured")
	}
	in := args.Input
	refuse := func(format string, a ...any) (*ForkTimelineResultResolver, error) {
		return &ForkTimelineResultResolver{ok: false, errMsg: fmt.Sprintf(format, a...)}, nil
	}

	specs, err := specsFromInput(in.Perturbations)
	if err != nil {
		return refuse("%v", err)
	}
	start, err := time.Parse(time.RFC3339, in.Start)
	if err != nil {
		return refuse("invalid start time: %v", err)
	}
	end, err := time.Parse(time.RFC3339, in.End)
	if err != nil {
		return refuse("invalid end time: %v", err)
	}
	if !end.After(start) {
		return refuse("the end of the window must be after its start")
	}
	var notes []string
	if end.Sub(start) > preview.DefaultMaxWindow {
		start = end.Add(-preview.DefaultMaxWindow)
		notes = append(notes, fmt.Sprintf("the window was capped to the most recent %s", preview.DefaultMaxWindow))
	}
	tol := perturb.DefaultShiftTolerance
	if in.ShiftToleranceSeconds != nil && *in.ShiftToleranceSeconds > 0 {
		tol = time.Duration(*in.ShiftToleranceSeconds) * time.Second
	}

	active, found, err := r.Profiles.Load(ctx, tenant, in.ProfileToken)
	if err != nil {
		return nil, err
	}
	if !found || active.ActiveVersionToken == "" {
		return refuse("this profile has no published version yet, so there are no live rules and no history to fork")
	}
	rows, err := r.DetectRules.LoadByProfileVersion(ctx, tenant, active.ActiveVersionToken)
	if err != nil {
		return nil, err
	}
	published := make([]dmmodel.PublishedDetectionRule, 0, len(rows))
	for _, row := range rows {
		published = append(published, dmmodel.PublishedDetectionRule{
			Token: row.RuleToken, Definition: row.Definition,
			EntityGroupToken: row.EntityGroupToken, EntityGroupVersion: row.EntityGroupVersion,
		})
	}
	scoped, failed := runtime.CompilePublishedRules(tenant, active.ActiveVersionToken, published)
	if failed > 0 {
		notes = append(notes, fmt.Sprintf("%d live rule(s) no longer compile and are not part of the fork, as in production", failed))
	}
	if len(scoped) == 0 {
		return refuse("this profile's active version has no live detection rules to replay")
	}
	// Geofence containment is a read of position however it is asked for (see previewRule).
	for _, s := range scoped {
		if s.Compiled.RequiresPosition {
			if err := auth.Authorize(ctx, auth.LocationRead); err != nil {
				return nil, err
			}
			if r.FenceSets == nil {
				return refuse("a live rule tests geofence containment, and the geofence archive is not reachable from this service")
			}
			break
		}
	}

	if !globalPreviewGate.acquire(tenant) {
		return refuse("the maximum number of previews are already running for this tenant; try again in a moment")
	}
	defer globalPreviewGate.release(tenant)

	var opener preview.ReplayOpener = r.Replay
	if opener == nil {
		opener = r.GetNats(ctx)
	}
	reality, err := perturb.Wrap(opener, tenant, nil)
	if err != nil {
		return refuse("%v", err)
	}
	forked, err := perturb.Wrap(opener, tenant, specs)
	if err != nil {
		return refuse("%v", err)
	}
	reg := runtime.NewRuleRegistry(scoped)
	runCtx, cancel := context.WithTimeout(ctx, previewBudget(ctx))
	defer cancel()
	window := preview.TimeRange{Start: start, End: end}
	runOpts := []preview.Option{preview.WithAppliedGuard(r.AppliedGuard)}
	// The two replays are independent reads of the same history; each gets fresh engine state.
	base, err := preview.Run(runCtx, reality, streams.ResolvedEvents, reg, tenant, active.ActiveVersionToken,
		window, 0, preview.DefaultMaxScan, preview.DefaultMaxRead, r.FenceSets, nil, runOpts...)
	if err != nil {
		return nil, err
	}
	fork, err := preview.Run(runCtx, forked, streams.ResolvedEvents, reg, tenant, active.ActiveVersionToken,
		window, 0, preview.DefaultMaxScan, preview.DefaultMaxRead, r.FenceSets, nil, runOpts...)
	if err != nil {
		return nil, err
	}
	if base.Degraded != "" {
		notes = append(notes, "reality: "+base.Degraded)
	}
	if fork.Degraded != "" {
		notes = append(notes, "fork: "+fork.Degraded)
	}
	return newForkResult(specs, len(scoped), base, fork, perturb.Diff(base.Firings, fork.Firings, tol), notes), nil
}

// ForkTimelineResultResolver resolves ExperimentalForkTimelineResult.
type ForkTimelineResultResolver struct {
	ok          bool
	errMsg      string
	degraded    string
	described   []string
	ruleCount   int32
	baseline    []*PreviewFiringResolver
	fork        []*PreviewFiringResolver
	divergences []*ExperimentalDivergenceResolver
}

func firingResolvers(fs []preview.Firing) []*PreviewFiringResolver {
	out := make([]*PreviewFiringResolver, 0, len(fs))
	for _, f := range fs {
		out = append(out, &PreviewFiringResolver{occurredAt: f.OccurredAt.UTC().Format(time.RFC3339Nano),
			series: f.Series, signal: signalName(f.Raise), trace: []*NodeTraceStepResolver{}})
	}
	return out
}

func signalName(raise bool) string {
	if raise {
		return "raised"
	}
	return "resolved"
}

func newForkResult(specs []perturb.Spec, rules int, base, fork preview.Result, div []perturb.Divergence, notes []string) *ForkTimelineResultResolver {
	out := &ForkTimelineResultResolver{ok: true, ruleCount: int32(rules),
		baseline: firingResolvers(base.Firings), fork: firingResolvers(fork.Firings),
		described: make([]string, 0, len(specs)), divergences: make([]*ExperimentalDivergenceResolver, 0, len(div))}
	for _, s := range specs {
		out.described = append(out.described, s.Describe())
	}
	for _, d := range div {
		out.divergences = append(out.divergences, &ExperimentalDivergenceResolver{d: d})
	}
	for i, n := range notes {
		if i > 0 {
			out.degraded += "; "
		}
		out.degraded += n
	}
	return out
}

func (r *ForkTimelineResultResolver) Ok() bool { return r.ok }
func (r *ForkTimelineResultResolver) Error() *string {
	if r.errMsg == "" {
		return nil
	}
	return &r.errMsg
}
func (r *ForkTimelineResultResolver) Degraded() *string {
	if r.degraded == "" {
		return nil
	}
	return &r.degraded
}
func (r *ForkTimelineResultResolver) Perturbations() []string {
	if r.described == nil {
		return []string{}
	}
	return r.described
}
func (r *ForkTimelineResultResolver) RuleCount() int32 { return r.ruleCount }
func (r *ForkTimelineResultResolver) Baseline() []*PreviewFiringResolver {
	if r.baseline == nil {
		return []*PreviewFiringResolver{}
	}
	return r.baseline
}
func (r *ForkTimelineResultResolver) Fork() []*PreviewFiringResolver {
	if r.fork == nil {
		return []*PreviewFiringResolver{}
	}
	return r.fork
}
func (r *ForkTimelineResultResolver) Divergence() []*ExperimentalDivergenceResolver {
	if r.divergences == nil {
		return []*ExperimentalDivergenceResolver{}
	}
	return r.divergences
}

// ExperimentalDivergenceResolver resolves ExperimentalDivergence.
type ExperimentalDivergenceResolver struct{ d perturb.Divergence }

func (r *ExperimentalDivergenceResolver) Kind() string   { return r.d.Kind }
func (r *ExperimentalDivergenceResolver) Series() string { return r.d.Series }
func (r *ExperimentalDivergenceResolver) Signal() string { return signalName(r.d.Raise) }
func (r *ExperimentalDivergenceResolver) At() string     { return r.d.At.UTC().Format(time.RFC3339Nano) }
func (r *ExperimentalDivergenceResolver) BaselineAt() *string {
	if r.d.Baseline.IsZero() {
		return nil
	}
	s := r.d.Baseline.UTC().Format(time.RFC3339Nano)
	return &s
}
func (r *ExperimentalDivergenceResolver) DeltaSeconds() float64 { return r.d.Delta.Seconds() }
