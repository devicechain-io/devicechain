// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// ShedPriorityResolver resolves a tenant's ADR-063 shed priority (1–100) from
// user-management's tenantGovernance query, cached with the SAME hot-path protections
// as the ADR-023 ceilings (60s TTL, inflight dedupe, refresh-concurrency cap,
// fail-open) via the shared tenantResolver. event-sources reads it once per inbound
// message on a path where the tenant is only grammar-validated, so the concurrency cap
// is load-bearing here, not incidental — a flood of distinct tenants must not amplify
// into unbounded lookups.
//
// Fail-open serves DefaultShedPriority (a bronze-band value, never gold): a slow or
// unreachable authority degrades a tenant to "sheds like the fail-safe", never to
// "rides through like premium". A null shedPriority on the wire (the tenant and its
// tier both declared none) means the same thing — inherit the fail-safe.
type ShedPriorityResolver struct {
	*tenantResolver[int]
}

// NewShedPriorityResolver wires a shed-priority resolver to user-management at umURL
// over the service-token client. Mirrors NewServiceLimitResolver.
func NewShedPriorityResolver(client *svcclient.Client, umURL string) *ShedPriorityResolver {
	fetch := func(ctx context.Context, tenant string) (int, error) {
		return fetchShedPriority(ctx, client, umURL, tenant)
	}
	return &ShedPriorityResolver{
		tenantResolver: newTenantResolver(fetch, DefaultShedPriority, "shed-priority"),
	}
}

// Resolve returns the tenant's shed priority (1–100) and whether it was RESOLVED from a
// real fetched value (true) versus the fail-safe default served because the tenant has
// not been fetched yet (false). event-sources holds off shedding an unresolved tenant:
// the fail-safe default is a bronze band, and shedding a not-yet-known tenant on it
// could shed a gold tenant during the cold-cache window a floor-activation restart
// creates (ADR-063). A resolved value — including a genuinely null-on-the-wire tenant,
// which fetchShedPriority resolves to the default and CACHES — reads true and is shed
// normally.
func (r *ShedPriorityResolver) Resolve(tenant string) (int, bool) {
	return r.resolveOK(tenant)
}

// fetchShedPriority reads the scalar shedPriority from tenantGovernance. It does NOT
// use the Dimension/governanceQuery machinery (that is rate+burst-only) — shedPriority
// is a standalone scalar. A null value, or one outside the 1–100 band (unreachable
// through the API, which validates on write, but a direct DB write could park one),
// resolves to DefaultShedPriority: inheriting the fail-safe is the safe reading of an
// absent-or-junk priority, the same discipline serviceFetcher applies to a non-positive
// ceiling.
func fetchShedPriority(ctx context.Context, client *svcclient.Client, umURL, tenant string) (int, error) {
	var out struct {
		TenantGovernance struct {
			ShedPriority *int32 `json:"shedPriority"`
		} `json:"tenantGovernance"`
	}
	if err := client.Query(ctx, umURL, tenant, `query { tenantGovernance { shedPriority } }`, nil, &out); err != nil {
		return 0, err
	}
	return resolveShedPriority(out.TenantGovernance.ShedPriority), nil
}

// resolveShedPriority folds a wire shedPriority onto the fail-safe default. A null
// value (tenant and tier both declared none) or one outside the 1–100 band resolves to
// DefaultShedPriority. Split out pure so the rule is unit-testable without a live
// user-management (the concrete svcclient.Client is not an interface).
func resolveShedPriority(p *int32) int {
	if p == nil || *p < 1 || *p > 100 {
		return DefaultShedPriority
	}
	return int(*p)
}
