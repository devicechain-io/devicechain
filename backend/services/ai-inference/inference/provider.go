// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package inference is the fail-closed inference seam of the ai-inference service
// (ADR-056). It owns the Provider interface (the provider-agnostic call contract),
// the shipped Claude implementation, and — the security core — the resolution
// cascade that turns an inference request into a ready-to-call provider only after
// every gate passes: an active provider is configured and enabled, the tenant has
// consented to external routing (when the provider routes outside the boundary), and
// the provider's API key resolves. Any gap fails CLOSED (ErrUnavailable /
// ErrConsentRequired) — there is no path that calls out without consent.
//
// The service holds NO ambient authority over tenant data (ADR-047): a provider
// returns only a candidate string, which flows back to the caller (event-processing,
// Slice 1) and through the deterministic rules.Compile firewall carrying the human's
// own token. "AI proposes, the CEL compiler disposes" — the model is never in the
// replay-correct DETECT/REACT path.
package inference

import (
	"context"
	"errors"
)

// ErrUnavailable is the fail-closed sentinel returned when inference cannot be served
// for a benign, operator-facing reason: no active provider, a disabled provider, a
// missing key, an unbuilt kind, or no tenant in context. It is deliberately coarse —
// the caller surfaces "unavailable" without leaking which gate tripped to an
// unprivileged path.
var ErrUnavailable = errors.New("inference is unavailable")

// ErrConsentRequired is returned when the resolved provider routes outside the
// deployment boundary and the tenant has NOT opted in to external AI routing
// (ADR-056 §6). Distinct from ErrUnavailable so the console can prompt the tenant to
// opt in rather than tell an operator to fix configuration.
var ErrConsentRequired = errors.New("tenant has not opted in to external AI routing")

// Input is a single inference request: an optional system prompt and the user prompt.
// The output-token cap and the endpoint are baked into the resolved Provider (from the
// service config + the provider row), not carried here, so a caller cannot widen them.
type Input struct {
	// System is an optional system prompt (instructions/persona). Empty omits it.
	System string
	// Prompt is the user prompt. Required (the resolver validates it non-empty and
	// size-bounded before a Provider is built).
	Prompt string
}

// Output is a single inference result: the raw completion text and the model that
// produced it. The candidate is returned verbatim to the caller — it is validated by
// the deterministic compiler downstream, never trusted here.
type Output struct {
	// Candidate is the provider's generated text (the rule candidate, for NL authoring).
	Candidate string
	// Model is the model id the provider reported answering with (for observability).
	Model string
}

// Provider is the provider-agnostic inference call. A single implementation ships at
// GA (Claude); the entity's Kind + Endpoint select and target it, so a self-hosted or
// openai-compatible model lands as a new implementation with no change above this line
// (ADR-056 — "still need to deploy our own models on the same interface").
type Provider interface {
	// Infer runs one prompt and returns the completion. The call is bounded by ctx's
	// deadline (the caller sets it from the configured inference timeout). It never
	// logs or returns the API key.
	Infer(ctx context.Context, in Input) (Output, error)
}

// ConsentChecker reports whether a tenant has opted in to external AI routing. It is
// the ai-inference side of the ADR-056 §6 governance flag, read from user-management
// over a service token. It MUST fail closed: the resolution cascade treats any error
// as "not permitted", so an unconfigured or unreachable checker denies external
// routing rather than allowing it.
type ConsentChecker interface {
	ExternalEnabled(ctx context.Context, tenant string) (bool, error)
}
