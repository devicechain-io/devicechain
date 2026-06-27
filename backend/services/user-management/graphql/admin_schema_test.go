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
}

// TestAdminQueriesForbidWithoutAuthority confirms an authenticated identity that
// lacks the required system authority is refused (ErrForbidden) — a logged-in
// non-superuser cannot read the admin directory.
func TestAdminQueriesForbidWithoutAuthority(t *testing.T) {
	r := &AdminResolver{}
	ctx := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{}})

	_, err := r.Identities(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.Tenants(ctx)
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
	ctx := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.UserRead)}})

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
	limited := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{string(auth.UserWrite)}})
	_, err = r.Roles(limited, struct{ Scope *string }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.CreateRole(limited, struct{ Request adminRoleCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.CreateTenant(limited, struct{ Request adminTenantCreateInput }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}
