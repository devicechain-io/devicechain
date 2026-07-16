// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// InferenceRequestInput is the inbound inference request (a prompt + optional system
// prompt). The output-token cap, endpoint, and timeout are server-side (config +
// provider row), never caller-supplied.
type InferenceRequestInput struct {
	Prompt string
	System *string
}

// systemPrompt returns the optional system prompt ("" when absent).
func (in InferenceRequestInput) systemPrompt() string {
	if in.System == nil {
		return ""
	}
	return *in.System
}

// runInference validates the prompt, applies the configured per-call deadline, runs
// the provider, and shapes the result. Shared by both inference entrypoints; the
// caller has already resolved the provider through the fail-closed cascade.
func (r *SchemaResolver) runInference(ctx context.Context, resolved *inference.Resolved, req InferenceRequestInput) (*InferenceResultResolver, error) {
	inferCtx, cancel := context.WithTimeout(ctx, r.Inference.InferenceTimeout())
	defer cancel()
	out, err := resolved.Provider.Infer(inferCtx, inference.Input{
		System: req.systemPrompt(),
		Prompt: req.Prompt,
	})
	if err != nil {
		return nil, err
	}
	return &InferenceResultResolver{candidate: out.Candidate, model: out.Model, provider: resolved.Token}, nil
}

// tenantSafeError coarsens an inference error for the TENANT-facing path. An
// ErrConsentRequired is surfaced as-is (actionable — the tenant opts in); everything
// else collapses to the bare ErrUnavailable sentinel with the detail logged
// server-side, so an unprivileged caller never learns internal topology (the
// user-management URL, the provider endpoint) or which specific gate tripped. The
// operator testAiProvider path keeps the detailed error (same trust tier as the config).
func tenantSafeError(err error) error {
	if err == nil || errors.Is(err, inference.ErrConsentRequired) {
		return err
	}
	log.Warn().Err(err).Msg("inferRuleCandidate inference failed")
	return inference.ErrUnavailable
}

// InferRuleCandidate runs a prompt through the ACTIVE provider on behalf of a tenant's
// NL-authoring request (ADR-056 Zone 1). Gated by the least-privilege ai:infer
// authority (the event-processing service token holds it, Slice 1); the tenant comes
// from context (the service-token X-DC-Tenant header). The resolver enforces the
// external-routing consent gate fail-closed — this resolver never reaches a provider
// without it. The returned candidate is validated downstream by rules.Compile; it is
// not trusted or persisted here.
func (r *SchemaResolver) InferRuleCandidate(ctx context.Context, args struct {
	Request InferenceRequestInput
}) (*InferenceResultResolver, error) {
	if err := auth.Authorize(ctx, auth.AIInfer); err != nil {
		return nil, err
	}
	if r.Inference == nil {
		return nil, fmt.Errorf("inference is not configured")
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("inference requires a tenant context")
	}
	if err := r.Inference.ValidatePrompt(args.Request.systemPrompt(), args.Request.Prompt); err != nil {
		return nil, err
	}
	resolved, err := r.Inference.ResolveForTenant(ctx, tenant)
	if err != nil {
		return nil, tenantSafeError(err)
	}
	result, err := r.runInference(ctx, resolved, args.Request)
	if err != nil {
		return nil, tenantSafeError(err)
	}
	return result, nil
}

// TestAiProvider runs an operator-supplied prompt through a SPECIFIC provider (by
// token) as a smoke test — the ai:admin test-infer affordance behind the console
// provider screen (Slice 0d). It validates a provider's endpoint + key before the
// operator promotes it. Gated by ai:admin (a provider-management action, not the
// production inference path); it does NOT apply the tenant-consent gate (an operator
// testing operator-owned instance config with an operator prompt — no tenant data
// crosses the boundary), but the provider's key must still resolve, so a test never
// calls out unauthenticated.
func (r *SchemaResolver) TestAiProvider(ctx context.Context, args struct {
	Token   string
	Request InferenceRequestInput
}) (*InferenceResultResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	if r.Inference == nil {
		return nil, fmt.Errorf("inference is not configured")
	}
	if err := r.Inference.ValidatePrompt(args.Request.systemPrompt(), args.Request.Prompt); err != nil {
		return nil, err
	}
	resolved, err := r.Inference.ResolveProvider(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return r.runInference(ctx, resolved, args.Request)
}

// CreateAiProvider creates a new provider.
func (r *SchemaResolver) CreateAiProvider(ctx context.Context, args struct {
	Request model.AIProviderCreateRequest
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	created, err := r.GetApi(ctx).CreateAIProvider(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *created, S: r, C: ctx}, nil
}

// UpdateAiProvider updates the provider with the given (current) token.
// expectedUpdatedAt, when supplied, is an optimistic-concurrency precondition.
func (r *SchemaResolver) UpdateAiProvider(ctx context.Context, args struct {
	Token             string
	Request           model.AIProviderCreateRequest
	ExpectedUpdatedAt *string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	updated, err := r.GetApi(ctx).UpdateAIProvider(ctx, args.Token, &args.Request, args.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *updated, S: r, C: ctx}, nil
}

// DeleteAiProvider deletes the provider with the given token.
func (r *SchemaResolver) DeleteAiProvider(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteAIProvider(ctx, args.Token)
}

// SetActiveAiProvider promotes the provider with the given token to THE active one.
func (r *SchemaResolver) SetActiveAiProvider(ctx context.Context, args struct {
	Token string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	active, err := r.GetApi(ctx).SetActiveProvider(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *active, S: r, C: ctx}, nil
}

// ClearActiveAiProvider clears the active provider (return to "default of none").
func (r *SchemaResolver) ClearActiveAiProvider(ctx context.Context) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := r.GetApi(ctx).ClearActiveProvider(ctx); err != nil {
		return false, err
	}
	return true, nil
}
