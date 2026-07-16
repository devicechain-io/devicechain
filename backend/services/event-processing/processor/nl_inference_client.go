// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// inferRuleCandidateMutation carries one NL-authoring prompt to ai-inference's active provider
// (ADR-056). The candidate is the raw, untrusted model output — it is validated downstream by
// this service's own rules.Compile firewall, never trusted here. The mutation is authorized by
// the ai:infer authority the service-token client carries, and ai-inference resolves the tenant
// from the token's tenant header to gate external-routing consent.
const inferRuleCandidateMutation = `mutation($request: InferenceRequest!) {
  inferRuleCandidate(request: $request) { candidate model provider }
}`

// inferenceClient is the drafter's seam to ai-inference: it calls inferRuleCandidate over the
// ADR-044 service-token client (least-privilege ai:infer). It is dependency-inverted behind
// nldraft.Inferer so the drafter never depends on the transport.
type inferenceClient struct {
	client *svcclient.Client
	url    string
}

// NewInferenceClient builds the drafter's inference seam over a service-token client and
// ai-inference's GraphQL URL (wired in main). It returns the drafter's interface so the concrete
// adapter stays package-private.
func NewInferenceClient(client *svcclient.Client, url string) nldraft.Inferer {
	return &inferenceClient{client: client, url: url}
}

// Infer carries one prompt to the active provider under the given tenant and returns the raw
// candidate. Any error (not configured, no active provider, consent denied, or a transport
// failure) propagates to the drafter, which reports it as an unavailable result — the caller
// never sees a partial success.
func (c *inferenceClient) Infer(ctx context.Context, tenant, prompt, system string) (nldraft.InferOutput, error) {
	request := map[string]any{"prompt": prompt}
	if system != "" {
		request["system"] = system
	}
	vars := map[string]any{"request": request}

	var out struct {
		InferRuleCandidate struct {
			Candidate string `json:"candidate"`
			Model     string `json:"model"`
			Provider  string `json:"provider"`
		} `json:"inferRuleCandidate"`
	}
	if err := c.client.Query(ctx, c.url, tenant, inferRuleCandidateMutation, vars, &out); err != nil {
		return nldraft.InferOutput{}, fmt.Errorf("ai-inference: %w", err)
	}
	return nldraft.InferOutput{
		Candidate: out.InferRuleCandidate.Candidate,
		Model:     out.InferRuleCandidate.Model,
		Provider:  out.InferRuleCandidate.Provider,
	}, nil
}

// compile-time assertion that the client satisfies the drafter's Inferer contract.
var _ nldraft.Inferer = (*inferenceClient)(nil)
