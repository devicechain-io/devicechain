// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-user-management/iam"
)

// The integrations ceiling is a cross-service wire contract: device-management selects
// integrationsPerTenant off tenantGovernance (the query in core/governance, built from the
// same constant as the tier key) before it will create an integration.
func TestTheIntegrationsCeilingIsServedUnderItsTierKeyName(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(context.Background(),
		`{ __type(name: "TenantGovernance") { fields { name type { kind name } } } }`, "", nil)
	require.Empty(t, res.Errors)
	var out struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Type struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"type"`
			} `json:"fields"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))
	for _, f := range out.Type.Fields {
		if f.Name == iam.IntegrationsPerTenantConfigKey {
			require.Equal(t, "SCALAR", f.Type.Kind, "must be a nullable Int")
			require.Equal(t, "Int", f.Type.Name)
			return
		}
	}
	t.Fatalf("TenantGovernance does not serve %q", iam.IntegrationsPerTenantConfigKey)
}

func TestTheIntegrationsCeilingResolvesFromTheTier(t *testing.T) {
	tier := &iam.TenantTier{Token: iam.TierGoldToken, Config: map[string]any{iam.IntegrationsPerTenantConfigKey: float64(12)}}
	got, err := (&TenantGovernanceResolver{t: &iam.Tenant{Tier: tier}}).IntegrationsPerTenant()
	require.NoError(t, err)
	require.EqualValues(t, 12, *got)

	got, err = (&TenantGovernanceResolver{t: &iam.Tenant{Tier: &iam.TenantTier{Token: iam.TierSilverToken}}}).IntegrationsPerTenant()
	require.NoError(t, err)
	require.Nil(t, got)
}
