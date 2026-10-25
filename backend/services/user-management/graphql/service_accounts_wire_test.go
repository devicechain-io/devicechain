// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

func newServiceAccountWireEnv(t *testing.T) *wireEnv {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{},
		&iam.Membership{}, &iam.Invitation{}, &iam.IdentityLink{}, &iam.ServiceAccount{}, &iam.ServiceAccountSecret{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	svc := admin.NewService(store, 0, 0, nil)
	svc.SetViewerBaseline(identity.ViewerAuthorities())
	k, err := credential.DeriveServiceAccountSecretKey([]byte("graphql-service-account-test-key"))
	require.NoError(t, err)
	svc.SetServiceAccountSecretKey(k)
	ctx := context.Background()
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, store.CreateTenantTier(ctx, tier))
	require.NoError(t, store.CreateTenant(ctx, &iam.Tenant{Token: "acme", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
	require.NoError(t, store.CreateRole(ctx, &iam.Role{Scope: iam.ScopeTenant, Token: iam.TenantAdminRoleToken, Authorities: []string{"*"}}))
	require.NoError(t, store.CreateRole(ctx, &iam.Role{Scope: iam.ScopeTenant, Token: iam.ViewerRoleToken, Authorities: identity.ViewerAuthorities()}))
	_, err = svc.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "boss@acme.example", Password: "pw", Enabled: true})
	require.NoError(t, err)
	_, err = svc.AddMembership(ctx, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	require.NoError(t, err)
	return &wireEnv{
		schema: gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		svc:    svc, store: store,
		mgr: identity.NewManager(nil, rdbm, nil, nil, 0, 0, "https://dc.example.com/api/user-management", identity.BootstrapConfig{}, nil),
	}
}

const createServiceAccountDoc = `mutation($r: ServiceAccountCreateRequest!) {
  createServiceAccount(request: $r) {
    clientId clientSecret tokenEndpoint
    serviceAccount { token clientId enabled roles { token } scope { kind token } secrets { hint expiresAt active } }
  }
}`

// The secret is in the create answer and nowhere else: every read of the account — the
// list, the single read, the full field set of its secrets — is free of it.
func TestTheSecretIsReadableOnlyFromTheCreateAnswer(t *testing.T) {
	w := newServiceAccountWireEnv(t)
	data, errs := w.exec(t, bossClaims(), createServiceAccountDoc, map[string]any{"r": map[string]any{
		"token": "ci-bot", "roleTokens": []any{iam.ViewerRoleToken},
		"scope": []any{map[string]any{"kind": "CUSTOMER", "token": "acme-corp"}},
	}})
	require.Nil(t, errs)
	cred := data["createServiceAccount"].(map[string]any)
	secret := cred["clientSecret"].(string)
	require.True(t, strings.HasPrefix(secret, admin.ServiceAccountSecretPrefix))
	require.Equal(t, "https://dc.example.com/api/user-management/oauth/token", cred["tokenEndpoint"])

	for _, doc := range []string{
		`{ serviceAccount(token: "ci-bot") { token name description clientId enabled createdBy createdAt roles { token } scope { kind token } secrets { hint createdAt expiresAt lastUsedAt active } } }`,
		`{ serviceAccounts(criteria: {pageNumber: 1, pageSize: 10}) { results { token clientId secrets { hint createdAt expiresAt lastUsedAt active } } pagination { totalRecords } } }`,
	} {
		data, errs := w.exec(t, bossClaims(), doc, nil)
		require.Nil(t, errs)
		raw, err := json.Marshal(data)
		require.NoError(t, err)
		require.NotContains(t, string(raw), secret[len(admin.ServiceAccountSecretPrefix):], "a read returned the secret")
		require.Contains(t, string(raw), "ci-bot", "the negative control: the read did return the account")
	}

	// A scoped member cannot list the tenant's accounts.
	scoped := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "c@acme.example",
		Authorities: []string{"member:read"}, ScopeAnchors: []string{"customer:acme-corp"}}
	_, errs = w.exec(t, scoped, `{ serviceAccounts(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`, nil)
	require.NotNil(t, errs, "a scoped member listed the tenant's service accounts")

	// Without member:write, no create.
	reader := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@acme.example",
		Authorities: []string{"member:read"}}
	_, errs = w.exec(t, reader, createServiceAccountDoc, map[string]any{"r": map[string]any{"token": "x", "roleTokens": []any{}}})
	require.NotNil(t, errs)
}

// The SDL carries the cleartext secret in exactly one field, on one type, returned by
// exactly the two mutations that mint one; and no field anywhere is named for a digest.
func TestTheSchemaCarriesTheSecretInOnePlace(t *testing.T) {
	sdl := SchemaContent
	require.Equal(t, 1, strings.Count(sdl, "clientSecret:"), "clientSecret appears on more than one type")
	require.Regexp(t, regexp.MustCompile(`(?s)type ServiceAccountCredentials \{[^}]*clientSecret: String!`), sdl)
	var names []string
	for _, m := range regexp.MustCompile(`(?s)\n    (\w+)\((?:[^()]|\([^()]*\))*\): ServiceAccountCredentials!`).FindAllStringSubmatch(sdl, -1) {
		names = append(names, m[1])
	}
	require.ElementsMatch(t, []string{"createServiceAccount", "rotateServiceAccountSecret"}, names)
	require.NotRegexp(t, regexp.MustCompile(`(?i)\n\s+\w*digest\w*\s*[:(]`), sdl, "a field exposes a digest")
}
