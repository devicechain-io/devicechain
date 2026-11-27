// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// intent:read and intent:write are reserved a minor before anything checks them. What
// the reservation buys is that THIS release accepts a role naming them, on every path a
// role is written by — including an unrelated edit of a role that already holds one,
// since every write revalidates the whole authority set. A system role refuses them:
// an intent acts on one tenant's devices.
func TestIntentAuthoritiesAreAcceptedOnTenantRoles(t *testing.T) {
	for _, authorities := range [][]string{
		{string(auth.IntentRead)},
		{string(auth.IntentWrite)},
		{string(auth.IntentRead), string(auth.IntentWrite)},
	} {
		t.Run(authorities[len(authorities)-1], func(t *testing.T) {
			e := newPlaneEnv(t)
			e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
			boss := as("boss@acme.example", "acme", "*")

			// The tenant's own data plane.
			role, err := e.s.CreateTenantRole(boss, TenantRoleInput{Token: "intent-owners", Authorities: authorities})
			require.NoError(t, err)
			require.ElementsMatch(t, authorities, role.Authorities)
			require.NotNil(t, role.TenantID)
			require.Equal(t, "acme", *role.TenantID)

			renamed, err := e.s.UpdateTenantRole(boss, "intent-owners",
				&RoleUpdateRequest{Name: dcgraphql.OptionalStringOf("Intent owners")})
			require.NoError(t, err, "an unrelated edit of a role holding a reserved authority must succeed")
			require.ElementsMatch(t, authorities, renamed.Authorities)

			// The operator's catalogue.
			_, err = e.s.CreateRole(context.Background(), RoleInput{
				Scope: string(iam.ScopeTenant), Token: "catalogue-intents", Authorities: authorities,
			})
			require.NoError(t, err)
			_, err = e.s.UpdateRole(context.Background(), string(iam.ScopeTenant), "catalogue-intents",
				&RoleUpdateRequest{Description: dcgraphql.OptionalStringOf("renamed")}, nil)
			require.NoError(t, err)

			// A system role must not advertise power over a tenant's own intents.
			require.Error(t, validateAuthorities(iam.ScopeSystem, authorities))
			_, err = e.s.CreateRole(context.Background(), RoleInput{
				Scope: string(iam.ScopeSystem), Token: "system-intents", Authorities: authorities,
			})
			require.Error(t, err)
		})
	}
}

// Granting is still bounded by what the granter holds: a reserved authority is not a
// way around the no-escalation rule.
func TestIntentAuthoritiesCannotBeGrantedByAMemberWhoLacksThem(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "dave@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	_, err := e.s.CreateTenantRole(as("dave@acme.example", "acme", "role:write", "intent:read"), TenantRoleInput{
		Token: "escalate", Authorities: []string{string(auth.IntentWrite)},
	})
	require.Error(t, err)
}
