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
