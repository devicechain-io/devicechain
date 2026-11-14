// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func TestRulesCeilingIsServedFromTheTier(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(context.Background(), `{ __type(name: "TenantGovernance") { fields { name } } }`, "", nil)
	require.Empty(t, res.Errors)
	require.Contains(t, string(res.Data), `"name":"rulesPerTenant"`)
	r := &TenantGovernanceResolver{t: &iam.Tenant{Tier: &iam.TenantTier{Config: map[string]any{"rulesPerTenant": float64(3)}}}}
	got, err := r.RulesPerTenant()
	require.NoError(t, err)
	require.NotNil(t, got)
	require.EqualValues(t, 3, *got)
	r.t.Tier = nil
	got, err = r.RulesPerTenant()
	require.NoError(t, err)
	require.Nil(t, got)
}
