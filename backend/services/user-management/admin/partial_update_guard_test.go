// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
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
// 🔴 ALL FOUR ARE VISIBLE TO IT, INCLUDING UpdateRole, AND THAT IS RECENT. The guard used
// to locate the input by POSITION — parameter 3 of exactly 4 — and walked past anything
// else in silence. UpdateRole takes (ctx, scope, token, request), because a role's
// identity is the PAIR: "operator" at system scope and "operator" at tenant scope are
// different roles with different authority vocabularies, so the scope cannot move into
// the payload and cannot be dropped. Under the positional rule that fifth parameter made
// it invisible — certified by nothing, and uncatchable by the anti-vacuity floor, because
// the floor bounds what was WALKED and a skipped method is not among them. The guard now
// locates the input by SHAPE, so the signature this conversion needs and the rule this
// conversion is measured by no longer disagree.
//
// What is local is what only this service can say: which updates have NOT been converted,
// and how many Update* methods reflection must find before the walk is believable.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Service]{
		Families: partialUpdateFamilies(),

		// Every admin-plane update in this service is converted, so both maps are empty.
		// They are written out rather than omitted: a nil map and an empty one behave
		// identically here, and spelling them is what makes "the residual is zero" a
		// statement a reader can count rather than an absence they have to infer.
		Exempt:            map[string]string{},
		NotAnEntityUpdate: map[string]string{},

		// The anti-vacuity floor: the TOTAL number of Update* methods on this service,
		// not the number converted. UpdateRole, UpdateTenant, UpdateTenantTier and
		// UpdateOAuthClient — four, and every one of them is walked rather than skipped.
		MinUpdateMethods: 4,
	})
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
