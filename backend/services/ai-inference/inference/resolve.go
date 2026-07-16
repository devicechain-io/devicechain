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
// is no path around it that reaches a Provider. It owns the active-provider read, the
// external-routing consent gate, the key resolution, and provider construction.
type Resolver struct {
	api     *model.Api
	consent ConsentChecker
	rate    RateGate
	bounds  Bounds
	client  *http.Client
}

// NewResolver builds a resolver over the provider store (for the active pointer + the
// secret store), the consent checker (fail-closed), the per-tenant rate gate, the
// configured bounds, and an optional HTTP client (nil ⇒ the package default). consent
// must be non-nil — main injects a deny-all checker when service-to-service auth is
// unconfigured, so the cascade never dereferences a nil checker and external routing
// stays denied. A nil rate gate leaves calls unmetered and is for tests only; main
// always wires one, since the platform default is itself a limit.
func NewResolver(api *model.Api, consent ConsentChecker, rate RateGate, bounds Bounds, client *http.Client) *Resolver {
	if consent == nil {
		// Enforce the documented invariant fail-closed rather than deferring to a nil
		// dereference on the first external-kind resolution: a nil checker denies all
		// external routing.
		consent = NewDeniedConsentChecker("no consent checker configured")
	}
	return &Resolver{api: api, consent: consent, rate: rate, bounds: bounds, client: client}
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

// ResolveForTenant resolves the ACTIVE provider for a tenant's inference request,
// enforcing the external-routing consent gate and the per-tenant rate ceiling. This is
// the production NL-authoring path (Slice 1). Every step fails closed:
//
//  1. a tenant must be present (the caller reads it from the service-token header);
//  2. an active provider must be configured ("default of none" ⇒ unavailable);
//  3. the active provider must be enabled;
//  4. the tenant must be within its inference rate ceiling (ADR-056 §6 / ADR-023);
//  5. if the provider routes outside the boundary (every GA kind), the tenant must
//     have opted in — a consent-check error is treated as NOT permitted, never allowed;
//  6. the provider's API key must resolve (see build).
//
// ORDERING IS LOAD-BEARING. The active-provider read (local) runs before anything on
// the network, so a "default of none" instance short-circuits without touching
// user-management. The rate gate then runs BEFORE the consent check, because consent
// is deliberately UNCACHED — it queries user-management on every call so a revocation
// takes effect immediately — which makes it an amplifier: a caller looping the draft
// door would otherwise turn one loop into one user-management query per iteration. An
// in-memory bucket in front sheds that loop for free. The rate resolver's own lookups
// are out-of-band, capped, and cached, so the hot path stays local either way.
//
// Charging before consent means an over-rate tenant that never opted in is told it is
// rate-limited rather than that it needs consent. That ordering costs nothing: the
// consent gate still runs on every call that is under the ceiling, so no data crosses
// the boundary without it — the security invariant is unchanged, only the message an
// abusive caller sees.
func (r *Resolver) ResolveForTenant(ctx context.Context, tenant string) (*Resolved, error) {
	if strings.TrimSpace(tenant) == "" {
		return nil, fmt.Errorf("%w: no tenant in context", ErrUnavailable)
	}
	active, err := r.api.ActiveProvider(ctx)
	if err != nil {
		// Wrapped, not returned raw: this is a LOCAL store failure, and an unwrapped error
		// classifies as a provider fault — pointing an operator at their inference provider
		// when their own database is the thing that blipped. The detail is coarsened away
		// from the caller by tenantSafeError and logged server-side either way.
		return nil, fmt.Errorf("%w: could not read the active provider: %v", ErrUnavailable, err)
	}
	if active == nil {
		return nil, fmt.Errorf("%w: no active provider is configured", ErrUnavailable)
	}
	if !active.Enabled {
		return nil, fmt.Errorf("%w: the active provider is disabled", ErrUnavailable)
	}
	// Charged once per resolution, and only past the cheap local gates above, so a
	// call that could never have been served does not burn the tenant's budget.
	if r.rate != nil && !r.rate.Allow(tenant) {
		return nil, ErrRateLimited
	}
	if model.IsExternalProviderKind(active.Kind) {
		ok, err := r.consent.ExternalEnabled(ctx, tenant)
		if err != nil {
			return nil, fmt.Errorf("%w: could not verify external-routing consent: %v", ErrUnavailable, err)
		}
		if !ok {
			return nil, ErrConsentRequired
		}
	}
	return r.build(ctx, active)
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
