// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The user-management half of a cross-service contract: every enforcing service reads
// tenantGovernance over a service token and must tell "no such tenant" from "could not
// ask", because only the second is an outage worth paging on. The difference is carried
// by extensions.code, which core/governance matches against the same constant.
func TestTenantGovernanceReportsAnUnknownTenantByCode(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.TenantTier{}, &iam.Tenant{},
		&iam.OAuthClient{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, store.CreateTenantTier(context.Background(), tier))
	require.NoError(t, store.CreateTenant(context.Background(), &iam.Tenant{
		Token: "acme", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive,
	}))
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	query := func(tenant string) *gql.Response {
		ctx := auth.WithClaims(context.Background(), &auth.Claims{
			TokenType: auth.TokenTypeService, Authorities: []string{string(auth.TenantRead)},
		})
		ctx = core.WithTenant(ctx, tenant)
		ctx = context.WithValue(ctx, ContextIdentityKey, mgr)
		return schema.Exec(ctx, `{ tenantGovernance { purgeState } }`, "", nil)
	}

	unknown := query("ghost")
	require.Len(t, unknown.Errors, 1, "an unknown tenant must be refused")
	assert.Equal(t, governance.CodeUnknownTenant, unknown.Errors[0].Extensions["code"],
		"the refusal must say, by code, that the tenant does not exist")

	known := query("acme")
	require.Empty(t, known.Errors, "a known tenant is served")
	assert.JSONEq(t, `{"tenantGovernance":{"purgeState":"active"}}`, string(known.Data))
}
