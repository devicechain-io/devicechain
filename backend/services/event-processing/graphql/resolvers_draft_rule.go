// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// DraftDetectionRuleFromText is the ADR-056 slice-1 NL→rule authoring door — the third front
// door to the one CEL compiler, alongside the form builder (validateDetectionRules) and the
// visual canvas (compileCanvas). It runs the bounded infer→compile→repair loop: it carries the
// author's prompt to the model the tenant's tier offers (over an ai:infer service token, per-tenant
// opt-in + fail-closed) and runs the returned candidate through the SAME rules.Compile firewall
// the other doors use. It PERSISTS NOTHING — the compiled draft flows back for the human to
// review and save through the normal createDetectionRule door under their own token (the AI
// proposes, the deterministic compiler disposes, the human decides).
//
// It gates on device:write, NOT device:read like the pure compile doors: drafting is a precursor
// to authoring and spends inference budget, so a read-only viewer must not be able to invoke it.
// The tenant rides the request context and is threaded to ai-inference (which reads it to gate
// external-routing consent). When the drafting seam is not wired (ai-inference is an opt-in area),
// it returns an unavailable result rather than a hard error, so the console shows an actionable
// reason.
func (r *SchemaResolver) DraftDetectionRuleFromText(ctx context.Context, args struct {
	Input draftRuleFromTextInput
}) (*DraftRuleResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		// Fail closed: a tenant-scoped call with no tenant in context must not run — the same
		// tenant source the platform's storage-scope callback uses.
		return nil, core.ErrNoTenant
	}

	// Not wired (no service secret / ai-inference is not an enabled+configured peer): report
	// unavailable, fail-closed. The form + canvas doors are unaffected.
	if r.Drafter == nil {
		return &DraftRuleResultResolver{res: nldraft.Result{
			Unavailable:       true,
			UnavailableReason: "NL rule drafting is not enabled on this deployment",
		}}, nil
	}

	hints := args.Input.metricsSlice()
	metrics := make([]nldraft.MetricHint, 0, len(hints))
	for _, m := range hints {
		metrics = append(metrics, nldraft.MetricHint{
			Key:         m.Key,
			DataType:    derefStr(m.DataType),
			Unit:        derefStr(m.Unit),
			Description: derefStr(m.Description),
		})
	}

	res, err := r.Drafter.Draft(ctx, tenant, nldraft.Request{
		Text:         args.Input.Text,
		ProfileToken: args.Input.ProfileToken,
		Metrics:      metrics,
	})
	if err != nil {
		return nil, err
	}
	return &DraftRuleResultResolver{res: res}, nil
}

// draftRuleFromTextInput mirrors the SDL DraftRuleFromTextInput.
type draftRuleFromTextInput struct {
	Text         string
	ProfileToken string
	Metrics      *[]metricHintInput
}

// Metrics is a pointer to a slice for the nullable SDL list; normalize a nil pointer to an
// empty slice so callers can range over it.
func (i *draftRuleFromTextInput) metricsSlice() []metricHintInput {
	if i.Metrics == nil {
		return nil
	}
	return *i.Metrics
}

type metricHintInput struct {
	Key         string
	DataType    *string
	Unit        *string
	Description *string
}

// derefStr returns the pointed-to string or "".
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// DraftRuleResultResolver resolves the NL drafting outcome.
type DraftRuleResultResolver struct {
	res nldraft.Result
}

// Ok reports whether a candidate compiled through the DETECT firewall.
func (r *DraftRuleResultResolver) Ok() bool { return r.res.OK }

// Definition resolves the compiled rules.Rule JSON (null unless ok).
func (r *DraftRuleResultResolver) Definition() *string {
	if !r.res.OK {
		return nil
	}
	d := r.res.Definition
	return &d
}

// EstimatedCost resolves the compiled leaf cost (null unless ok).
func (r *DraftRuleResultResolver) EstimatedCost() *int32 {
	if !r.res.OK {
		return nil
	}
	c := r.res.EstimatedCost
	return &c
}

// Model resolves the answering provider's model (null when no candidate was produced).
func (r *DraftRuleResultResolver) Model() *string { return nilIfEmpty(r.res.Model) }

// Provider resolves the answering provider name (null when no candidate was produced).
func (r *DraftRuleResultResolver) Provider() *string { return nilIfEmpty(r.res.Provider) }

// Attempts resolves the number of inference rounds performed.
func (r *DraftRuleResultResolver) Attempts() int32 { return r.res.Attempts }

// RawCandidate resolves the model's last rejected output (null when ok or none produced).
func (r *DraftRuleResultResolver) RawCandidate() *string { return nilIfEmpty(r.res.RawCandidate) }

// Diagnostics resolves the compiler rejection reasons (empty when ok).
func (r *DraftRuleResultResolver) Diagnostics() []*DraftDiagnosticResolver {
	out := make([]*DraftDiagnosticResolver, 0, len(r.res.Diagnostics))
	for _, d := range r.res.Diagnostics {
		out = append(out, &DraftDiagnosticResolver{d: d})
	}
	return out
}

// Unavailable reports whether the inference path itself could not run.
func (r *DraftRuleResultResolver) Unavailable() bool { return r.res.Unavailable }

// UnavailableReason resolves the (coarsened) reason (null unless unavailable).
func (r *DraftRuleResultResolver) UnavailableReason() *string {
	if !r.res.Unavailable {
		return nil
	}
	return nilIfEmpty(r.res.UnavailableReason)
}

// DraftDiagnosticResolver resolves one drafting diagnostic.
type DraftDiagnosticResolver struct {
	d nldraft.Diagnostic
}

// Field resolves the anchored rule field (null when unanchored).
func (r *DraftDiagnosticResolver) Field() *string { return nilIfEmpty(r.d.Field) }

// Message resolves the console-surfaceable reason.
func (r *DraftDiagnosticResolver) Message() string { return r.d.Message }

// nilIfEmpty maps "" to a null pointer (an omitted optional String), else a pointer to s.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
