// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// serviceFetcher fetches a tenant's outbound limits from user-management's
// data-plane governance query over a service token (ADR-044). A null field in the
// response (no override) is resolved to the platform default here, so the returned
// Limits are always concrete and never zero/unlimited. A non-positive value that
// somehow reached the column (only possible via a direct out-of-band DB write — the
// GraphQL write path rejects it) is also floored to the platform default rather than
// trusted as a ceiling of zero (which would admit nothing) — the enforcer treats a
// non-positive read-back as inherit-the-default, never as a live limit.
type serviceFetcher struct {
	client *svcclient.Client
	umURL  string
	def    Limits
}

// NewServiceFetcher builds a Fetcher that reads tenantGovernance from
// user-management at umURL (its /graphql endpoint), defaulting unset overrides to
// def.
func NewServiceFetcher(client *svcclient.Client, umURL string, def Limits) Fetcher {
	return &serviceFetcher{client: client, umURL: umURL, def: def}
}

func (f *serviceFetcher) Fetch(ctx context.Context, tenant string) (Limits, error) {
	const q = `query { tenantGovernance { outboundMessagesPerSecond outboundBurst } }`
	var out struct {
		TenantGovernance struct {
			OutboundMessagesPerSecond *float64 `json:"outboundMessagesPerSecond"`
			OutboundBurst             *int     `json:"outboundBurst"`
		} `json:"tenantGovernance"`
	}
	if err := f.client.Query(ctx, f.umURL, tenant, q, nil, &out); err != nil {
		return Limits{}, err
	}
	limits := f.def
	if v := out.TenantGovernance.OutboundMessagesPerSecond; v != nil && *v > 0 {
		limits.MessagesPerSecond = *v
	}
	if v := out.TenantGovernance.OutboundBurst; v != nil && *v > 0 {
		limits.Burst = *v
	}
	return limits, nil
}
