// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-microservice/aiwire"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// inferRuleCandidateMutation carries one NL-authoring prompt to ai-inference (ADR-056), which
// picks the model itself: the one the tenant assigned to the rule-drafting FUNCTION, or its
// tier's default model, in either case only if the tenant is entitled to it (ADR-065). The request
// deliberately carries no model or function: which job this is follows from the mutation being
// called, and a caller able to name either would be choosing its own entitlement.
// The candidate is the raw, untrusted model output — it is validated downstream by
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

// NewInferenceClient builds the drafter's inference seam: a service-token client (least-privilege
// ai:infer, minted from user-management at umCfg with the shared secret) aimed at ai-inference's
// GraphQL URL. It returns the drafter's interface so the concrete adapter stays package-private.
//
// It builds the svcclient itself rather than taking one, because the client's TIMEOUT is part of
// this seam's contract and a caller-supplied client would be free to get it wrong. The default
// svcclient bound (10s) is far below ai-inference's inference timeout (40s by default, up to
// config.AiInferenceMaxCallTimeout), so a default client cut slow models off first and turned
// ai-inference's honest "timed out" into an opaque transport error the author read as an outage.
// This client outwaits the callee's CEILING (config.AiInferenceCallerTimeout), so whatever the
// operator configured, ai-inference's own answer is the one that arrives.
func NewInferenceClient(umCfg config.UserManagementConfiguration, secret, url string) nldraft.Inferer {
	client := svcclient.New(umCfg, secret, "event-processing", []string{string(auth.AIInfer)},
		svcclient.WithQueryTimeout(config.AiInferenceCallerTimeout))
	return &inferenceClient{client: client, url: url}
}

// Infer carries one prompt to the active provider under the given tenant and returns the raw
// candidate. Any error (not configured, no active provider, consent denied, rate limited, or a
// transport failure) propagates to the drafter, which reports it as an unavailable result — the
// caller never sees a partial success. Two transient outcomes are classified so the drafter can
// report them without knowing the transport:
//
//   - rate limited → nldraft.ErrRateLimited, on ai-inference's extensions.code
//   - timed out → nldraft.ErrTimedOut, on ai-inference's extensions.code, or this call's own
//     bound (or ctx's deadline) firing while ai-inference was being asked
//
// Classification reads the GraphQL error's CODE (svcclient.GraphQLError.Codes), never its
// message text, which ai-inference is free to reword. A failure to obtain the service token is
// checked FIRST and stays opaque: user-management being slow is not the model being slow, and
// ai-inference was never asked.
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
		// Wrapped, not replaced, in every arm: the drafter matches the sentinel while the
		// underlying detail still reaches the server-side log.
		switch {
		case errors.Is(err, svcclient.ErrServiceToken):
			return nldraft.InferOutput{}, fmt.Errorf("ai-inference: %w", err)
		case hasCode(err, aiwire.CodeRateLimited):
			return nldraft.InferOutput{}, fmt.Errorf("ai-inference: %w: %v", nldraft.ErrRateLimited, err)
		case hasCode(err, aiwire.CodeTimedOut) || isTimeout(err):
			return nldraft.InferOutput{}, fmt.Errorf("ai-inference: %w: %v", nldraft.ErrTimedOut, err)
		}
		return nldraft.InferOutput{}, fmt.Errorf("ai-inference: %w", err)
	}
	return nldraft.InferOutput{
		Candidate: out.InferRuleCandidate.Candidate,
		Model:     out.InferRuleCandidate.Model,
		Provider:  out.InferRuleCandidate.Provider,
	}, nil
}

// hasCode reports whether ai-inference answered with a GraphQL error carrying code.
func hasCode(err error, code string) bool {
	var gqlErr *svcclient.GraphQLError
	return errors.As(err, &gqlErr) && slices.Contains(gqlErr.Codes, code)
}

// isTimeout reports a transport-level timeout: the client's own bound, or a deadline on ctx.
// A cancelled context (the author went away) is deliberately NOT a timeout.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// compile-time assertion that the client satisfies the drafter's Inferer contract.
var _ nldraft.Inferer = (*inferenceClient)(nil)
