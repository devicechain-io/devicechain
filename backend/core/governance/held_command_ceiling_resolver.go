// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// DefaultHeldCommandCeiling is the last-resort bound on how many commands one tenant
// may have parked in the HELD state — the state command-delivery puts a command in
// when the platform is DELIBERATELY withholding dispatch because the device is known
// absent. It is a large-but-finite number on purpose: an offline fleet's backlog
// accumulates there and can sit for days, so the failure this bounds is not a burst,
// it is an unbounded queue growing quietly on behalf of a tenant whose devices are
// simply not there.
//
// It exists as a floor under a caller that supplies a non-positive default (see
// NewHeldCommandCeilingResolver), not as the platform's answer. The platform's answer
// is the enforcing service's own configured number, exactly as with the ADR-023
// ceilings: user-management resolves the cascade to "override, else tier, else
// nothing", and the service folds that onto its own Helm-configured default.
//
// 🔴 There is deliberately no value meaning UNLIMITED, at any level. ADR-023's rule —
// missing means a real limit, never unmetered — applies with more force here than to a
// rate, because nothing drains a HELD backlog on its own: an unbounded one is a
// tenant-triggered, operator-invisible growth in durable storage.
const DefaultHeldCommandCeiling = 10000

// HeldCommandCeilingResolver resolves a tenant's HELD-command ceiling (ADR-023
// governance, packaged per ADR-065 tier) from user-management's tenantGovernance
// query, cached with the SAME hot-path protections as the ADR-023 rate ceilings and
// the ADR-063 shed priority (60s TTL, inflight dedupe, refresh-concurrency cap,
// fail-open) via the shared tenantResolver.
//
// It is a CEILING (how much), not a preference (who degrades first) — so unlike
// ShedPriorityResolver it has no band scale and no notion of a class that is never
// shed. And it is a standalone SCALAR, not a rate+burst Dimension: a held backlog has
// no burst and no per-second unit, and forcing it through the Dimension machinery
// would fabricate both.
//
// Fail-open serves the caller's configured default, which is itself a real ceiling —
// so a slow or unreachable user-management degrades a tenant to "bounded at the
// service default", never to "may hold without limit".
type HeldCommandCeilingResolver struct {
	*tenantResolver[int]
}

// NewHeldCommandCeilingResolver wires a held-command-ceiling resolver to
// user-management at umURL over the service-token client, defaulting an uncached or
// unresolvable tenant to def.
//
// Unlike NewShedPriorityResolver — which hardcodes DefaultShedPriority because a shed
// BAND is a platform-wide notion every service must read identically — this
// constructor takes def, the way NewServiceLimitResolver does. How large a held
// backlog is tolerable is the enforcing service's own number, configured in its Helm
// values, and this package has no business stating it.
//
// A non-positive def is floored to DefaultHeldCommandCeiling rather than honoured. A
// zero default would mean "this tenant may hold nothing", which reads as an outage for
// every tenant on the instance the moment user-management is unreachable — the exact
// inversion of fail-open that serviceFetcher's non-positive flooring exists to prevent.
func NewHeldCommandCeilingResolver(client *svcclient.Client, umURL string, def int) *HeldCommandCeilingResolver {
	if def <= 0 {
		def = DefaultHeldCommandCeiling
	}
	fetch := func(ctx context.Context, tenant string) (int, error) {
		return fetchHeldCommandCeiling(ctx, client, umURL, tenant, def)
	}
	return &HeldCommandCeilingResolver{
		tenantResolver: newTenantResolver(fetch, def, "held-command-ceiling"),
	}
}

// Resolve returns the tenant's effective HELD-command ceiling without blocking — the
// hot-path function command-delivery calls before parking a command.
//
// It uses resolve rather than resolveOK, unlike ShedPriorityResolver. The shed
// resolver needs the "was this actually fetched?" bool because its default is a bronze
// band, so acting on it during a cold-cache window is a real, wrong action (shedding a
// gold tenant). Here the default is a live ceiling in its own right — the same reading
// TenantLimitResolver takes of a rate default — so there is nothing for a caller to
// hold off on, and holding off would mean admitting an unbounded backlog while the
// cache warms.
func (r *HeldCommandCeilingResolver) Resolve(tenant string) int {
	return r.resolve(tenant)
}

// heldCommandCeilingQuery reads the one field this resolver needs. Named rather than
// inlined at the call so a test can pin it, for the reason spelled out on
// purgeStateQuery: the concrete svcclient.Client is not an interface, so nothing about
// the fetch is otherwise reachable from a unit test, and a typo here would make every
// fetch on the instance error — which, given the deliberate fail-open, means every
// tenant silently sits at the service default with only a WARN to say so.
const heldCommandCeilingQuery = `query { tenantGovernance { heldCommandCeiling } }`

// heldCommandCeilingResponse is the wire shape of that query's data object. Named for
// the same reason as the query: an anonymous struct inside the fetch cannot be handed a
// real response by a test, so a wrong json tag would decode to nil — which reads as
// "inherit the default" and turns a broken decode into a resolver that quietly ignores
// every override an operator has set.
type heldCommandCeilingResponse struct {
	TenantGovernance struct {
		HeldCommandCeiling *int32 `json:"heldCommandCeiling"`
	} `json:"tenantGovernance"`
}

// fetchHeldCommandCeiling reads the scalar heldCommandCeiling from tenantGovernance.
// It does NOT use the Dimension/governanceQuery machinery (that is rate+burst-only) —
// this is a standalone scalar, like shedPriority.
func fetchHeldCommandCeiling(ctx context.Context, client *svcclient.Client, umURL, tenant string, def int) (int, error) {
	var out heldCommandCeilingResponse
	if err := client.Query(ctx, umURL, tenant, heldCommandCeilingQuery, nil, &out); err != nil {
		return 0, err
	}
	return resolveHeldCommandCeiling(out.TenantGovernance.HeldCommandCeiling, def), nil
}

// resolveHeldCommandCeiling folds a wire heldCommandCeiling onto the service's default.
// A null value (the tenant and its tier both declared none) or a non-positive one
// resolves to def. Split out pure so the rule is unit-testable without a live
// user-management (the concrete svcclient.Client is not an interface).
//
// It takes def as an argument rather than closing over a package const, which is the
// one place this differs from resolveShedPriority: the default belongs to the enforcing
// service, not to this package (see NewHeldCommandCeilingResolver).
//
// A NON-POSITIVE value is treated as "inherit", not honoured, for the same reason
// parseRate floors one: the write path rejects it, so it can only arrive by a direct
// out-of-band DB write, and a ceiling of zero would mean the tenant may hold NOTHING —
// every command to an absent device rejected outright. Inherit is the safe reading of a
// value that should not exist. There is no ceiling that means unlimited, so there is no
// direction in which this fold can fail open.
func resolveHeldCommandCeiling(c *int32, def int) int {
	if c == nil || *c <= 0 {
		return def
	}
	return int(*c)
}
