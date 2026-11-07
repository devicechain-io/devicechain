// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/physpr"
	"github.com/devicechain-io/dc-event-processing/internal/preview"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/calcfield"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// ExperimentalPhysicalPREnv is the environment variable that turns the experimental physical-PR
// operations on. It is OFF unless this is exactly "true": an experiment is never reachable by
// accident, and with it off both operations refuse before reading a request field.
const ExperimentalPhysicalPREnv = "DC_EXPERIMENTAL_PHYSICAL_PR"

// errPhysicalPRDisabled is what both operations answer while the flag is off.
var errPhysicalPRDisabled = errors.New("experimental physical pull requests are not enabled on this deployment " +
	"(set " + ExperimentalPhysicalPREnv + "=true on the event-processing service)")

// maxBisectSlack bounds how far either side of the moment in question an alarm still counts, so the
// replay window a bisect asks for stays near the preview's own cap.
const maxBisectSlack = time.Hour

// maxShiftWindowSeconds bounds the caller-supplied shift window to the replay cap.
const maxShiftWindowSeconds = int32(24 * 60 * 60)

// experimentalRuleSide is one side of a comparison (ExperimentalRuleSideInput): exactly one of
// Graph and RuleDefinition, plus the draft profile metrics that side is replayed under.
type experimentalRuleSide struct {
	Label          *string
	Author         *string
	Graph          *string
	RuleDefinition *string
	DraftMetrics   *[]previewMetricInput
}

type experimentalRuleDiffInput struct {
	ProfileToken       string
	Base               experimentalRuleSide
	Head               experimentalRuleSide
	Start              string
	End                string
	ShiftWindowSeconds *int32
}

type experimentalRuleVersion struct {
	Commit         string
	Author         string
	AuthorKind     *string
	Message        *string
	PublishedAt    string
	Graph          *string
	RuleDefinition *string
	DraftMetrics   *[]previewMetricInput
}

type experimentalBisectInput struct {
	ProfileToken  string
	Versions      []experimentalRuleVersion
	Series        string
	At            string
	SlackSeconds  *int32
	LookbackHours *int32
}

// replaySide compiles one side with the preview path's own compiler, applies the same
// position-authority gate PreviewRule applies, and replays it. A problem the author must fix comes
// back as diagnostics (ok=false); degraded carries the replay's partial-result reason.
func (r *SchemaResolver) replaySide(ctx context.Context, tenant, activeVersion, profileToken string,
	side experimentalRuleSide, tr preview.TimeRange) (res preview.Result, diags []graph.Diagnostic, err error) {
	compiled, definition, _, cerr := r.compileDraft(previewRuleInput{
		Graph: side.Graph, RuleDefinition: side.RuleDefinition, ProfileToken: profileToken})
	if cerr != nil {
		return res, cerr, nil
	}
	draft, derr := draftResolution(side.DraftMetrics)
	if derr != nil {
		return res, derr, nil
	}
	// A rule that tests geofence containment reads position: the same authority and the same
	// precondition as PreviewRule, for the same reasons (see the gate there).
	if compiled.RequiresPosition {
		if err := auth.Authorize(ctx, auth.LocationRead); err != nil {
			return res, nil, err
		}
		if r.FenceSets == nil {
			return res, []graph.Diagnostic{{Severity: "error", Message: "this rule tests geofence containment, and the geofence archive is not reachable from this service, so it cannot be replayed"}}, nil
		}
	}
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{{
		Tenant: tenant, ProfileVersionToken: activeVersion, Compiled: compiled, Definition: definition,
	}})
	var opener preview.ReplayOpener = r.Replay
	if opener == nil {
		opener = r.GetNats(ctx)
	}
	runCtx, cancel := context.WithTimeout(ctx, previewBudget(ctx))
	defer cancel()
	opts := []preview.Option{preview.WithAppliedGuard(r.AppliedGuard)}
	var note string
	if draft != nil {
		if keys := draft.ConstantKeys(); len(keys) > 0 {
			var byDevice map[string][]calcfield.Constant
			byDevice, note = r.draftConstants(runCtx, tenant, profileToken, keys)
			opts = append(opts, preview.WithDeviceConstants(byDevice))
		}
	}
	res, err = preview.Run(runCtx, opener, streams.ResolvedEvents, reg, tenant, activeVersion, tr, 0,
		preview.DefaultMaxScan, preview.DefaultMaxRead, r.FenceSets, draft, opts...)
	if err != nil {
		return res, nil, err
	}
	if note != "" {
		res.Degraded = strings.TrimPrefix(note+"; "+res.Degraded, "; ")
		res.Degraded = strings.TrimSuffix(res.Degraded, "; ")
	}
	return res, nil, nil
}

// physprPrelude is everything both operations check before replaying: the flag, the authority,
// the tenant, and a published profile to replay against. A non-nil degraded string is the
// finished answer.
func (r *SchemaResolver) physprPrelude(ctx context.Context, profileToken string) (tenant, active string, degraded string, err error) {
	if !r.PhysicalPR {
		return "", "", "", errPhysicalPRDisabled
	}
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return "", "", "", err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return "", "", "", core.ErrNoTenant
	}
	if r.Profiles == nil {
		return "", "", "", fmt.Errorf("physical pull requests are unavailable: the profile projection is not configured")
	}
	a, found, err := r.Profiles.Load(ctx, tenant, profileToken)
	if err != nil {
		return "", "", "", err
	}
	if !found || a.ActiveVersionToken == "" {
		return tenant, "", "this profile has no published version yet, so there is no recorded history to replay against", nil
	}
	return tenant, a.ActiveVersionToken, "", nil
}

func parseWindow(startS, endS string) (preview.TimeRange, string, []graph.Diagnostic) {
	start, err := time.Parse(time.RFC3339, startS)
	if err != nil {
		return preview.TimeRange{}, "", []graph.Diagnostic{{Severity: "error", Message: fmt.Sprintf("invalid start time: %v", err)}}
	}
	end, err := time.Parse(time.RFC3339, endS)
	if err != nil {
		return preview.TimeRange{}, "", []graph.Diagnostic{{Severity: "error", Message: fmt.Sprintf("invalid end time: %v", err)}}
	}
	if !end.After(start) {
		return preview.TimeRange{}, "", []graph.Diagnostic{{Severity: "error", Message: "the end of the window must be after its start"}}
	}
	note := ""
	if end.Sub(start) > preview.DefaultMaxWindow {
		start = end.Add(-preview.DefaultMaxWindow)
		note = fmt.Sprintf("the window was capped to the most recent %s", preview.DefaultMaxWindow)
	}
	return preview.TimeRange{Start: start, End: end}, note, nil
}

func sideRef(s experimentalRuleSide, fallback string) physpr.Ref {
	ref := physpr.Ref{Label: fallback}
	if s.Label != nil && *s.Label != "" {
		ref.Label = *s.Label
	}
	if s.Author != nil {
		ref.Author = *s.Author
	}
	return ref
}

// ExperimentalPreviewRuleDiff replays two versions of a detection rule over the same recorded
// window and reports the difference in what each would have done: the "physical pull request".
// EXPERIMENTAL and off unless DC_EXPERIMENTAL_PHYSICAL_PR=true. It reuses the preview harness
// unchanged (two Run calls), so it carries the preview's isolation contract and approximations.
func (r *SchemaResolver) ExperimentalPreviewRuleDiff(ctx context.Context, args struct{ Input experimentalRuleDiffInput }) (*ExperimentalRuleDiffResolver, error) {
	in := args.Input
	tenant, active, degraded, err := r.physprPrelude(ctx, in.ProfileToken)
	if err != nil {
		return nil, err
	}
	if active == "" {
		return &ExperimentalRuleDiffResolver{ok: true, degraded: &degraded, diags: []*CanvasDiagnosticResolver{}}, nil
	}
	tr, clampNote, werr := parseWindow(in.Start, in.End)
	if werr != nil {
		return failedDiff(werr), nil
	}
	if !globalPreviewGate.acquire(tenant) {
		d := "the maximum number of previews are already running for this tenant; try again in a moment"
		return &ExperimentalRuleDiffResolver{ok: true, degraded: &d, diags: []*CanvasDiagnosticResolver{}}, nil
	}
	defer globalPreviewGate.release(tenant)

	base, diags, err := r.replaySide(ctx, tenant, active, in.ProfileToken, in.Base, tr)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		return failedDiff(prefixDiags("base: ", diags)), nil
	}
	head, diags, err := r.replaySide(ctx, tenant, active, in.ProfileToken, in.Head, tr)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		return failedDiff(prefixDiags("head: ", diags)), nil
	}

	shift := time.Duration(0)
	if in.ShiftWindowSeconds != nil && *in.ShiftWindowSeconds > 0 {
		s := *in.ShiftWindowSeconds
		if s > maxShiftWindowSeconds {
			s = maxShiftWindowSeconds
		}
		shift = time.Duration(s) * time.Second
	}
	d := physpr.DiffFirings(base, head, shift)
	window := fmt.Sprintf("%s .. %s", tr.Start.UTC().Format(time.RFC3339), tr.End.UTC().Format(time.RFC3339))
	out := &ExperimentalRuleDiffResolver{
		ok:       true,
		compared: true,
		diff:     d,
		report:   physpr.RenderDiff(d, sideRef(in.Base, "base"), sideRef(in.Head, "head"), window),
		diags:    []*CanvasDiagnosticResolver{},
	}
	var notes []string
	if clampNote != "" {
		notes = append(notes, clampNote)
	}
	if d.BaseDegraded != "" {
		notes = append(notes, "base: "+d.BaseDegraded)
	}
	if d.HeadDegraded != "" {
		notes = append(notes, "head: "+d.HeadDegraded)
	}
	if len(notes) > 0 {
		s := strings.Join(notes, "; ")
		out.degraded = &s
	}
	return out, nil
}

// ExperimentalBisectRule replays an ordered history of rule versions (oldest first) over the
// window ending shortly after `at` and names the first version whose answer to "is an alarm raised
// on series near `at`" differs from the oldest version's, with the principal that published it.
// EXPERIMENTAL and off unless DC_EXPERIMENTAL_PHYSICAL_PR=true.
func (r *SchemaResolver) ExperimentalBisectRule(ctx context.Context, args struct{ Input experimentalBisectInput }) (*ExperimentalBisectResolver, error) {
	in := args.Input
	if !r.PhysicalPR {
		return nil, errPhysicalPRDisabled
	}
	if len(in.Versions) < 2 || len(in.Versions) > physpr.MaxVersions {
		return failedBisect(graph.Diagnostic{Severity: "error",
			Message: fmt.Sprintf("versions must hold between 2 and %d entries, oldest first", physpr.MaxVersions)}), nil
	}
	if strings.TrimSpace(in.Series) == "" {
		return failedBisect(graph.Diagnostic{Severity: "error", Message: "series (the device token) is required"}), nil
	}
	at, perr := time.Parse(time.RFC3339, in.At)
	if perr != nil {
		return failedBisect(graph.Diagnostic{Severity: "error", Message: fmt.Sprintf("invalid at time: %v", perr)}), nil
	}
	slack := 5 * time.Minute
	if in.SlackSeconds != nil && *in.SlackSeconds >= 0 {
		slack = time.Duration(*in.SlackSeconds) * time.Second
		if slack > maxBisectSlack {
			slack = maxBisectSlack
		}
	}
	lookback := 6 * time.Hour
	if in.LookbackHours != nil && *in.LookbackHours > 0 && *in.LookbackHours <= 24 {
		lookback = time.Duration(*in.LookbackHours) * time.Hour
	}
	tenant, active, degraded, err := r.physprPrelude(ctx, in.ProfileToken)
	if err != nil {
		return nil, err
	}
	if active == "" {
		return &ExperimentalBisectResolver{ok: true, degraded: &degraded, diags: []*CanvasDiagnosticResolver{}}, nil
	}
	if !globalPreviewGate.acquire(tenant) {
		d := "the maximum number of previews are already running for this tenant; try again in a moment"
		return &ExperimentalBisectResolver{ok: true, degraded: &d, diags: []*CanvasDiagnosticResolver{}}, nil
	}
	defer globalPreviewGate.release(tenant)

	commits := make([]physpr.Commit, len(in.Versions))
	for i, v := range in.Versions {
		pub, terr := time.Parse(time.RFC3339, v.PublishedAt)
		if terr != nil {
			return failedBisect(graph.Diagnostic{Severity: "error", Message: fmt.Sprintf("versions[%d].publishedAt: %v", i, terr)}), nil
		}
		c := physpr.Commit{ID: v.Commit, Author: v.Author, At: pub}
		if v.AuthorKind != nil {
			c.AuthorKind = *v.AuthorKind
		}
		if v.Message != nil {
			c.Message = *v.Message
		}
		commits[i] = c
	}

	tr := preview.TimeRange{Start: at.Add(-lookback), End: at.Add(slack)}
	var degradedNotes []string
	var probeDiags []graph.Diagnostic
	probe := func(ctx context.Context, i int) (bool, error) {
		v := in.Versions[i]
		res, diags, err := r.replaySide(ctx, tenant, active, in.ProfileToken, experimentalRuleSide{
			Graph: v.Graph, RuleDefinition: v.RuleDefinition, DraftMetrics: v.DraftMetrics}, tr)
		if err != nil {
			return false, err
		}
		if len(diags) > 0 {
			probeDiags = prefixDiags(fmt.Sprintf("version %s: ", v.Commit), diags)
			return false, errors.New("a version does not compile")
		}
		if res.Degraded != "" {
			degradedNotes = append(degradedNotes, fmt.Sprintf("%s: %s", v.Commit, res.Degraded))
		}
		for _, f := range res.Firings {
			if f.Raise && f.Series == in.Series && !f.OccurredAt.Before(at.Add(-slack)) && !f.OccurredAt.After(at.Add(slack)) {
				return true, nil
			}
		}
		return false, nil
	}
	br, berr := physpr.Bisect(ctx, len(commits), probe)
	if len(probeDiags) > 0 {
		return failedBisect(probeDiags...), nil
	}
	question := fmt.Sprintf("is an alarm raised on %s within %s of %s?", in.Series, slack, at.UTC().Format(time.RFC3339))
	out := &ExperimentalBisectResolver{ok: true, diags: []*CanvasDiagnosticResolver{}}
	if len(degradedNotes) > 0 {
		s := strings.Join(dedupe(degradedNotes), "; ")
		out.degraded = &s
	}
	if errors.Is(berr, physpr.ErrNoChange) {
		out.report = fmt.Sprintf("Bisect: %s\n%s\n", question, physpr.ErrNoChange.Error())
		out.replays = int32(len(br.Trail))
		return out, nil
	}
	if berr != nil {
		return nil, berr
	}
	out.found = true
	out.first = commits[br.First]
	out.previous = commits[br.First-1]
	out.appears = !br.OldestHolds
	out.replays = int32(len(br.Trail))
	out.report = physpr.RenderBisect(br, commits, question)
	return out, nil
}

func prefixDiags(prefix string, ds []graph.Diagnostic) []graph.Diagnostic {
	out := make([]graph.Diagnostic, len(ds))
	for i, d := range ds {
		d.Message = prefix + d.Message
		out[i] = d
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func failedDiff(diags []graph.Diagnostic) *ExperimentalRuleDiffResolver {
	rs := make([]*CanvasDiagnosticResolver, 0, len(diags))
	for _, d := range diags {
		rs = append(rs, newCanvasDiagnostic(d))
	}
	return &ExperimentalRuleDiffResolver{ok: false, diags: rs}
}

func failedBisect(diags ...graph.Diagnostic) *ExperimentalBisectResolver {
	rs := make([]*CanvasDiagnosticResolver, 0, len(diags))
	for _, d := range diags {
		rs = append(rs, newCanvasDiagnostic(d))
	}
	return &ExperimentalBisectResolver{ok: false, diags: rs}
}

// ── Resolvers ──────────────────────────────────────────────────────────────

// ExperimentalRuleDiffResolver resolves ExperimentalRuleDiff.
type ExperimentalRuleDiffResolver struct {
	// compared is true only when both sides were replayed and diffed; a skipped request is ok but
	// compared nothing, and must never read as "no behavioural change".
	compared bool
	ok       bool
	diff     physpr.Diff
	report   string
	degraded *string
	diags    []*CanvasDiagnosticResolver
}

func (r *ExperimentalRuleDiffResolver) Ok() bool                                 { return r.ok }
func (r *ExperimentalRuleDiffResolver) Empty() bool                              { return r.ok && r.compared && r.diff.Empty() }
func (r *ExperimentalRuleDiffResolver) Report() string                           { return r.report }
func (r *ExperimentalRuleDiffResolver) Degraded() *string                        { return r.degraded }
func (r *ExperimentalRuleDiffResolver) Diagnostics() []*CanvasDiagnosticResolver { return r.diags }
func (r *ExperimentalRuleDiffResolver) Summary() *ExperimentalDiffSummaryResolver {
	return &ExperimentalDiffSummaryResolver{d: r.diff}
}
func (r *ExperimentalRuleDiffResolver) Changes() []*ExperimentalFiringChangeResolver {
	out := make([]*ExperimentalFiringChangeResolver, 0, len(r.diff.Changes))
	for _, c := range r.diff.Changes {
		out = append(out, &ExperimentalFiringChangeResolver{c: c})
	}
	return out
}

// ExperimentalDiffSummaryResolver resolves ExperimentalDiffSummary.
type ExperimentalDiffSummaryResolver struct{ d physpr.Diff }

func (r *ExperimentalDiffSummaryResolver) AlarmsAdded() int32      { return int32(r.d.Raises.Added) }
func (r *ExperimentalDiffSummaryResolver) AlarmsSuppressed() int32 { return int32(r.d.Raises.Removed) }
func (r *ExperimentalDiffSummaryResolver) AlarmsMoved() int32      { return int32(r.d.Raises.Shifted) }
func (r *ExperimentalDiffSummaryResolver) AlarmsUnchanged() int32  { return int32(r.d.Raises.Unchanged) }
func (r *ExperimentalDiffSummaryResolver) ResolutionsChanged() int32 {
	return int32(r.d.Resolves.Changed())
}
func (r *ExperimentalDiffSummaryResolver) DevicesAffected() int32 { return int32(len(r.d.Series)) }
func (r *ExperimentalDiffSummaryResolver) BaseCalculatedSkips() int32 {
	return int32(r.d.Base.CalculatedSkips)
}
func (r *ExperimentalDiffSummaryResolver) HeadCalculatedSkips() int32 {
	return int32(r.d.Head.CalculatedSkips)
}
func (r *ExperimentalDiffSummaryResolver) BaseEventsScanned() int32 {
	return int32(r.d.Base.EventsScanned)
}
func (r *ExperimentalDiffSummaryResolver) HeadEventsScanned() int32 {
	return int32(r.d.Head.EventsScanned)
}

// ExperimentalFiringChangeResolver resolves ExperimentalFiringChange.
type ExperimentalFiringChangeResolver struct{ c physpr.Change }

func (r *ExperimentalFiringChangeResolver) Series() string  { return r.c.Series }
func (r *ExperimentalFiringChangeResolver) Edge() string    { return string(r.c.Edge) }
func (r *ExperimentalFiringChangeResolver) Kind() string    { return string(r.c.Kind) }
func (r *ExperimentalFiringChangeResolver) BaseAt() *string { return optTime(r.c.Base) }
func (r *ExperimentalFiringChangeResolver) HeadAt() *string { return optTime(r.c.Head) }
func (r *ExperimentalFiringChangeResolver) ShiftSeconds() *int32 {
	if r.c.Kind != physpr.Shifted {
		return nil
	}
	s := int32(r.c.Shift / time.Second)
	return &s
}

func optTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

// ExperimentalBisectResolver resolves ExperimentalBisectResult.
type ExperimentalBisectResolver struct {
	ok       bool
	found    bool
	appears  bool
	replays  int32
	first    physpr.Commit
	previous physpr.Commit
	report   string
	degraded *string
	diags    []*CanvasDiagnosticResolver
}

func (r *ExperimentalBisectResolver) Ok() bool                                 { return r.ok }
func (r *ExperimentalBisectResolver) Found() bool                              { return r.found }
func (r *ExperimentalBisectResolver) Report() string                           { return r.report }
func (r *ExperimentalBisectResolver) Replays() int32                           { return r.replays }
func (r *ExperimentalBisectResolver) Degraded() *string                        { return r.degraded }
func (r *ExperimentalBisectResolver) Diagnostics() []*CanvasDiagnosticResolver { return r.diags }
func (r *ExperimentalBisectResolver) Outcome() *string {
	if !r.found {
		return nil
	}
	s := "disappears"
	if r.appears {
		s = "appears"
	}
	return &s
}
func (r *ExperimentalBisectResolver) Commit() *ExperimentalCommitResolver {
	if !r.found {
		return nil
	}
	return &ExperimentalCommitResolver{c: r.first}
}
func (r *ExperimentalBisectResolver) PreviousCommit() *ExperimentalCommitResolver {
	if !r.found {
		return nil
	}
	return &ExperimentalCommitResolver{c: r.previous}
}

// ExperimentalCommitResolver resolves ExperimentalCommit.
type ExperimentalCommitResolver struct{ c physpr.Commit }

func (r *ExperimentalCommitResolver) Id() string          { return r.c.ID }
func (r *ExperimentalCommitResolver) Author() string      { return r.c.Author }
func (r *ExperimentalCommitResolver) AuthorKind() *string { return nonEmpty(r.c.AuthorKind) }
func (r *ExperimentalCommitResolver) Message() *string    { return nonEmpty(r.c.Message) }
func (r *ExperimentalCommitResolver) PublishedAt() string {
	return r.c.At.UTC().Format(time.RFC3339)
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
