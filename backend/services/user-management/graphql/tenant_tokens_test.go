// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenantTokens is the one query on the tenant data plane that is deliberately NOT
// scoped to the caller's tenant: it answers "which tenants exist", which is what a
// cross-tenant service (broker presence reconciliation) needs and what nothing else
// here can supply. Its entire protection is the TIER of the authority it demands, so
// that is what these tests pin.

// Unauthenticated is refused before the store is touched. The resolver is built with
// no identity manager on the context, so a resolver that authorized late would panic
// rather than return — which is a failure either way, but this asserts the refusal.
func TestTenantTokensFailsClosedWithoutClaims(t *testing.T) {
	r := &SchemaResolver{}
	_, err := r.TenantTokens(context.Background())
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// 🔴 THE CASE THIS QUERY EXISTS TO SURVIVE. A tenant subject holding "*" holds every
// TENANT-tier authority — an ordinary tenant admin, signed in to the console. If
// tenant:read were a tenant-tier authority, that subject would pass this check and any
// tenant admin could enumerate every other tenant on the instance. It is system-tier,
// and this test is what says so out loud: a change that re-tiered it would break here
// rather than quietly opening the list.
func TestATenantAdminCannotEnumerateTheInstancesTenants(t *testing.T) {
	r := &SchemaResolver{}
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)},
	})

	_, err := r.TenantTokens(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden,
		"a tenant subject holding \"*\" must not be able to list the instance's tenants")
}

// A tenant-tier subject that names tenant:read explicitly is refused for the same
// reason — the tier is what decides, not the string. Without this case the test above
// could be passing merely because "*" is expanded per tier rather than because the
// authority itself is out of reach.
func TestNamingTheAuthorityDoesNotCrossTheTier(t *testing.T) {
	r := &SchemaResolver{}
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeAccess,
		Authorities: []string{string(auth.TenantRead)},
	})

	_, err := r.TenantTokens(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// The counterweight: a SERVICE token carrying tenant:read — exactly what
// governance.NewTenantLifecycleGate mints, and what the presence reconciler will
// present — must pass the authorization check. Without this, every assertion above is
// satisfied by a resolver that refuses everyone, and the reconciler would be built
// against a query it can never call.
func TestAServiceTokenMayListTenantTokens(t *testing.T) {
	r := &SchemaResolver{}
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeService,
		Authorities: []string{string(auth.TenantRead)},
	})

	// It gets past authorization and on to the store. There is no identity manager on
	// this context, so reaching the store panics — which is precisely the observation
	// wanted: authorization did not refuse it.
	require.Panics(t, func() { _, _ = r.TenantTokens(ctx) },
		"a service token with tenant:read was refused at authorization; it must reach the store")
}
