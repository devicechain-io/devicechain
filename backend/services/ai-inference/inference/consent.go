// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// serviceConsentChecker reads a tenant's external-AI-routing consent from
// user-management's tenantGovernance query over a service token (ADR-044, mirroring
// the egress-limit fetcher). The flag is the ADR-056 §6 ai_external_enabled governance
// column, set by an operator via the admin tenant mutation. A query error propagates —
// the resolution cascade treats it as NOT permitted, so a transient user-management
// outage denies external routing rather than allowing it.
type serviceConsentChecker struct {
	client *svcclient.Client
	umURL  string
}

// NewServiceConsentChecker builds a ConsentChecker that reads tenantGovernance from
// user-management at umURL (its /graphql endpoint) over the given service-token client.
func NewServiceConsentChecker(client *svcclient.Client, umURL string) ConsentChecker {
	return &serviceConsentChecker{client: client, umURL: umURL}
}

func (c *serviceConsentChecker) ExternalEnabled(ctx context.Context, tenant string) (bool, error) {
	const q = `query { tenantGovernance { aiExternalEnabled } }`
	var out struct {
		TenantGovernance struct {
			AiExternalEnabled bool `json:"aiExternalEnabled"`
		} `json:"tenantGovernance"`
	}
	if err := c.client.Query(ctx, c.umURL, tenant, q, nil, &out); err != nil {
		return false, err
	}
	return out.TenantGovernance.AiExternalEnabled, nil
}

// deniedConsentChecker fails closed when service-to-service auth is not configured:
// external-routing consent cannot be verified, so it is never permitted. main injects
// this when the shared service secret or the user-management endpoint is absent, so the
// external NL-authoring path is cleanly unavailable rather than silently permissive.
// (The operator smoke test does not go through consent, so it still works.)
type deniedConsentChecker struct{ reason string }

// NewDeniedConsentChecker builds a fail-closed checker that denies all external
// routing, carrying a reason for the surfaced error.
func NewDeniedConsentChecker(reason string) ConsentChecker {
	return deniedConsentChecker{reason: reason}
}

func (d deniedConsentChecker) ExternalEnabled(context.Context, string) (bool, error) {
	return false, fmt.Errorf("external-routing consent cannot be verified: %s", d.reason)
}
