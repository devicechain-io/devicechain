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

// externalProviderKinds are the kinds that route an inference call OUTSIDE the
// deployment boundary (a hosted third-party API). A tenant must have OPTED IN
// (ADR-056 §6, the ai_external_enabled governance flag) before its authoring
// requests may use one — the resolution cascade enforces this fail-closed. A future
// self-hosted / in-boundary kind is deliberately NOT listed here, so it needs no
// external-routing consent (its data never leaves the boundary); at GA every
// registered kind is external.
var externalProviderKinds = map[AIProviderKind]struct{}{
	AIProviderKindAnthropic: {},
}

// IsExternalProviderKind reports whether s names a kind that routes outside the
// deployment boundary and therefore requires per-tenant external-routing consent.
// An unregistered kind returns false — it has no impl and is rejected earlier, so
// this never gates on an unknown value.
func IsExternalProviderKind(s string) bool {
	_, ok := externalProviderKinds[AIProviderKind(s)]
	return ok
}

// validateProviderKind rejects a kind outside the registered vocabulary.
func validateProviderKind(k string) error {
	if !ValidProviderKind(k) {
		return fmt.Errorf("%w: %q (known: %v)", ErrUnknownProviderKind, k, ProviderKinds())
	}
	return nil
}
