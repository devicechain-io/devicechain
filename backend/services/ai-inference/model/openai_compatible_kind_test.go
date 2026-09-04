// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

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

	_, err = api.UpdateAIProvider(ctx, "vllm", openAIReq("vllm", strp("")), nil)
	assert.ErrorIs(t, err, ErrEndpointRequired)
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
