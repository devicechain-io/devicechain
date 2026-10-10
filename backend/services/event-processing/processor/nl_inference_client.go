// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
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
// svcclient bound (10s) is far below ai-inference's inference timeout (60s by default, up to
// config.AiInferenceMaxCallTimeout), so a default client cut slow models off first and turned
// ai-inference's honest "timed out" into an opaque transport error the author read as an outage.
// This client outwaits the callee's CEILING (config.AiInferenceCallerTimeout), so whatever the
// operator configured, ai-inference's own answer is the one that arrives.
func NewInferenceClient(umCfg config.UserManagementConfiguration, secret, url string) nldraft.Inferer {
	client := svcclient.New(umCfg, secret, "event-processing", []string{string(auth.AIInfer)},
		svcclient.WithQueryTimeout(config.AiInferenceCallerTimeout))
	return &inferenceClient{client: client, url: url}
}

// timedOutMarker is the stable prefix of ai-inference's ErrTimedOut — the same WIRE CONTRACT,
// and the same benign-on-drift reasoning, as rateLimitMarker below.
const timedOutMarker = "inference timed out"

// rateLimitMarker is the stable substring ai-inference's ErrRateLimited carries (ADR-056 §6 /
// ADR-023). It is a WIRE CONTRACT between the two services: svcclient surfaces a GraphQL error
// only as its message text, so classifying a transient rate-limit — the one inference outcome the
// author fixes by waiting rather than by calling an operator — means matching on it here.
//
// Deliberately a prefix of the sentinel's text rather than the whole message, so wording may be
// refined without breaking the match. If it ever does drift, the failure is BENIGN by
// construction: the error falls through to the generic unavailable reason, which is exactly
// today's behaviour — a rate-limited author sees a vaguer message, never a wrong outcome. The
// alternative (teaching svcclient to decode GraphQL error extensions) is a core change for every
// service and is the right home for this if a second caller ever needs typed error codes.
const rateLimitMarker = "inference rate limit exceeded"

// Infer carries one prompt to the active provider under the given tenant and returns the raw
// candidate. Any error (not configured, no active provider, consent denied, rate limited, or a
// transport failure) propagates to the drafter, which reports it as an unavailable result — the
// caller never sees a partial success. A rate-limit rejection is classified into
// nldraft.ErrRateLimited and a timeout (either side's) into nldraft.ErrTimedOut, so the drafter
// can report those transient outcomes without knowing the transport; every other error stays
// opaque to it.
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
		if strings.Contains(err.Error(), rateLimitMarker) {
			// Wrapped, not replaced: the drafter matches the sentinel while the underlying
			// detail still reaches the server-side log.
			return nldraft.InferOutput{}, fmt.Errorf("ai-inference: %w: %v", nldraft.ErrRateLimited, err)
		}
		if strings.Contains(err.Error(), timedOutMarker) || isTimeout(err) {
			// Either ai-inference said its provider overran, or this call's own bound fired
			// first. The author's remedy is the same — try again — and neither is an outage.
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
