// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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
	// AIProviderKindOpenAICompatible routes to any endpoint speaking the OpenAI
	// chat-completions API: vLLM, Ollama, DeepSeek, llama.cpp's server, a gateway in
	// front of several of them.
	//
	// 🔑 IT IS DEFINED BY ITS ENDPOINT, NOT BY A VENDOR, which is the one way it
	// differs structurally from every other kind here. `anthropic` has a built-in
	// default base URL and an Endpoint that merely overrides it; this kind has no
	// meaningful default, because the whole point of it is that the operator says
	// where the model lives. So the Endpoint is REQUIRED for it — enforced at write
	// (validateEndpointForKind) so an unusable provider never enters the store, and
	// again at build, which is the seam a row written by any other path would have to
	// pass.
	AIProviderKindOpenAICompatible AIProviderKind = "openai-compatible"
)

// registeredProviderKinds is the set of kinds with a shipped Provider impl. A kind is
// accepted here only once something can serve it — failing closed keeps a provider no
// impl could call out of the store — so an entry here and a case in the resolver's
// build() are added together.
var registeredProviderKinds = map[AIProviderKind]struct{}{
	AIProviderKindAnthropic:        {},
	AIProviderKindOpenAICompatible: {},
}

// ErrEndpointRequired is returned when a kind that has no built-in base URL is written
// without one.
var ErrEndpointRequired = errors.New("this provider kind is defined by its endpoint, so an " +
	"endpoint is required")

// endpointRequiredKinds are the kinds with NO built-in default base URL: the endpoint
// is not an override for them, it is the address of the model.
//
// 🔴 IT IS AN ALLOWLIST OF "NEEDS ONE", NOT A DENYLIST, so a kind added later without
// being considered here is treated as having a default — which is only ever true of a
// kind whose impl carries one, and an impl with no default refuses at build anyway. The
// two checks are deliberately doubled: this one keeps an unusable row out of the store
// (where an operator sees the mistake immediately, on the screen that made it), and
// build's keeps a row that got in some other way from producing an unauthenticated call
// to whatever a bare path resolves to.
var endpointRequiredKinds = map[AIProviderKind]struct{}{
	AIProviderKindOpenAICompatible: {},
}

// validateEndpointForKind rejects a kind that needs an endpoint and was given none.
func validateEndpointForKind(kind AIProviderKind, endpoint string) error {
	if _, needs := endpointRequiredKinds[kind]; !needs {
		return nil
	}
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("%w: %q has no built-in API base URL", ErrEndpointRequired, kind)
	}
	return nil
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
//
// 🔴 `openai-compatible` IS DELIBERATELY NOT HERE, AND IT IS THE CASE THAT MAKES THE
// POINT. It is the one kind that can be EITHER posture: the same wire protocol serves a
// vLLM pod in the cluster and DeepSeek's public API, and the row cannot be told apart by
// its Kind — only by its Endpoint, which is a URL an operator typed. Classifying it as
// in-boundary would mean a tenant's prompt could reach a third-party API with no opt-in
// the moment someone pointed a "self-hosted" provider at one, and the tenant would never
// have been asked. So the kind stays external and a self-hosted deployment costs its
// tenants one opt-in they can give once.
//
// The finer answer — consent decided per ENDPOINT rather than per KIND — is a real
// design and is NOT being smuggled in here. It needs the deployment to be able to say
// which addresses are inside its own boundary, which is an operator-configured claim
// (the egress allowlist is the shape it would take), not something this map can express.
// Until that exists, treating the ambiguous kind as external is the only honest reading.
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
