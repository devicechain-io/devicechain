// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
)

// adminCtx builds the credential the admin plane actually carries: an IDENTITY
// token (ADR-033 — /admin/graphql validates identity-tier tokens and runs in the
// system context). The token type is load-bearing, not decoration: system-tier
// authorities like tenant:read and user:write can only be satisfied by an identity
// or service token (ADR-065), so claims left untyped here would be refused on the
// tier and every "forbidden without the right authority" assertion below would
// pass for the wrong reason — testing nothing.
func adminCtx(authorities ...string) context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeIdentity,
		Authorities: authorities,
	})
}

// TestAdminSchemaParses validates that the admin schema parses against its
// resolver root — every admin field must have a matching resolver method
// (ADR-033).
func TestAdminSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("admin schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
}

// TestAdminQueriesFailClosed confirms the admin read resolvers reject an
// unauthenticated request (no claims on context) with ErrUnauthenticated, before
// ever touching the service — so the endpoint's identity-token requirement is not
// the only gate (ADR-033 defence in depth).
func TestAdminQueriesFailClosed(t *testing.T) {
	r := &AdminResolver{}
	ctx := context.Background()

	_, err := r.Identities(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.Tenants(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.OauthClients(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// TestAdminQueriesForbidWithoutAuthority confirms an authenticated identity that
// lacks the required system authority is refused (ErrForbidden) — a logged-in
// non-superuser cannot read the admin directory.
func TestAdminQueriesForbidWithoutAuthority(t *testing.T) {
	r := &AdminResolver{}
	ctx := adminCtx()

	_, err := r.Identities(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.Tenants(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	// An identity with unrelated authorities still lacks client:read.
	_, err = r.OauthClients(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// TestAdminOAuthClientMutationsFailClosed confirms the OAuth client registry
// mutations reject an unauthenticated request before touching the service — the
// client:write gate runs first (ADR-047).
func TestAdminOAuthClientMutationsFailClosed(t *testing.T) {
	r := &AdminResolver{}
	ctx := context.Background()

	_, err := r.CreateOauthClient(ctx, struct{ Request adminOAuthClientCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.DeleteOauthClient(ctx, struct{ ClientId string }{ClientId: "x"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	// Authenticated but without client:write → forbidden.
	ctx = adminCtx("user:read")
	_, err = r.SetOauthClientEnabled(ctx, struct {
		ClientId string
		Enabled  bool
	}{ClientId: "x"})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// TestAdminMutationsFailClosed confirms the admin mutations reject an
// unauthenticated request (no claims) before touching the service — the
// user:write gate runs first, so a missing admin service in context is never
// reached (ADR-033).
func TestAdminMutationsFailClosed(t *testing.T) {
	r := &AdminResolver{}
	ctx := context.Background()

	_, err := r.CreateIdentity(ctx, struct{ Request adminIdentityCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.SetIdentityEnabled(ctx, struct {
		Email   string
		Enabled bool
	}{Email: "a@b.c"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.AddMembership(ctx, struct {
		Email      string
		Tenant     string
		RoleTokens []string
	}{Email: "a@b.c", Tenant: "t"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	ok, err := r.DeleteIdentity(ctx, struct{ Email string }{Email: "a@b.c"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)
}

// TestAdminMutationsForbidWithoutAuthority confirms an authenticated identity
// lacking user:write is refused on a representative mutation.
func TestAdminMutationsForbidWithoutAuthority(t *testing.T) {
	r := &AdminResolver{}
	ctx := adminCtx(string(auth.UserRead))

	_, err := r.CreateIdentity(ctx, struct{ Request adminIdentityCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// TestAdminCatalogFailClosed confirms the role-catalog and tenant resolvers gate
// on their own authorities: an unauthenticated request is rejected, and an
// identity holding only user:write (not role:write / tenant:write) is forbidden
// from the catalog and tenant mutations (ADR-033 least privilege).
func TestAdminCatalogFailClosed(t *testing.T) {
	r := &AdminResolver{}

	// Unauthenticated.
	bare := context.Background()
	_, err := r.Roles(bare, struct{ Scope *string }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, err = r.CreateRole(bare, struct{ Request adminRoleCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, err = r.CreateTenant(bare, struct{ Request adminTenantCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	// Authenticated but holding only user:write — wrong authority for the catalog.
	limited := adminCtx(string(auth.UserWrite))
	_, err = r.Roles(limited, struct{ Scope *string }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.CreateRole(limited, struct{ Request adminRoleCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.CreateTenant(limited, struct{ Request adminTenantCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// TestAdminTenantTierFailClosed holds the tier resolvers (ADR-065) to the same
// least-privilege bar as the rest of the catalog: unauthenticated is rejected, and
// an identity holding only user:write cannot read or edit the tier vocabulary.
//
// Worth pinning rather than assuming: a tier is a COMMERCIAL fact — what a customer
// is sold, and (once ADR-056's menu lands) which AI models they may spend money on.
// Editing one moves every tenant at that tier, live. Reads gate on tenant:read and
// writes on tenant:write, deliberately reusing the tenant authorities rather than
// minting a tier-specific pair: a tier is tenant-registry packaging administered by
// the same operator on the same plane.
func TestAdminTenantTierFailClosed(t *testing.T) {
	r := &AdminResolver{}

	bare := context.Background()
	_, err := r.TenantTiers(bare)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, err = r.CreateTenantTier(bare, struct{ Request adminTenantTierCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, err = r.UpdateTenantTier(bare, struct {
		Token   string
		Request adminTenantTierUpdateInput
	}{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	_, err = r.DeleteTenantTier(bare, struct{ Token string }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	// Authenticated but holding only user:write — wrong authority for packaging.
	limited := adminCtx(string(auth.UserWrite))
	_, err = r.TenantTiers(limited)
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.CreateTenantTier(limited, struct{ Request adminTenantTierCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.UpdateTenantTier(limited, struct {
		Token   string
		Request adminTenantTierUpdateInput
	}{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.DeleteTenantTier(limited, struct{ Token string }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)

	// tenant:read is enough to LIST tiers but never to change them — a read-only
	// operator must not be able to re-price the platform.
	reader := adminCtx(string(auth.TenantRead))
	_, err = r.CreateTenantTier(reader, struct{ Request adminTenantTierCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.DeleteTenantTier(reader, struct{ Token string }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}
