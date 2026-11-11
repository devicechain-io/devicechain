// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/replayinput"
	"github.com/devicechain-io/dc-event-processing/internal/review"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ruletest"
	"github.com/devicechain-io/dc-microservice/streams"
)

type compareRuleEnvelopeInput struct {
	Token              string
	Enabled            bool
	Definition         string
	EntityGroupToken   *string
	EntityGroupVersion *int32
}

func (in *compareRuleEnvelopeInput) envelope() *review.RuleEnvelope {
	if in == nil {
		return nil
	}
	e := &review.RuleEnvelope{Token: in.Token, Enabled: in.Enabled, Definition: json.RawMessage(in.Definition)}
	if in.EntityGroupToken != nil {
		e.EntityGroupToken = *in.EntityGroupToken
	}
	if in.EntityGroupVersion != nil {
		e.EntityGroupVersion = int(*in.EntityGroupVersion)
	}
	return e
}

type compareWindowInput struct{ Role, Start, End string }
type compareExpectationInput struct {
	Window string
	Name   *string
	Expect string
}
type compareBoundsInput struct{ Role, Stream, FirstSeq, LastSeq string }
type compareRuleVersionsInput struct {
	ProfileToken       string
	Base, Draft        *compareRuleEnvelopeInput
	DeviceTokens       []string
	Windows            []compareWindowInput
	Expectations       *[]compareExpectationInput
	ShiftWindowSeconds *int32
	StreamBounds       *[]compareBoundsInput
	ConstantMetrics    *[]string
}

// CompareRuleVersions is the service-only recorded-input comparison adapter.
func (r *SchemaResolver) CompareRuleVersions(ctx context.Context, args struct{ Input compareRuleVersionsInput }) (*CompareRuleVersionsResultResolver, error) {
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return nil, auth.ErrForbidden
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Subject != "device-management" || auth.IsAgentOrigin(claims) {
		return nil, auth.ErrForbidden
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !globalPreviewGate.acquire(tenant) {
		return compareRefused(refusalBusy, "too many concurrent previews"), nil
	}
	defer globalPreviewGate.release(tenant)
	in := args.Input
	if len(in.DeviceTokens) > review.MaxDevices || (in.Expectations != nil && len(*in.Expectations) > review.MaxExpectations) {
		return compareRefused(refusalLimitExceeded, "comparison device or expectation limit exceeded"), nil
	}
	p := review.Params{Tenant: tenant, ProfileToken: in.ProfileToken, Base: in.Base.envelope(), Draft: in.Draft.envelope(), Devices: in.DeviceTokens, Suffix: streams.ResolvedEvents, Lateness: r.EvaluationLateness, AppliedGuard: r.AppliedGuard, Now: time.Now()}
	for _, w := range in.Windows {
		start, e := time.Parse(time.RFC3339Nano, w.Start)
		if e != nil {
			return compareRefused(refusalInvalidInput, "window start must be RFC3339"), nil
		}
		end, e := time.Parse(time.RFC3339Nano, w.End)
		if e != nil {
			return compareRefused(refusalInvalidInput, "window end must be RFC3339"), nil
		}
		if end.Sub(start) > review.MaxWindow {
			return compareRefused(refusalLimitExceeded, "each comparison window is limited to 24 hours"), nil
		}
		p.Windows = append(p.Windows, review.Window{Role: review.Role(w.Role), Start: start, End: end})
	}
	if in.Expectations != nil {
		for _, e := range *in.Expectations {
			var expect ruletest.Expect
			if err := ruletest.DecodeStrict([]byte(e.Expect), &expect); err != nil {
				return compareRefused(refusalInvalidInput, err.Error()), nil
			}
			if e.Name != nil {
				expect.Name = *e.Name
			}
			p.Expectations = append(p.Expectations, review.Expectation{Window: review.Role(e.Window), Expect: expect})
		}
	}
	if in.ShiftWindowSeconds != nil {
		p.ShiftWindow = time.Duration(*in.ShiftWindowSeconds) * time.Second
	}
	if in.ConstantMetrics != nil {
		p.ConstantMetrics = *in.ConstantMetrics
	}
	if in.StreamBounds != nil {
		for _, b := range *in.StreamBounds {
			first, e1 := strconv.ParseUint(b.FirstSeq, 10, 64)
			last, e2 := strconv.ParseUint(b.LastSeq, 10, 64)
			if e1 != nil || e2 != nil {
				return compareRefused(refusalInvalidInput, "sequence bounds must be uint64 decimal strings"), nil
			}
			p.StreamBounds = append(p.StreamBounds, review.Bounds{Role: review.Role(b.Role), Stream: b.Stream, FirstSeq: first, LastSeq: last})
		}
	}
	var source replayinput.Opener = r.Replay
	if source == nil {
		source = r.GetNats(ctx)
	}
	p.Source = source
	budget := previewBudget(ctx)
	if budget <= 0 {
		return compareRefused(refusalTimeout, "no comparison time remains"), nil
	}
	p.WindowBudget = budget / 2
	type historicalRule struct {
		env   *review.RuleEnvelope
		found bool
		err   error
	}
	history := map[string]historicalRule{}
	p.VersionRules = func(ctx context.Context, version string) (*review.RuleEnvelope, bool, error) {
		if cached, ok := history[version]; ok {
			return cached.env, cached.found, cached.err
		}
		if r.DetectRules == nil {
			return nil, false, fmt.Errorf("rule projection is unavailable")
		}
		rows, err := r.DetectRules.LoadByProfileVersion(ctx, tenant, version)
		if err != nil {
			history[version] = historicalRule{err: err}
			return nil, false, err
		}
		if p.Base == nil {
			return nil, false, nil
		}
		for _, row := range rows {
			if row.RuleToken == p.Base.Token {
				env := &review.RuleEnvelope{Token: row.RuleToken, Enabled: true, Definition: json.RawMessage(row.Definition), EntityGroupToken: row.EntityGroupToken, EntityGroupVersion: int(row.EntityGroupVersion)}
				history[version] = historicalRule{env: env, found: true}
				return env, true, nil
			}
		}
		history[version] = historicalRule{}
		return nil, false, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	out, err := review.Run(runCtx, p)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		var input *review.InputError
		if errors.As(err, &input) {
			if input.RuleRejected {
				return compareRefused(refusalRuleRejected, err.Error()), nil
			}
			return compareRefused(refusalInvalidInput, err.Error()), nil
		}
		var limit *evaluate.LimitError
		if errors.As(err, &limit) {
			return compareRefused(refusalLimitExceeded, err.Error()), nil
		}
		var rule *evaluate.RuleError
		if errors.As(err, &rule) {
			return compareRefused(refusalRuleRejected, err.Error()), nil
		}
		var malformed *evaluate.InputError
		if errors.As(err, &malformed) {
			return compareRefused(refusalInvalidInput, err.Error()), nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return compareRefused(refusalTimeout, "comparison time budget exhausted"), nil
		}
		return nil, err
	}
	// encoding/json sorts map keys; compact re-encoding also canonicalizes embedded JSON.
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &CompareRuleVersionsResultResolver{out: &out, result: string(raw), digest: fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}

type CompareRuleVersionsResultResolver struct {
	out            *review.Result
	refusal        *CompareRefusalResolver
	result, digest string
}
type CompareRefusalResolver struct{ reason, message string }

func compareRefused(reason, message string) *CompareRuleVersionsResultResolver {
	return &CompareRuleVersionsResultResolver{refusal: &CompareRefusalResolver{reason, message}}
}
func (r *CompareRefusalResolver) Reason() string                              { return r.reason }
func (r *CompareRefusalResolver) Message() string                             { return r.message }
func (r *CompareRuleVersionsResultResolver) Ok() bool                         { return r.out != nil }
func (r *CompareRuleVersionsResultResolver) Refusal() *CompareRefusalResolver { return r.refusal }
func (r *CompareRuleVersionsResultResolver) Outcome() *string {
	if r.out == nil {
		return nil
	}
	return &r.out.Outcome
}
func (r *CompareRuleVersionsResultResolver) Fidelity() *CompareFidelityResolver {
	if r.out == nil {
		return nil
	}
	return &CompareFidelityResolver{r.out.Fidelity}
}
func (r *CompareRuleVersionsResultResolver) InputDigest() *string {
	if r.out == nil {
		return nil
	}
	return &r.out.InputDigest
}
func (r *CompareRuleVersionsResultResolver) Result() *string {
	if r.out == nil {
		return nil
	}
	return &r.result
}
func (r *CompareRuleVersionsResultResolver) ResultDigest() *string {
	if r.out == nil {
		return nil
	}
	return &r.digest
}
func (r *CompareRuleVersionsResultResolver) EngineBuild() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "development"
}
func (r *CompareRuleVersionsResultResolver) Expectations() []*CompareExpectationResolver {
	out := []*CompareExpectationResolver{}
	if r.out != nil {
		for _, e := range r.out.Expectations {
			out = append(out, &CompareExpectationResolver{e})
		}
	}
	return out
}
func (r *CompareRuleVersionsResultResolver) Windows() []*CompareWindowResolver {
	out := []*CompareWindowResolver{}
	if r.out != nil {
		for _, w := range r.out.Windows {
			out = append(out, &CompareWindowResolver{w})
		}
	}
	return out
}

type CompareFidelityResolver struct{ f review.Fidelity }

func (r *CompareFidelityResolver) Status() string    { return r.f.Status }
func (r *CompareFidelityResolver) Reasons() []string { return append([]string{}, r.f.Reasons...) }
func (r *CompareFidelityResolver) Label() string     { return r.f.Label }

type CompareExpectationResolver struct{ e review.ExpectationResult }

func (r *CompareExpectationResolver) Index() int32           { return int32(r.e.Index) }
func (r *CompareExpectationResolver) Name() string           { return r.e.Name }
func (r *CompareExpectationResolver) Window() string         { return string(r.e.Window) }
func (r *CompareExpectationResolver) Verdict() string        { return string(r.e.Verdict) }
func (r *CompareExpectationResolver) Baseline() string       { return string(r.e.Baseline) }
func (r *CompareExpectationResolver) Reason() string         { return r.e.Reason }
func (r *CompareExpectationResolver) BaselineReason() string { return r.e.BaselineReason }

type CompareWindowResolver struct{ w review.WindowResult }

func (r *CompareWindowResolver) Role() string  { return string(r.w.Role) }
func (r *CompareWindowResolver) Start() string { return r.w.Start.UTC().Format(time.RFC3339Nano) }
func (r *CompareWindowResolver) End() string   { return r.w.End.UTC().Format(time.RFC3339Nano) }
func (r *CompareWindowResolver) LeadInStart() string {
	return r.w.LeadInStart.UTC().Format(time.RFC3339Nano)
}
func (r *CompareWindowResolver) Stream() string      { return r.w.Bounds.Stream }
func (r *CompareWindowResolver) FirstSeq() string    { return strconv.FormatUint(r.w.Bounds.FirstSeq, 10) }
func (r *CompareWindowResolver) LastSeq() string     { return strconv.FormatUint(r.w.Bounds.LastSeq, 10) }
func (r *CompareWindowResolver) InputDigest() string { return r.w.InputDigest }
func (r *CompareWindowResolver) Degraded() []string  { return append([]string{}, r.w.Degraded...) }
func (r *CompareWindowResolver) ChangeCounts() *CompareCountsResolver {
	return &CompareCountsResolver{r.w.Counts}
}
func (r *CompareWindowResolver) ChangesTruncated() bool { return r.w.ChangesTruncated }

type CompareCountsResolver struct{ c review.Counts }

func (r *CompareCountsResolver) Added() int32          { return int32(r.c.Added) }
func (r *CompareCountsResolver) Removed() int32        { return int32(r.c.Removed) }
func (r *CompareCountsResolver) Shifted() int32        { return int32(r.c.Shifted) }
func (r *CompareCountsResolver) ContentChanged() int32 { return int32(r.c.ContentChanged) }
