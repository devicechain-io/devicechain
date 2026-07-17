// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// TenantFacts are the per-call facts this service reads from user-management about a
// tenant. Both come from the SAME tenantGovernance query, deliberately: they are read
// on every inference call, and asking for them separately would double an
// intentionally-uncached round trip.
type TenantFacts struct {
	// ExternalEnabled is the ADR-056 §6 ai_external_enabled consent flag: may this
	// tenant's data leave the boundary. Operator-set, fail-closed.
	ExternalEnabled bool
	// TierToken is the tenant's ADR-065 packaging tier — what this service joins its
	// own grant tables against to resolve the tenant's model menu. The tier entity
	// itself lives in user-management and is never mirrored here.
	//
	// This is the one place the tier crosses the wire, and it does not contradict
	// ADR-065 slice 2's decision to keep the tier OFF the governance wire. That
	// decision is about the provenance of a resolved VALUE: a service enforcing a
	// ceiling has no business knowing why the number is what it is. This is a
	// different need — the tier is an IDENTITY used to resolve a SET, and
	// user-management cannot resolve that set on our behalf because it cannot see
	// providers at all (ai-inference is opt-in; user-management ships everywhere and
	// may never reference it). GraphQL field selection keeps the original property
	// intact: the other services reading tenantGovernance do not select tierToken.
	TierToken string
}

// TenantFactsReader reads the per-call tenant facts. An interface so the resolver can
// be driven without a user-management peer in tests, and so main can inject a
// fail-closed reader when service-to-service auth is unconfigured.
type TenantFactsReader interface {
	Facts(ctx context.Context, tenant string) (TenantFacts, error)
}

// serviceTenantFactsReader reads a tenant's facts from user-management's
// tenantGovernance query over a service token (ADR-044, mirroring the egress-limit
// fetcher). A query error propagates — the resolution cascade treats a failure as NOT
// permitted, so a transient user-management outage denies external routing rather than
// allowing it, and yields no menu rather than a guessed one.
type serviceTenantFactsReader struct {
	client *svcclient.Client
	umURL  string
}

// NewServiceTenantFactsReader builds a TenantFactsReader that reads tenantGovernance
// from user-management at umURL (its /graphql endpoint) over the given service-token
// client.
func NewServiceTenantFactsReader(client *svcclient.Client, umURL string) TenantFactsReader {
	return &serviceTenantFactsReader{client: client, umURL: umURL}
}

// tenantFactsQuery and tenantFactsResponse are the CROSS-SERVICE contract with
// user-management, named at package scope so a test can pin them against the shape
// user-management actually serves rather than against a copy. Both field names must
// match user-management's TenantGovernance type exactly.
//
// Drift fails LOUD, not silent, and it is worth being exact about why — the reverse
// claim stood here for a while and would have argued for defenses this code does not
// need. The failure is not a zero-value decode: graph-gophers validates the SELECTION
// against the schema, so a renamed or dropped field makes the whole query an error
// server-side, svcclient turns a non-empty `errors` array into a Go error rather than
// decoding `data`, and Facts() returns that error — which ResolveForFunction treats as
// ErrUnavailable and logs. The door does close for every tenant, but it says so.
//
// The names are still pinned by a test on each end (TestFactsQuerySelectsTheContractFields
// here, TestTierTokenIsServedUnderThatExactName in user-management) because a loud
// failure at RUNTIME on every tenant is a poor substitute for a red build.
const tenantFactsQuery = `query { tenantGovernance { aiExternalEnabled tierToken } }`

type tenantFactsResponse struct {
	TenantGovernance struct {
		AiExternalEnabled bool   `json:"aiExternalEnabled"`
		TierToken         string `json:"tierToken"`
	} `json:"tenantGovernance"`
}

func (r *serviceTenantFactsReader) Facts(ctx context.Context, tenant string) (TenantFacts, error) {
	var out tenantFactsResponse
	if err := r.client.Query(ctx, r.umURL, tenant, tenantFactsQuery, nil, &out); err != nil {
		return TenantFacts{}, err
	}
	return TenantFacts{
		ExternalEnabled: out.TenantGovernance.AiExternalEnabled,
		TierToken:       out.TenantGovernance.TierToken,
	}, nil
}

// deniedTenantFactsReader fails closed when service-to-service auth is not configured:
// neither consent nor the tenant's tier can be established, so nothing may be routed.
// main injects this when the shared service secret or the user-management endpoint is
// absent, so the NL-authoring path is cleanly unavailable rather than silently
// permissive. (The operator smoke test resolves a named provider directly and reads no
// tenant facts, so it still works.)
type deniedTenantFactsReader struct{ reason string }

// NewDeniedTenantFactsReader builds a fail-closed reader that denies every tenant
// resolution, carrying a reason for the surfaced error.
func NewDeniedTenantFactsReader(reason string) TenantFactsReader {
	return deniedTenantFactsReader{reason: reason}
}

func (d deniedTenantFactsReader) Facts(context.Context, string) (TenantFacts, error) {
	return TenantFacts{}, fmt.Errorf("tenant facts cannot be read: %s", d.reason)
}
