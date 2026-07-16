// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"sort"
)

// AIProviderKind is a registered inference-provider kind. It selects the Provider
// implementation the ai-inference service calls (slice 0c). It is DeviceChain's
// stable token for a provider family, decoupled from any SDK, so the value is
// API-stable.
type AIProviderKind string

const (
	// AIProviderKindAnthropic routes to the Anthropic Claude API. The only Kind with
	// a shipped Provider impl at GA (frontier-only-behind-opt-in, ADR-056).
	AIProviderKindAnthropic AIProviderKind = "anthropic"
)

// registeredProviderKinds is the set of kinds with a shipped Provider impl. It is
// intentionally minimal at GA: `openai-compatible` / `selfhosted` are reserved by
// the design (the entity already carries an Endpoint for them) but are NOT accepted
// until their impl lands — failing closed keeps an unusable provider, one no impl
// could serve, out of the store. Adding one later adds an entry here + its impl.
var registeredProviderKinds = map[AIProviderKind]struct{}{
	AIProviderKindAnthropic: {},
}

// ErrUnknownProviderKind is returned when a create/update names a kind outside the
// registered vocabulary.
var ErrUnknownProviderKind = errors.New("provider kind is not one of the registered inference providers")

// ValidProviderKind reports whether s names a registered provider kind.
func ValidProviderKind(s string) bool {
	_, ok := registeredProviderKinds[AIProviderKind(s)]
	return ok
}

// ProviderKinds returns the registered provider-kind vocabulary, sorted. The GraphQL
// surface exposes it so the console offers a picker rather than a free-text field.
func ProviderKinds() []string {
	out := make([]string, 0, len(registeredProviderKinds))
	for k := range registeredProviderKinds {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// internalProviderKinds are the kinds that route an inference call INSIDE the
// deployment boundary (a self-hosted / in-cluster model): tenant data never leaves,
// so no external-routing consent is required. This is an explicit ALLOWLIST, and
// IsExternalProviderKind is its complement — a kind is treated as EXTERNAL (consent
// required, ADR-056 §6) unless it is affirmatively registered here as in-boundary.
//
// The direction is deliberate and fail-closed: a FUTURE PR that adds an external
// provider kind to the resolver's build() switch but forgets to touch this map still
// requires consent — it does NOT silently skip the opt-in gate (the cross-file seam
// bug an external denylist would invite; cf. ADR-062 S5). The cost of the inversion is
// only that a genuinely in-boundary kind must be added here to skip consent, an
// affirmative, reviewable act. Empty at GA: every registered kind is external.
var internalProviderKinds = map[AIProviderKind]struct{}{}

// IsExternalProviderKind reports whether s routes outside the deployment boundary and
// therefore requires per-tenant external-routing consent. Fail-closed: any kind not
// explicitly registered as in-boundary (including an unknown value) is external.
func IsExternalProviderKind(s string) bool {
	_, internal := internalProviderKinds[AIProviderKind(s)]
	return !internal
}

// validateProviderKind rejects a kind outside the registered vocabulary.
func validateProviderKind(k string) error {
	if !ValidProviderKind(k) {
		return fmt.Errorf("%w: %q (known: %v)", ErrUnknownProviderKind, k, ProviderKinds())
	}
	return nil
}
