// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// The inbound-integrations ceiling: how many inbound integrations (a network server or a
// webhook sender authenticated by its own bearer token) one tenant may hold.
//
// Each integration is an authenticated ingest route, with its own bad-credential bucket and
// its own cached pre-check on the ingest path, so the count is packaging a tier sells rather
// than something an author is left to choose.
const (
	// IntegrationsPerTenantField is the field device-management selects off tenantGovernance
	// AND the tier config key an operator sets, declared once here for the reason
	// GeoFenceCeilingField gives: the two must be the same string by construction.
	IntegrationsPerTenantField = "integrationsPerTenant"

	// DefaultIntegrationsPerTenant is what a tenant whose tier declares nothing may hold.
	DefaultIntegrationsPerTenant = 5

	// MaxIntegrationsPerTenant is the most any tier may grant.
	MaxIntegrationsPerTenant = 100

	// IntegrationsPerTenantBecause is the reason a tier value above the maximum is refused,
	// for the operator who was refused.
	IntegrationsPerTenantBecause = "each integration is an authenticated ingest route with its own " +
		"credential check and failure bucket on the ingest path"
)

// IntegrationsCeilingResolver reads a tenant's integrations ceiling from user-management's
// tenantGovernance.
//
// 🔴 IT DOES NOT CACHE AND IT RETURNS AN ERROR. It is read when an integration is created,
// which is rare, and a tier may sit at zero, so answering an unreachable user-management with
// any number would raise the ceiling of exactly the tenant nobody could read. The caller
// refuses the create.
type IntegrationsCeilingResolver struct {
	fetch func(ctx context.Context, tenant string) (int, error)
}

// NewIntegrationsCeilingResolver wires the resolver to user-management at umURL over the
// given service-token client, which needs tenant:read.
func NewIntegrationsCeilingResolver(client *svcclient.Client, umURL string) *IntegrationsCeilingResolver {
	return &IntegrationsCeilingResolver{fetch: func(ctx context.Context, tenant string) (int, error) {
		var out integrationsCeilingResponse
		if err := client.Query(ctx, umURL, tenant, integrationsCeilingQuery, nil, &out); err != nil {
			return 0, err
		}
		return resolveIntegrationsCeiling(out.TenantGovernance.IntegrationsPerTenant), nil
	}}
}

// Resolve answers how many integrations the tenant may hold.
func (r *IntegrationsCeilingResolver) Resolve(ctx context.Context, tenant string) (int, error) {
	if tenant == "" {
		return 0, fmt.Errorf("governance: cannot resolve the integrations ceiling without a tenant")
	}
	return r.fetch(ctx, tenant)
}

// integrationsCeilingQuery selects the one field, built from the constant so a rename cannot
// leave the query selecting the old name.
const integrationsCeilingQuery = "query { tenantGovernance { " + IntegrationsPerTenantField + " } }"

// integrationsCeilingResponse is the wire shape of that query. The json tag must be a
// literal; TestIntegrationsCeilingQueryAndResponseAgree decodes a body built from the
// constant, so a tag that drifts from it fails instead of silently decoding to nil.
type integrationsCeilingResponse struct {
	TenantGovernance struct {
		IntegrationsPerTenant *int32 `json:"integrationsPerTenant"`
	} `json:"tenantGovernance"`
}

// resolveIntegrationsCeiling folds the wire value onto the platform default. Null (neither
// the tenant nor its tier declares one) and a negative value resolve to the default; a value
// above the maximum is clamped to it, for the version-skew reason resolveGeoFenceCap gives.
// Zero is a real setting: a tier that holds no integrations.
func resolveIntegrationsCeiling(v *int32) int {
	if v == nil || *v < 0 {
		return DefaultIntegrationsPerTenant
	}
	if int(*v) > MaxIntegrationsPerTenant {
		return MaxIntegrationsPerTenant
	}
	return int(*v)
}
