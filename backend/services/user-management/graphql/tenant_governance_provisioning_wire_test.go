// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// The self-registration dimension through the REAL schema and store, as device-management
// reads it: the exact query core/governance composes for the dimension, executed over a
// service token against rows persisted through the store. The three tenants are the three
// levels of the cascade — a per-tenant override, a tier, and neither — and each must read
// back the value that level supplies. The "neither" tenant reads null, which
// core/governance folds onto DefaultProvisioningLimits rather than onto "unlimited".
func TestTenantGovernanceServesTheProvisioningCascadeOverTheWire(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.TenantTier{}, &iam.Tenant{},
		&iam.OAuthClient{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	ctx := context.Background()

	gold := &iam.TenantTier{Token: "gold", Config: map[string]any{
		governance.Provisioning.RateField:  float64(50),
		governance.Provisioning.BurstField: float64(1000),
	}}
	silver := &iam.TenantTier{Token: "silver"}
	require.NoError(t, store.CreateTenantTier(ctx, gold))
	require.NoError(t, store.CreateTenantTier(ctx, silver))

	rate, burst := 3.5, 9
	require.NoError(t, store.CreateTenant(ctx, &iam.Tenant{
		Token: "override", Enabled: true, TierID: gold.ID, PurgeState: iam.PurgeActive,
		ProvisioningRegistrationsPerSecond: &rate, ProvisioningBurst: &burst,
	}))
	require.NoError(t, store.CreateTenant(ctx, &iam.Tenant{
		Token: "tiered", Enabled: true, TierID: gold.ID, PurgeState: iam.PurgeActive,
	}))
	require.NoError(t, store.CreateTenant(ctx, &iam.Tenant{
		Token: "bare", Enabled: true, TierID: silver.ID, PurgeState: iam.PurgeActive,
	}))

	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	query := `query { tenantGovernance { ` +
		governance.Provisioning.RateField + ` ` + governance.Provisioning.BurstField + ` } }`

	read := func(tenant string) map[string]any {
		cctx := auth.WithClaims(context.Background(), &auth.Claims{
			TokenType: auth.TokenTypeService, Authorities: []string{string(auth.TenantRead)},
		})
		cctx = core.WithTenant(cctx, tenant)
		cctx = context.WithValue(cctx, ContextIdentityKey, mgr)
		resp := schema.Exec(cctx, query, "", nil)
		require.Empty(t, resp.Errors, tenant)
		var out struct {
			TenantGovernance map[string]any `json:"tenantGovernance"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &out))
		return out.TenantGovernance
	}

	o := read("override")
	require.Equal(t, 3.5, o["provisioningRegistrationsPerSecond"], "the tenant's own override wins over its tier")
	require.EqualValues(t, 9, o["provisioningBurst"])

	tt := read("tiered")
	require.EqualValues(t, 50, tt["provisioningRegistrationsPerSecond"], "no override: the tier decides")
	require.EqualValues(t, 1000, tt["provisioningBurst"])

	b := read("bare")
	require.Nil(t, b["provisioningRegistrationsPerSecond"], "neither level declares: null, so the platform default applies")
	require.Nil(t, b["provisioningBurst"])
}
