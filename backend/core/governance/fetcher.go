// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// Dimension identifies one per-tenant governance dimension: the pair of
// tenantGovernance fields carrying its rate and burst overrides, plus a short name
// for logs. The fields are compile-time constants declared below — never caller or
// tenant input — so composing them into the query string is safe.
type Dimension struct {
	// Name labels the dimension in logs (bounded, never a metric label).
	Name string
	// RateField / BurstField are the tenantGovernance field names holding this
	// dimension's overrides. Both are nullable: null means "inherit the platform
	// default", which is itself a limit, never unlimited.
	RateField  string
	BurstField string
}

// The governance dimensions declared on the iam_tenants control-plane row and
// exposed by user-management's tenantGovernance query. Each is independent: a
// tenant overriding one inherits the platform default for the others.
var (
	// Ingest governs inbound device telemetry admission at event-sources (ADR-023 G.1/G.2).
	Ingest = Dimension{Name: "ingest", RateField: "ingestMessagesPerSecond", BurstField: "ingestBurst"}
	// Outbound governs REACT connector egress, charged at both the source
	// (event-processing) and the sink (outbound-connectors) — ADR-060 SD-3.
	Outbound = Dimension{Name: "outbound", RateField: "outboundMessagesPerSecond", BurstField: "outboundBurst"}
)

// serviceFetcher fetches a tenant's limits for one dimension from
// user-management's data-plane governance query over a service token (ADR-044).
type serviceFetcher struct {
	client *svcclient.Client
	umURL  string
	def    Limits
	dim    Dimension
}

// NewServiceFetcher builds a Fetcher reading dim's overrides from tenantGovernance
// at umURL (user-management's /graphql endpoint), resolving unset overrides to def.
func NewServiceFetcher(client *svcclient.Client, umURL string, def Limits, dim Dimension) Fetcher {
	return &serviceFetcher{client: client, umURL: umURL, def: def, dim: dim}
}

// NewServiceLimitResolver is the one-call wiring every enforcing service wants: a
// resolver for dim backed by user-management, defaulting to def.
func NewServiceLimitResolver(client *svcclient.Client, umURL string, def Limits, dim Dimension) *TenantLimitResolver {
	return NewTenantLimitResolver(NewServiceFetcher(client, umURL, def, dim), def, dim.Name)
}

// Fetch reads the dimension's two override fields and resolves them against the
// platform default, so the returned Limits are always concrete and never
// zero/unlimited.
//
// A null field means the tenant declared no override — inherit the default. A
// NON-POSITIVE field is also floored to the default rather than trusted: the
// GraphQL write path rejects those, so one can only arrive via a direct
// out-of-band DB write, and passing it through would hand core.TenantRateLimiter a
// ceiling that admits nothing (a self-inflicted outage for that tenant). Inherit
// is the safe reading of a value that should not exist; never a live limit of zero.
func (f *serviceFetcher) Fetch(ctx context.Context, tenant string) (Limits, error) {
	// Decoded generically (rather than into a per-dimension struct) so one fetcher
	// serves every dimension. json.Number carries both the Float and Int fields
	// without committing to a type here; a null decodes to a nil entry.
	var out struct {
		TenantGovernance map[string]*json.Number `json:"tenantGovernance"`
	}
	if err := f.client.Query(ctx, f.umURL, tenant, governanceQuery(f.dim), nil, &out); err != nil {
		return Limits{}, err
	}
	return resolveLimits(out.TenantGovernance, f.def, f.dim), nil
}

// governanceQuery composes the tenantGovernance query for one dimension. Both
// field names are package constants, never caller input.
func governanceQuery(dim Dimension) string {
	return fmt.Sprintf(`query { tenantGovernance { %s %s } }`, dim.RateField, dim.BurstField)
}

// resolveLimits folds a decoded tenantGovernance response onto the platform
// default. Split out pure so the fail-safe rules below are unit-testable without a
// live user-management (the concrete svcclient.Client is not an interface).
func resolveLimits(raw map[string]*json.Number, def Limits, dim Dimension) Limits {
	limits := def
	if v := raw[dim.RateField]; v != nil {
		if rate, err := v.Float64(); err == nil && rate > 0 {
			limits.MessagesPerSecond = rate
		}
	}
	if v := raw[dim.BurstField]; v != nil {
		if burst, err := v.Int64(); err == nil && burst > 0 {
			limits.Burst = int(burst)
		}
	}
	return limits
}
