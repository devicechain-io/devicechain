// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"reflect"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// 🔴 THE EXHAUSTIVENESS CHECK OVER THIS SERVICE'S ADMIN-PLANE UPDATE SURFACE.
//
// The guard itself is core's putest.AssertEveryUpdateTakesADedicatedRequest — it
// enumerates *Service's own Update* methods and asks three structural things of each:
// that the request type is REGISTERED with partialUpdateFamilies() so something drives
// its three states against a real database; that it carries no Token field; and that
// every exported field carries the three states. Its header says which mutants walked
// past the name-only version it replaced, and why none of the three is a check on
// spelling — one of those mutants was named after THIS service, because
// AdminTenantUpdateRequest was a well-named type of plain pointers with full-replace
// semantics and said so in its own comment.
//
// What is local is what only this service can say: which updates have NOT been
// converted, and how many Update* methods reflection must find before the walk is
// believable.
//
// 🔴 UpdateRole MAY BE INVISIBLE TO IT, AND THAT IS NOT SOMETHING THIS FILE CAN FIX.
// The guard's method filter accepts (receiver, ctx, token, *request) — four inputs, the
// last a pointer. A role is named by a PAIR (scope, token), because "operator" at system
// scope and "operator" at tenant scope are different roles with different authority
// vocabularies, so UpdateRole has five and is SKIPPED rather than reported. Contorting
// the signature to fit a reflection filter would change which role a caller addresses,
// which is the one thing this conversion must not do. TestUpdateRoleIsCoveredDespiteItsScopeArgument
// below is the standing check on that gap: it asserts the shape directly, so the day the
// filter widens, the guard's MinUpdateMethods floor rises and this file has to be read.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Service]{
		Families: partialUpdateFamilies(),

		// Every admin-plane update in this service is converted, so there is nothing to
		// exempt. The map is present and empty rather than omitted: a nil map and an empty
		// one behave identically here, and writing it out is what makes "the residual is
		// zero" a statement rather than an absence.
		Exempt: map[string]string{},

		// The anti-vacuity floor. Reflection over a renamed or embedded receiver could
		// find nothing at all, and a loop over nothing reports success.
		//
		// 🔴 IT IS 3, NOT 4, AND THE MISSING ONE IS UpdateRole — see the header. Raising it
		// to 4 would fail today for the right reason and the wrong cause, which is how a
		// floor gets deleted instead of understood.
		MinUpdateMethods: 3,
	})
}

// TestUpdateRoleIsCoveredDespiteItsScopeArgument is what stands in for the guard on the
// one method the guard cannot see.
//
// It asserts the same three structural things by hand — the request type is the one the
// harness registers, it carries no Token, and every exported field carries the three
// states — so UpdateRole is not certified by the word "Update" in its name while the
// reflection filter looks past it.
func TestUpdateRoleIsCoveredDespiteItsScopeArgument(t *testing.T) {
	var registered bool
	for _, fam := range partialUpdateFamilies() {
		if fam.Name == "role" {
			_, registered = fam.NewRequest().(*RoleUpdateRequest)
		}
	}
	require.True(t, registered,
		"the role family does not build a *RoleUpdateRequest, so nothing drives UpdateRole's "+
			"three states against a database and the guard cannot see it either")

	putest.AssertCarriesTheThreeStates(t, "UpdateRole", reflect.TypeOf(RoleUpdateRequest{}))
}

// TestEmptyListIsRefusedForAnOAuthClientsAllowlists drives the FOURTH wire state a list
// has — `[]` — on the two fields where it is REFUSED rather than honoured.
//
// The shared harness has a property for [] (EmptyListIsTheSameAsANull) and it only runs
// on a CLEARABLE list, where the claim is that null and [] agree in emptying the column.
// For a required list they agree in being rejected, which is a different claim with no
// property to carry it — and [] is the spelling that actually arrives, because a form
// with nothing selected serializes as an empty array and never as null.
//
// 🔴 WITHOUT THIS, "the allowlists cannot be emptied" IS HALF-TESTED: the null half is
// driven by ARequiredFieldRefusesAnExplicitNull, and a fold that special-cased [] into
// "leave it alone" would pass everything else in this package while silently turning the
// most likely client request into a no-op that reports success.
func TestEmptyListIsRefusedForAnOAuthClientsAllowlists(t *testing.T) {
	const clientId = "mcp-client"
	uris := []string{"https://example.invalid/callback"}
	scopes := []string{"read-only"}

	cases := []struct {
		field   string
		request func() *OAuthClientUpdateRequest
	}{
		{"redirectUris", func() *OAuthClientUpdateRequest {
			return &OAuthClientUpdateRequest{RedirectUris: dcgraphql.OptionalStringListOf([]string{})}
		}},
		{"scopes", func() *OAuthClientUpdateRequest {
			return &OAuthClientUpdateRequest{Scopes: dcgraphql.OptionalStringListOf([]string{})}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			s := newPartialUpdateService(t, &iam.OAuthClient{})
			ctx := putest.TenantContext(partialUpdateTenant)()
			_, _, err := s.CreateOAuthClient(ctx, OAuthClientInput{
				ClientId: clientId, RedirectURIs: uris, Scopes: scopes,
			})
			require.NoError(t, err)

			_, err = s.UpdateOAuthClient(ctx, clientId, tc.request())
			require.Errorf(t, err, "an empty %s was accepted", tc.field)
			require.Contains(t, err.Error(), tc.field)

			// And the refusal wrote nothing: the client still holds both lists.
			after, err := s.iam.OAuthClientByClientId(ctx, clientId)
			require.NoError(t, err)
			require.Equal(t, uris, after.RedirectURIs)
			require.Equal(t, scopes, after.Scopes)
		})
	}
}

// TestAnEmptyAuthoritySetIsAllowed is the counterweight to the test above, and the
// reason "a list cannot be emptied" is not the rule this conversion adopted.
//
// A role granting nothing is legal at CREATE and has always been, so refusing to empty
// one on update would make it creatable and uncorrectable. The rule is that update and
// create agree about what a legal record is — which is a claim about each entity, not
// about lists.
func TestAnEmptyAuthoritySetIsAllowed(t *testing.T) {
	s := newPartialUpdateService(t, &iam.Role{})
	ctx := putest.TenantContext(partialUpdateTenant)()

	// The premise: create allows it. Asserted rather than assumed — if this ever stops
	// being true, the update below should stop being allowed too, and the test that
	// notices should be this one.
	_, err := s.CreateRole(ctx, RoleInput{
		Scope: string(iam.ScopeSystem), Token: "powerless", Authorities: []string{},
	})
	require.NoError(t, err, "createRole refused an empty authority set, which is the premise "+
		"the update below is allowed on")

	_, err = s.CreateRole(ctx, RoleInput{
		Scope: string(iam.ScopeSystem), Token: "ops-role", Authorities: []string{"role:read"},
	})
	require.NoError(t, err)

	emptied, err := s.UpdateRole(ctx, string(iam.ScopeSystem), "ops-role", &RoleUpdateRequest{
		Authorities: dcgraphql.ClearedStringList(),
	})
	require.NoError(t, err, "clearing a role's authorities was refused, which leaves a role "+
		"that create can produce and update cannot correct")
	require.Empty(t, emptied.Authorities)
}

// TestUpdateRoleCannotCrossScopes pins the half of the conversion that had to NOT change:
// which role a request addresses.
//
// scope and token together are a role's identity, and the request carries neither. A
// conversion that folded scope into the payload, or that resolved it from anything but
// the argument, would let an update of the tenant-scoped "ops" role land on the
// system-scoped one — a privilege change that returns 200 and names the right token.
func TestUpdateRoleCannotCrossScopes(t *testing.T) {
	s := newPartialUpdateService(t, &iam.Role{})
	ctx := putest.TenantContext(partialUpdateTenant)()

	for _, scope := range []iam.RoleScope{iam.ScopeSystem, iam.ScopeTenant} {
		_, err := s.CreateRole(ctx, RoleInput{Scope: string(scope), Token: "ops", Name: string(scope)})
		require.NoError(t, err)
	}

	_, err := s.UpdateRole(ctx, string(iam.ScopeTenant), "ops", &RoleUpdateRequest{
		Name: dcgraphql.OptionalStringOf("renamed"),
	})
	require.NoError(t, err)

	sys, err := s.iam.RoleByScopeToken(ctx, iam.ScopeSystem, "ops")
	require.NoError(t, err)
	require.Equal(t, string(iam.ScopeSystem), sys.Name.String,
		"updating the TENANT-scoped role changed the SYSTEM-scoped one of the same token")

	ten, err := s.iam.RoleByScopeToken(ctx, iam.ScopeTenant, "ops")
	require.NoError(t, err)
	require.Equal(t, "renamed", ten.Name.String, "the update did not reach the role it named")
}
