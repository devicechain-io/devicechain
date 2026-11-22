// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

const readinessQuery = `{ fleetOperationReadiness { protocolVersion enforcement executableKinds orphanedAttributeRows } }`

func TestFleetOperationReadinessAuthorizationAndCatalogFailure(t *testing.T) {
	base := profileLocationCtx(t)
	api := (&SchemaResolver{}).GetApi(base)
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(base)).AutoMigrate(&model.Device{}, &model.EntityAttribute{}, &model.AttributeGeneration{}))
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, tc := range []struct {
		name   string
		claims *auth.Claims
		want   string
	}{
		{"no authority", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"device:read"}}, auth.ErrForbidden.Error()},
		{"scoped", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"fleet:operate"}, ScopeAnchors: []string{"customer:acme"}}, auth.ErrScopeUnsupported.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := schema.Exec(auth.WithClaims(base, tc.claims), readinessQuery, "", nil)
			require.Len(t, res.Errors, 1)
			require.Equal(t, tc.want, res.Errors[0].Message)
		})
	}
	ctx := auth.WithClaims(base, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"fleet:operate"}})
	res := schema.Exec(ctx, readinessQuery, "", nil)
	require.Empty(t, res.Errors)
	// SQLite has no pg_catalog: a catalog error must preserve the other facts.
	require.JSONEq(t, `{"fleetOperationReadiness":{"protocolVersion":1,"enforcement":"UNREADABLE","executableKinds":[],"orphanedAttributeRows":0}}`, string(res.Data))
}

// GraphQL aliases may select orphanedAttributeRows many times in one request; the count must run
// once per resolver, its value and its error both reused.
func TestFleetOperationReadinessCountsOrphansOnce(t *testing.T) {
	calls := 0
	r := &FleetOperationReadinessResolver{orphans: func() (int64, error) { calls++; return 3, nil }}
	for range 200 {
		n, err := r.OrphanedAttributeRows()
		if err != nil || n == nil || *n != 3 {
			t.Fatalf("got %v, %v", n, err)
		}
	}
	if calls != 1 {
		t.Fatalf("the orphan count ran %d times for one resolver, want 1", calls)
	}
}
