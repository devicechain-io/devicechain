// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The instance-scoped provider surface (ADR-056 §4, re-homed to the admin plane by
// ADR-065). Every resolver here gates on ai:admin — a SYSTEM-tier authority, so it
// can only ever be satisfied by an identity or service token, never by a tenant
// access token however privileged (core/auth). The /admin/graphql endpoint already
// refuses an access token outright; these checks are the second layer.

// AiProvider looks up a single provider by its token. Returns nil when not found.
func (r *AdminResolver) AiProvider(ctx context.Context, args struct {
	Token string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	matches, err := apiFrom(ctx).AIProvidersByToken(ctx, []string{args.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &AIProviderResolver{M: *matches[0], C: ctx}, nil
}

// AiProviders searches providers by criteria.
func (r *AdminResolver) AiProviders(ctx context.Context, args struct {
	Criteria model.AIProviderSearchCriteria
}) (*AIProviderSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	found, err := apiFrom(ctx).AIProviders(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &AIProviderSearchResultsResolver{M: *found, C: ctx}, nil
}

// AiProviderKinds returns the registered provider-kind vocabulary (for a console
// picker). Read-gated like the rest of the surface.
func (r *AdminResolver) AiProviderKinds(ctx context.Context) ([]string, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	return model.ProviderKinds(), nil
}

// ActiveAiProvider returns THE active provider, or nil when none is active.
func (r *AdminResolver) ActiveAiProvider(ctx context.Context) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	active, err := apiFrom(ctx).ActiveProvider(ctx)
	if err != nil {
		return nil, err
	}
	if active == nil {
		return nil, nil
	}
	return &AIProviderResolver{M: *active, C: ctx}, nil
}

// CreateAiProvider creates a new provider.
func (r *AdminResolver) CreateAiProvider(ctx context.Context, args struct {
	Request model.AIProviderCreateRequest
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	created, err := apiFrom(ctx).CreateAIProvider(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *created, C: ctx}, nil
}

// UpdateAiProvider updates the provider with the given (current) token.
// expectedUpdatedAt, when supplied, is an optimistic-concurrency precondition.
func (r *AdminResolver) UpdateAiProvider(ctx context.Context, args struct {
	Token             string
	Request           model.AIProviderCreateRequest
	ExpectedUpdatedAt *string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	updated, err := apiFrom(ctx).UpdateAIProvider(ctx, args.Token, &args.Request, args.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *updated, C: ctx}, nil
}

// DeleteAiProvider deletes the provider with the given token.
func (r *AdminResolver) DeleteAiProvider(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	return apiFrom(ctx).DeleteAIProvider(ctx, args.Token)
}

// SetActiveAiProvider promotes the provider with the given token to THE active one.
func (r *AdminResolver) SetActiveAiProvider(ctx context.Context, args struct {
	Token string
}) (*AIProviderResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	active, err := apiFrom(ctx).SetActiveProvider(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &AIProviderResolver{M: *active, C: ctx}, nil
}

// ClearActiveAiProvider clears the active provider (return to "default of none").
func (r *AdminResolver) ClearActiveAiProvider(ctx context.Context) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := apiFrom(ctx).ClearActiveProvider(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// TestAiProvider runs an operator-supplied prompt through a SPECIFIC provider (by
// token) as a smoke test — the affordance to validate a provider's endpoint + key
// before promoting it. It does NOT apply the tenant-consent gate (an operator
// testing operator-owned instance config with an operator prompt — no tenant data
// crosses the boundary), but the provider's key must still resolve, so a test never
// calls out unauthenticated.
//
// Unlike inferRuleCandidate it keeps the DETAILED error: the caller is an operator
// on the admin plane, the same trust tier as the config being tested, and a smoke
// test whose whole purpose is to say why an endpoint rejected the call is useless
// if it collapses to "unavailable".
func (r *AdminResolver) TestAiProvider(ctx context.Context, args struct {
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
	return runInference(ctx, r.Inference, r.Metrics, resolved, args.Request)
}
