// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// Bounds are the configured per-call limits the resolver bakes into every provider it
// builds and exposes to the mutation layer. They come from the fail-closed service
// config (ADR-056 §4) so a caller cannot widen them.
type Bounds struct {
	// MaxOutputTokens caps the provider's generated output for a single call.
	MaxOutputTokens int
	// MaxPromptBytes caps an inbound prompt (+ system) — enforced by ValidatePrompt.
	MaxPromptBytes int
	// Timeout bounds a single provider call (applied as a context deadline).
	Timeout time.Duration
}

// Resolver turns an inference request into a ready-to-call Provider only after every
// fail-closed gate passes. It is the ONE place external routing is authorized; there
// is no path around it that reaches a Provider. It owns the menu resolution, the
// external-routing consent gate, the rate ceiling, the key resolution, and provider
// construction.
type Resolver struct {
	api    *model.Api
	facts  TenantFactsReader
	rate   RateGate
	bounds Bounds
	client *http.Client
}

// NewResolver builds a resolver over the provider store (for the grant tables + the
// secret store), the tenant-facts reader (fail-closed), the per-tenant rate gate, the
// configured bounds, and an optional HTTP client (nil ⇒ the package default). facts
// must be non-nil — main injects a deny-all reader when service-to-service auth is
// unconfigured, so the cascade never dereferences a nil reader and external routing
// stays denied. A nil rate gate leaves calls unmetered and is for tests only; main
// always wires one, since the platform default is itself a limit.
func NewResolver(api *model.Api, facts TenantFactsReader, rate RateGate, bounds Bounds, client *http.Client) *Resolver {
	if facts == nil {
		// Enforce the documented invariant fail-closed rather than deferring to a nil
		// dereference on the first tenant resolution: a nil reader denies everything.
		facts = NewDeniedTenantFactsReader("no tenant-facts reader configured")
	}
	return &Resolver{api: api, facts: facts, rate: rate, bounds: bounds, client: client}
}

// Resolved is a built provider plus the identity of the row it came from, so the
// mutation can report which provider answered without re-reading it.
type Resolved struct {
	Provider Provider
	// Token is the provider row's token (returned on the inference result).
	Token string
	// Kind is the provider kind (for observability).
	Kind string
}

// InferenceTimeout is the configured per-call deadline; the mutation applies it.
func (r *Resolver) InferenceTimeout() time.Duration { return r.bounds.Timeout }

// ValidatePrompt rejects an empty or oversized request before any provider is built or
// any external call is made. The combined system+prompt byte length is bounded by the
// configured MaxPromptBytes (fail-closed: a non-positive config would reject nothing,
// but Validate guarantees it positive).
func (r *Resolver) ValidatePrompt(system, prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("inference prompt is required")
	}
	if n := len(system) + len(prompt); n > r.bounds.MaxPromptBytes {
		return fmt.Errorf("inference prompt exceeds the maximum size (%d > %d bytes)", n, r.bounds.MaxPromptBytes)
	}
	return nil
}

// ResolveForTenant resolves the model a tenant's inference request should use — its
// tier's DEFAULT from the menu ADR-065 entitles it to — enforcing the external-routing
// consent gate and the per-tenant rate ceiling. This is the production NL-authoring
// path (Slice 1). Every step fails closed:
//
//  1. a tenant must be present (the caller reads it from the service-token header);
//  2. this instance must grant something to somebody (nothing granted ⇒ unavailable);
//  3. the tenant must be within its inference rate ceiling (ADR-056 §6 / ADR-023);
//  4. the tenant's facts (consent + tier) must be readable from user-management — a
//     failure is NOT permission, never a guess;
//  5. the tenant's menu must name a default model (see model.Api.MenuForTenant);
//  6. if that model routes outside the boundary (every GA kind), the tenant must have
//     opted in;
//  7. the model's API key must resolve (see build).
//
// ORDERING IS LOAD-BEARING, and this preserves the property the retired active-pointer
// read used to give for free. A purely LOCAL check (does this instance grant anything
// at all?) runs before anything on the network, so an instance where AI was never
// configured short-circuits without touching user-management — the "costs nothing when
// switched off" property.
//
// The rate gate then runs BEFORE the tenant-facts read, because that read is
// deliberately UNCACHED — it queries user-management on every call so a consent
// revocation takes effect immediately — which makes it an amplifier: a caller looping
// the draft door would otherwise turn one loop into one user-management query per
// iteration. An in-memory bucket in front sheds that loop for free.
//
// Charging before the facts read means an over-rate tenant that never opted in is told
// it is rate-limited rather than that it needs consent. That ordering costs nothing:
// the consent gate still runs on every call that is under the ceiling, so no data
// crosses the boundary without it — the security invariant is unchanged, only the
// message an abusive caller sees.
//
// Consent is checked AFTER the menu resolves, because which boundary question applies
// depends on which model answered: an in-boundary model needs no consent, and once the
// embedded model ships (ADR-056 §4) a tenant that consents to nothing still gets
// in-boundary drafting. Reading consent earlier would be free (it arrives on the same
// query) but applying it earlier would collapse the two axes ADR-056 decision 4 keeps
// apart.
func (r *Resolver) ResolveForTenant(ctx context.Context, tenant string) (*Resolved, error) {
	if strings.TrimSpace(tenant) == "" {
		return nil, fmt.Errorf("%w: no tenant in context", ErrUnavailable)
	}
	granted, err := r.api.AnyGrants(ctx)
	if err != nil {
		// Wrapped, not returned raw: this is a LOCAL store failure, and an unwrapped error
		// classifies as a provider fault — pointing an operator at their inference provider
		// when their own database is the thing that blipped. The detail is coarsened away
		// from the caller by tenantSafeError and logged server-side either way.
		return nil, fmt.Errorf("%w: could not read the provider grants: %v", ErrUnavailable, err)
	}
	if !granted {
		return nil, fmt.Errorf("%w: no provider is granted to any tier or tenant", ErrUnavailable)
	}
	// Charged once per resolution, and only past the cheap local gate above, so a call
	// that could never have been served does not burn the tenant's budget.
	if r.rate != nil && !r.rate.Allow(tenant) {
		return nil, ErrRateLimited
	}
	facts, err := r.facts.Facts(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("%w: could not read the tenant's AI settings: %v", ErrUnavailable, err)
	}
	menu, err := r.api.MenuForTenant(ctx, facts.TierToken)
	if err != nil {
		return nil, fmt.Errorf("%w: could not resolve the tenant's model menu: %v", ErrUnavailable, err)
	}
	if menu.Default == nil {
		// The caller gets one coarse answer either way (ErrUnavailable — an operator
		// resolves this, not the tenant), but the SERVER log has to name the right thing
		// to go look at, and this message used to blame the tier unconditionally. It can
		// just as easily be the tenant's own grants: an exception-only tenant on a tier
		// that sells no AI has a menu the tier had no part in. Sending an operator to
		// re-package `bronze` when the mark they need is on the tenant is a slow way to
		// find nothing.
		//
		// An empty menu means nothing is granted at all; a non-empty one with no default
		// means the mark was cleared or the marked model went out of service.
		if len(menu.Providers) == 0 {
			return nil, fmt.Errorf("%w: no model is granted to this tenant or to its tier %q",
				ErrUnavailable, facts.TierToken)
		}
		return nil, fmt.Errorf(
			"%w: %d model(s) are granted to this tenant or its tier %q, but none is marked default (or the marked one is disabled)",
			ErrUnavailable, len(menu.Providers), facts.TierToken)
	}
	chosen := menu.Default
	if model.IsExternalProviderKind(chosen.Kind) && !facts.ExternalEnabled {
		return nil, ErrConsentRequired
	}
	return r.build(ctx, chosen)
}

// ResolveProvider resolves a SPECIFIC provider by token for an operator smoke test
// (the ai:admin test-infer affordance). It deliberately does NOT apply the
// tenant-consent gate: the operator supplies their own test prompt against
// operator-owned instance config, so no tenant data crosses the boundary. A disabled
// provider is allowed — the operator validates the key BEFORE promoting the provider.
// The key must still resolve (build), so a test never calls out unauthenticated.
func (r *Resolver) ResolveProvider(ctx context.Context, token string) (*Resolved, error) {
	matches, err := r.api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: no provider with that token", ErrUnavailable)
	}
	return r.build(ctx, matches[0])
}

// build resolves the provider's API key (server-internal, fail-closed) and constructs
// the Provider impl for its kind. A missing or unresolvable key is unavailable — never
// a call without the intended credential. An unregistered kind is unavailable (it has
// no impl); the write path already rejects such a kind, so this is defense-in-depth.
func (r *Resolver) build(ctx context.Context, p *model.AIProvider) (*Resolved, error) {
	key, err := r.api.Secrets.Resolve(ctx, model.AIProviderSecretRef(p.ID))
	if err != nil {
		if errors.Is(err, secrets.ErrSecretNotFound) {
			return nil, fmt.Errorf("%w: the provider has no API key configured", ErrUnavailable)
		}
		return nil, fmt.Errorf("%w: could not resolve the provider key: %v", ErrUnavailable, err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: the provider has no API key configured", ErrUnavailable)
	}

	switch model.AIProviderKind(p.Kind) {
	case model.AIProviderKindAnthropic:
		provider, err := newClaudeProvider(r.client, p.Endpoint, p.ModelID, string(key), r.bounds.MaxOutputTokens)
		if err != nil {
			return nil, err
		}
		return &Resolved{Provider: provider, Token: p.Token, Kind: p.Kind}, nil
	default:
		return nil, fmt.Errorf("%w: no implementation for provider kind %q", ErrUnavailable, p.Kind)
	}
}
