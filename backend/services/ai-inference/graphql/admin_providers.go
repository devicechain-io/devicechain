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

// AiProviderTierGrants returns every tier→provider grant on this instance: the
// packaging matrix (ADR-065 decision 10). Instance-global, including grants whose tier
// no longer exists — the console renders those as unknown so a stale grant is visible
// rather than filtered out of the only screen that could show it.
func (r *AdminResolver) AiProviderTierGrants(ctx context.Context) ([]*AIProviderTierGrantResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	grants, err := apiFrom(ctx).ListTierGrants(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AIProviderTierGrantResolver, 0, len(grants))
	for _, g := range grants {
		out = append(out, &AIProviderTierGrantResolver{M: g, C: ctx})
	}
	return out, nil
}

// AiProviderTenantGrants returns one tenant's additive grants (ADR-065 decision 7's
// audited exception).
func (r *AdminResolver) AiProviderTenantGrants(ctx context.Context, args struct {
	Tenant string
}) ([]*AIProviderTenantGrantResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	grants, err := apiFrom(ctx).ListTenantGrants(ctx, args.Tenant)
	if err != nil {
		return nil, err
	}
	out := make([]*AIProviderTenantGrantResolver, 0, len(grants))
	for _, g := range grants {
		out = append(out, &AIProviderTenantGrantResolver{M: g, C: ctx})
	}
	return out, nil
}

// GrantAiProviderToTier offers a provider to every tenant at a tier — an entitlement,
// and nothing about which model anything uses. Idempotent.
func (r *AdminResolver) GrantAiProviderToTier(ctx context.Context, args struct {
	Tier     string
	Provider string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := apiFrom(ctx).GrantProviderToTier(ctx, args.Tier, args.Provider); err != nil {
		return false, err
	}
	return true, nil
}

// RevokeAiProviderFromTier withdraws a tier's offer of a provider, reporting whether a
// grant was removed. Idempotent.
func (r *AdminResolver) RevokeAiProviderFromTier(ctx context.Context, args struct {
	Tier     string
	Provider string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	return apiFrom(ctx).RevokeProviderFromTier(ctx, args.Tier, args.Provider)
}

// SetAiTierDefault marks an already-granted provider as a tier's default model: what a
// tenant at that tier gets for a function it never assigned, PROVIDED its menu still
// includes that model. Demotes any previous default atomically. Refused when the pair is
// not granted — a default must be something the tier actually offers.
//
// It is a SEPARATE act from granting, and separate on purpose: grantAiProviderToTier
// carries no makeDefault flag, because fusing the two is what let a grant silently
// re-answer which model a tenant used. The AI packaging screen (/admin/ai-packaging,
// ADR-065 S5b) issues both mutations from one screen, so the separation is a property of
// the API without being a chore for the operator. Do not "fix" it by folding the flag
// back in.
//
// It is not the retired instance-wide active pointer returning, nor the retired platform
// baseline: the mark lives on a TIER GRANT, so it can never serve a tenant a model its
// tier was not sold (model.Api.ResolveModelForFunction).
func (r *AdminResolver) SetAiTierDefault(ctx context.Context, args struct {
	Tier     string
	Provider string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := apiFrom(ctx).SetTierDefault(ctx, args.Tier, args.Provider); err != nil {
		return false, err
	}
	return true, nil
}

// ClearAiTierDefault marks none of a tier's grants as its default, leaving every grant in
// place. Idempotent. Afterwards a tenant at that tier which assigned no model for a
// function resolves to none and must choose explicitly.
func (r *AdminResolver) ClearAiTierDefault(ctx context.Context, args struct {
	Tier string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := apiFrom(ctx).ClearTierDefault(ctx, args.Tier); err != nil {
		return false, err
	}
	return true, nil
}

// GrantAiProviderToTenant adds a provider to ONE tenant's menu over and above its
// tier. Additive-only by construction: there is no matching deny, so this can never
// take away what the tenant's tier offers. Idempotent.
func (r *AdminResolver) GrantAiProviderToTenant(ctx context.Context, args struct {
	Tenant   string
	Provider string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := apiFrom(ctx).GrantProviderToTenant(ctx, args.Tenant, args.Provider); err != nil {
		return false, err
	}
	return true, nil
}

// RevokeAiProviderFromTenant removes a tenant's additive grant, reporting whether one
// was removed. This withdraws an exception, not an entitlement — what the tier offers
// is untouched. Idempotent.
func (r *AdminResolver) RevokeAiProviderFromTenant(ctx context.Context, args struct {
	Tenant   string
	Provider string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	return apiFrom(ctx).RevokeProviderFromTenant(ctx, args.Tenant, args.Provider)
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
