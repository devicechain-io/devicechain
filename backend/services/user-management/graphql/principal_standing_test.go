// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

const principalStandingQuery = `query($principals:[String!]!,$authorities:[String!]!){
	principalStanding(principals:$principals,authorities:$authorities){principal qualifies}
}`

func TestPrincipalStandingWire(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)
	role := iam.Role{Scope: iam.ScopeTenant, Token: "reviewers", Authorities: []string{string(auth.DeviceWrite), string(auth.AlarmWrite)}}
	require.NoError(t, store.CreateRole(context.Background(), &role))
	id := iam.Identity{Email: "person@example.invalid", Enabled: true, PasswordHash: "x", Memberships: []iam.Membership{{TenantId: "acme", Enabled: true, TenantRoles: []iam.Role{role}}}}
	require.NoError(t, store.CreateIdentity(context.Background(), &id))
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, name := range []string{"person", "other service", "device-management", "agent service", "missing authority", "scoped service", "no claims"} {
		t.Run(name, func(t *testing.T) {
			claims := principalStandingServiceClaims()
			switch name {
			case "person":
				claims.TokenType = auth.TokenTypeAccess
				claims.Username = id.Email
			case "other service":
				claims.Subject = "event-processing"
			case "agent service":
				claims.ClientId = "agent"
			case "missing authority":
				claims.Authorities = nil
			case "scoped service":
				claims.ScopeAnchors = []string{"customer:one"}
			}
			ctx := context.Background()
			if name != "no claims" {
				ctx = auth.WithClaims(ctx, claims)
			}
			ctx = context.WithValue(core.WithTenant(ctx, "acme"), ContextIdentityKey, mgr)
			res := schema.Exec(ctx, principalStandingQuery, "", map[string]any{
				"principals":  []any{id.Email, "unknown@example.invalid", id.Email},
				"authorities": []any{string(auth.DeviceWrite), string(auth.AlarmWrite)},
			})
			if name != "device-management" {
				require.Len(t, res.Errors, 1)
				require.Equal(t, "FORBIDDEN", res.Errors[0].Extensions["code"])
				return
			}
			require.Empty(t, res.Errors)
			require.JSONEq(t, `{"principalStanding":[{"principal":"person@example.invalid","qualifies":true},{"principal":"unknown@example.invalid","qualifies":false}]}`, string(res.Data))
		})
	}
	ctx := context.WithValue(core.WithTenant(auth.WithClaims(context.Background(), principalStandingServiceClaims()), "acme"), ContextIdentityKey, mgr)
	res := schema.Exec(ctx, principalStandingQuery, "", map[string]any{
		"principals": standingVars(repeatedStandingInput(10, id.Email)), "authorities": standingVars(repeatedStandingInput(8, string(auth.DeviceWrite))),
	})
	require.Empty(t, res.Errors, "the maximum list lengths must be accepted")
	require.JSONEq(t, `{"principalStanding":[{"principal":"person@example.invalid","qualifies":true}]}`, string(res.Data))

	// A failure loading a member's authorities must fail the query, not answer false.
	require.NoError(t, db.Migrator().DropTable(&iam.Role{}))
	res = schema.Exec(ctx, principalStandingQuery, "", map[string]any{
		"principals": []any{"unknown@example.invalid", id.Email}, "authorities": []any{string(auth.DeviceWrite)},
	})
	require.NotEmpty(t, res.Errors)
	require.Equal(t, "null", string(res.Data), "a store error must not expose a partial answer")
}

func TestPrincipalStandingInputLimitsWire(t *testing.T) {
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	ctx := core.WithTenant(auth.WithClaims(context.Background(), principalStandingServiceClaims()), "acme")
	// No manager is injected: every invalid request must fail before accessing storage.
	for _, tc := range []struct {
		name        string
		principals  []string
		authorities []string
	}{
		{"no principals", []string{}, []string{"device:write"}},
		{"too many principals", repeatedStandingInput(11, "person@example.invalid"), []string{"device:write"}},
		{"empty principal", []string{""}, []string{"device:write"}},
		{"no authorities", []string{"person@example.invalid"}, []string{}},
		{"too many authorities", []string{"person@example.invalid"}, repeatedStandingInput(9, "device:write")},
		{"empty authority", []string{"person@example.invalid"}, []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := schema.Exec(ctx, principalStandingQuery, "", map[string]any{"principals": standingVars(tc.principals), "authorities": standingVars(tc.authorities)})
			require.Len(t, res.Errors, 1)
			require.Equal(t, integrity.CodeInvalid, res.Errors[0].Extensions["code"], res.Errors[0].Message)
		})
	}
}

func TestPrincipalStandingRequiresTenant(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), principalStandingServiceClaims())
	_, err := (&SchemaResolver{}).PrincipalStanding(ctx, struct {
		Principals  []string
		Authorities []string
	}{[]string{"person@example.invalid"}, []string{"device:write"}})
	require.ErrorIs(t, err, core.ErrNoTenant)
}

func principalStandingServiceClaims() *auth.Claims {
	claims := &auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{string(auth.TenantRead)}}
	claims.Subject = "device-management"
	return claims
}

// standingVars renders a list the way a decoded JSON request carries it.
func standingVars(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func repeatedStandingInput(n int, value string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = value
	}
	return out
}
