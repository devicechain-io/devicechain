// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openAIReq(token string, endpoint *string) *AIProviderCreateRequest {
	return &AIProviderCreateRequest{
		Token:    token,
		Name:     strp("Local vLLM"),
		Kind:     string(AIProviderKindOpenAICompatible),
		Endpoint: endpoint,
		Model:    "qwen2.5-coder",
		Enabled:  true,
	}
}

// The kind is registered, so a well-formed provider is accepted. Without this the
// refusal tests below would pass just as well against a kind that was never added.
func TestOpenAICompatibleProviderIsAccepted(t *testing.T) {
	api := newTestApi(t)
	created, err := api.CreateAIProvider(context.Background(),
		openAIReq("vllm", strp("http://vllm.ai.svc.cluster.local:8000")))
	require.NoError(t, err)
	assert.Equal(t, string(AIProviderKindOpenAICompatible), created.Kind)
	assert.Equal(t, "http://vllm.ai.svc.cluster.local:8000", created.Endpoint)
}

// 🔴 THE ENDPOINT IS THE PROVIDER. A row of this kind with no endpoint is unusable — it
// would splice a bare path onto nothing — so it is refused where the operator can see the
// mistake, on the screen that made it, rather than at the first inference call.
func TestOpenAICompatibleProviderRequiresAnEndpoint(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()

	_, err := api.CreateAIProvider(ctx, openAIReq("vllm", nil))
	assert.ErrorIs(t, err, ErrEndpointRequired)

	_, err = api.CreateAIProvider(ctx, openAIReq("vllm", strp("   ")))
	assert.ErrorIs(t, err, ErrEndpointRequired)
}

// An update may not strip the endpoint back off a provider that needs one, which is the
// half of the rule a create-only check would leave open.
func TestOpenAICompatibleProviderCannotHaveItsEndpointCleared(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()
	_, err := api.CreateAIProvider(ctx, openAIReq("vllm", strp("http://vllm:8000")))
	require.NoError(t, err)

	// Both spellings of "take the endpoint away" are refused: an explicit NULL, which is
	// what the clear means everywhere else, and the empty STRING a form sends for a
	// field the operator blanked. They travel different paths through the fold — one
	// yields a nil pointer, the other a pointer to "" — so a check on either alone says
	// nothing about the other.
	_, err = api.UpdateAIProvider(ctx, "vllm", &AIProviderUpdateRequest{
		Endpoint: dcgraphql.ClearedString(),
	}, nil)
	assert.ErrorIs(t, err, ErrEndpointRequired)

	_, err = api.UpdateAIProvider(ctx, "vllm", &AIProviderUpdateRequest{
		Endpoint: dcgraphql.OptionalStringOf(""),
	}, nil)
	assert.ErrorIs(t, err, ErrEndpointRequired)
}

// 🔴 NAMING ONLY THE KIND RE-VALIDATES THE STORED ENDPOINT.
//
// This is the input class the rest of the suite misses, and the miss is structural rather
// than accidental: the partial-update harness seeds an endpoint precisely so that driving
// `kind` on its own produces a LEGAL request, which is what lets it drive the field at all.
// So nothing exercises the request this guarantee is actually about — one side of the pair
// sent, with the stored other side invalid for it — and a fold that checked
// validateEndpointForKind only when the request CARRIED an endpoint would pass every other
// test here.
//
// What that mutant lets through is a provider stored with no address. It fails closed at
// USE, so nothing is called unauthenticated — but the write-time refusal is what the schema
// comment promises and what puts the mistake on the operator's screen instead of in a
// runtime error days later.
func TestUpdateAIProvider_ChangingOnlyTheKindRevalidatesTheStoredEndpoint(t *testing.T) {
	api := newTestApi(t)
	ctx := context.Background()
	// An anthropic provider, which legitimately carries NO endpoint.
	_, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil))
	require.NoError(t, err)

	// `kind` alone, onto the kind that is DEFINED by its address.
	_, err = api.UpdateAIProvider(ctx, "prov-a", &AIProviderUpdateRequest{
		Kind: dcgraphql.OptionalStringOf(string(AIProviderKindOpenAICompatible)),
	}, nil)
	assert.ErrorIs(t, err, ErrEndpointRequired,
		"a re-point onto a kind with no built-in base URL was accepted against a STORED empty "+
			"endpoint, so the provider is now stored unusable")

	// Nothing was written: the refusal is total.
	found, ferr := api.AIProvidersByToken(ctx, []string{"prov-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1)
	assert.Equal(t, string(AIProviderKindAnthropic), found[0].Kind,
		"the refused update still moved the kind")

	// THE COUNTERWEIGHT: the same re-point WITH an endpoint succeeds, so the refusal above
	// is about the pair rather than about the kind being unreachable.
	_, err = api.UpdateAIProvider(ctx, "prov-a", &AIProviderUpdateRequest{
		Kind:     dcgraphql.OptionalStringOf(string(AIProviderKindOpenAICompatible)),
		Endpoint: dcgraphql.OptionalStringOf("http://vllm:8000"),
	}, nil)
	require.NoError(t, err, "a re-point that names the endpoint the new kind needs was refused")
}

// The kind that HAS a built-in base URL is unaffected: requiring an endpoint of every
// kind would break the shipped one.
func TestAnthropicProviderStillNeedsNoEndpoint(t *testing.T) {
	api := newTestApi(t)
	_, err := api.CreateAIProvider(context.Background(), claudeReq("primary", nil))
	require.NoError(t, err)
}

// 🔴 THE CONSENT POSTURE IS THE DECISION THIS KIND FORCES. The same wire protocol serves
// an in-cluster vLLM and DeepSeek's public API, and only the ENDPOINT tells them apart —
// which the kind-keyed allowlist cannot read. So it stays EXTERNAL, and a tenant's prompt
// never reaches it without an opt-in. Classifying it in-boundary would mean pointing a
// "self-hosted" provider at a third-party API silently skipped the gate.
func TestOpenAICompatibleRoutingRequiresConsent(t *testing.T) {
	if !IsExternalProviderKind(string(AIProviderKindOpenAICompatible)) {
		t.Fatal("openai-compatible is classified in-boundary, so a tenant's prompt could reach " +
			"a third-party endpoint with no opt-in the moment an operator typed one")
	}
}

// The picker the console renders is the registered vocabulary, so the new kind has to be
// in it — a kind that can be stored but never chosen is only reachable by hand.
func TestProviderKindsOffersTheOpenAICompatibleKind(t *testing.T) {
	assert.Contains(t, ProviderKinds(), string(AIProviderKindOpenAICompatible))
}
