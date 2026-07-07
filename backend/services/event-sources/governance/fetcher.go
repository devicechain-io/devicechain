// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// serviceFetcher fetches a tenant's ingest limits from user-management's
// data-plane governance query over a service token (ADR-044). A null field in the
// response (no override) is resolved to the platform default here, so the
// returned Limits are always concrete and never zero/unlimited.
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
	const q = `query { tenantGovernance { ingestMessagesPerSecond ingestBurst } }`
	var out struct {
		TenantGovernance struct {
			IngestMessagesPerSecond *float64 `json:"ingestMessagesPerSecond"`
			IngestBurst             *int     `json:"ingestBurst"`
		} `json:"tenantGovernance"`
	}
	if err := f.client.Query(ctx, f.umURL, tenant, q, nil, &out); err != nil {
		return Limits{}, err
	}
	limits := f.def
	if v := out.TenantGovernance.IngestMessagesPerSecond; v != nil {
		limits.MessagesPerSecond = *v
	}
	if v := out.TenantGovernance.IngestBurst; v != nil {
		limits.Burst = *v
	}
	return limits, nil
}
