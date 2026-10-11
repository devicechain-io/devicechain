// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
)

// The list bounds on the admin plane's OAuth-client and tier-order inputs, driven through
// the served admin schema so the refusal is asserted as the wire carries it: the
// LIMIT_EXCEEDED code, not an error string. Each bound is exercised AT the limit (which
// must be accepted, or at least not refused as over the limit) and one past it.

const (
	createClientDoc = `mutation($r: AdminOAuthClientCreateRequest!) { createOauthClient(request: $r) { client { clientId } } }`
	updateClientDoc = `mutation($id: String!, $r: AdminOAuthClientUpdateRequest!) { updateOauthClient(clientId: $id, request: $r) { clientId } }`
	reorderDoc      = `mutation($t: [String!]!) { reorderTenantTiers(orderedTokens: $t) { token } }`
	createTierDoc   = `mutation($r: AdminTenantTierCreateRequest!) { createTenantTier(request: $r) { token } }`
)

type capsFixture struct {
	t      *testing.T
	svc    *admin.Service
	schema *gqlcore.Schema
}

func newCapsFixture(t *testing.T) *capsFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&iam.TenantTier{}, &iam.Tenant{}, &iam.OAuthClient{}))
	svc := admin.NewService(iam.NewStore(&rdb.RdbManager{Database: db}), 300*time.Second, 12*time.Hour, nil)
	return &capsFixture{t: t, svc: svc, schema: gqlcore.MustParseSchema(AdminSchemaContent, &AdminResolver{})}
}

// exec runs doc as a caller holding authorities and returns the first error's code ("" for
// no error, "<no code>" for an error without one).
func (f *capsFixture) exec(doc string, vars map[string]any, authorities ...string) string {
	f.t.Helper()
	ctx := context.WithValue(adminCtx(authorities...), ContextAdminKey, f.svc)
	res := f.schema.Exec(ctx, doc, "", vars)
	if len(res.Errors) == 0 {
		return ""
	}
	if code, ok := res.Errors[0].Extensions["code"].(string); ok {
		return code
	}
	return "<no code>: " + res.Errors[0].Message
}

func strs(n int, f func(i int) string) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func redirectURIs(n int) []any {
	return strs(n, func(i int) string { return fmt.Sprintf("https://app.example.com/cb/%d", i) })
}

// uriOfLen is an https redirect URI exactly n characters long.
func uriOfLen(n int) string {
	base := "https://app.example.com/"
	return base + strings.Repeat("a", n-len(base))
}

func readOnlyScopes(n int) []any { return strs(n, func(int) string { return auth.ScopeReadOnly }) }

func clientRequest(id string, uris, scopes []any) map[string]any {
	return map[string]any{"r": map[string]any{"clientId": id, "redirectUris": uris, "scopes": scopes}}
}

func TestOAuthClientRedirectURIsAreBounded(t *testing.T) {
	f := newCapsFixture(t)
	w := string(auth.ClientWrite)

	require.Equal(t, "", f.exec(createClientDoc, clientRequest("at-count", redirectURIs(admin.MaxClientRedirectURIs), readOnlyScopes(1)), w),
		"a client with exactly the maximum number of redirect URIs must register")
	require.Equal(t, limit.Code, f.exec(createClientDoc, clientRequest("over-count", redirectURIs(admin.MaxClientRedirectURIs+1), readOnlyScopes(1)), w))

	require.Equal(t, "", f.exec(createClientDoc, clientRequest("at-len", []any{uriOfLen(admin.MaxClientRedirectURILen)}, readOnlyScopes(1)), w),
		"a redirect URI of exactly the maximum length must register")
	require.Equal(t, limit.Code, f.exec(createClientDoc, clientRequest("over-len", []any{uriOfLen(admin.MaxClientRedirectURILen + 1)}, readOnlyScopes(1)), w))

	// The update path validates the resulting list by the same rules.
	upd := func(uris []any) map[string]any {
		return map[string]any{"id": "at-count", "r": map[string]any{"redirectUris": uris}}
	}
	require.Equal(t, "", f.exec(updateClientDoc, upd(redirectURIs(admin.MaxClientRedirectURIs)), w))
	require.Equal(t, limit.Code, f.exec(updateClientDoc, upd(redirectURIs(admin.MaxClientRedirectURIs+1)), w))
	require.Equal(t, limit.Code, f.exec(updateClientDoc, upd([]any{uriOfLen(admin.MaxClientRedirectURILen + 1)}), w))
}

func TestOAuthClientScopesAreBounded(t *testing.T) {
	f := newCapsFixture(t)
	w := string(auth.ClientWrite)
	uris := redirectURIs(1)

	require.Equal(t, "", f.exec(createClientDoc, clientRequest("at-count", uris, readOnlyScopes(admin.MaxClientScopes)), w),
		"a client with exactly the maximum number of scope entries must register")
	require.Equal(t, limit.Code, f.exec(createClientDoc, clientRequest("over-count", uris, readOnlyScopes(admin.MaxClientScopes+1)), w))

	// No supported scope is that long, so an entry AT the length bound is refused as an
	// unknown scope, but not as over the limit; one past it is refused as over the limit.
	atLen := f.exec(createClientDoc, clientRequest("at-len", uris, []any{strings.Repeat("s", admin.MaxClientScopeLen)}), w)
	require.NotEqual(t, "", atLen, "an unknown scope must still be refused")
	require.NotEqual(t, limit.Code, atLen, "a scope entry of exactly the maximum length is not over the limit")
	require.Equal(t, limit.Code, f.exec(createClientDoc, clientRequest("over-len", uris, []any{strings.Repeat("s", admin.MaxClientScopeLen+1)}), w))

	upd := func(scopes []any) map[string]any {
		return map[string]any{"id": "at-count", "r": map[string]any{"scopes": scopes}}
	}
	require.Equal(t, "", f.exec(updateClientDoc, upd(readOnlyScopes(admin.MaxClientScopes)), w))
	require.Equal(t, limit.Code, f.exec(updateClientDoc, upd(readOnlyScopes(admin.MaxClientScopes+1)), w))
}

// seedTiers registers n tiers directly through the service.
func (f *capsFixture) seedTiers(n int) []any {
	f.t.Helper()
	tokens := make([]any, n)
	for i := range n {
		tok := fmt.Sprintf("tier-%03d", i)
		_, err := f.svc.CreateTenantTier(context.Background(), admin.TierInput{Token: tok})
		require.NoError(f.t, err)
		tokens[i] = tok
	}
	return tokens
}

func TestTierCatalogAndReorderAreBounded(t *testing.T) {
	f := newCapsFixture(t)
	w := string(auth.TenantWrite)

	// The catalog fills to exactly its bound through the mutation, and refuses one more.
	tokens := f.seedTiers(admin.MaxTenantTiers - 1)
	require.Equal(t, "", f.exec(createTierDoc, map[string]any{"r": map[string]any{"token": "tier-last"}}, w),
		"the catalog must accept its hundredth tier")
	tokens = append(tokens, "tier-last")
	require.Equal(t, limit.Code, f.exec(createTierDoc, map[string]any{"r": map[string]any{"token": "tier-over"}}, w))

	// A reorder naming exactly the full catalog is accepted; one entry more is refused as
	// over the limit, before the store can call it a mismatch.
	reversed := make([]any, len(tokens))
	for i, tok := range tokens {
		reversed[len(tokens)-1-i] = tok
	}
	require.Equal(t, "", f.exec(reorderDoc, map[string]any{"t": reversed}, w))
	require.Equal(t, limit.Code, f.exec(reorderDoc, map[string]any{"t": append(append([]any{}, reversed...), "tier-000")}, w))

	// Per entry: a token of the maximum length is a mismatch (no such tier), not a limit;
	// one character longer is the limit.
	atLen := append(append([]any{}, reversed[1:]...), strings.Repeat("t", core.MaxTokenLen))
	code := f.exec(reorderDoc, map[string]any{"t": atLen}, w)
	require.NotEqual(t, "", code, "a reorder naming a tier that does not exist must be refused")
	require.NotEqual(t, limit.Code, code, "a token of exactly the maximum length is not over the limit")
	overLen := append(append([]any{}, reversed[1:]...), strings.Repeat("t", core.MaxTokenLen+1))
	require.Equal(t, limit.Code, f.exec(reorderDoc, map[string]any{"t": overLen}, w))
}

// Authorization is decided before the bound: a caller without the authority gets the
// authorization refusal for an over-limit request, never LIMIT_EXCEEDED — which would tell
// an unauthorized caller something about the mutation it may not call.
func TestAuthorizationIsCheckedBeforeTheListBounds(t *testing.T) {
	f := newCapsFixture(t)
	wrong := string(auth.UserWrite)

	for name, tc := range map[string]struct {
		doc  string
		vars map[string]any
	}{
		"createOauthClient": {createClientDoc, clientRequest("c", redirectURIs(admin.MaxClientRedirectURIs+1), readOnlyScopes(admin.MaxClientScopes+1))},
		"updateOauthClient": {updateClientDoc, map[string]any{"id": "c", "r": map[string]any{"redirectUris": redirectURIs(admin.MaxClientRedirectURIs + 1)}}},
		"reorderTenantTiers": {reorderDoc, map[string]any{"t": strs(admin.MaxTenantTiers+1, func(i int) string {
			return fmt.Sprintf("t%d", i)
		})}},
	} {
		t.Run(name, func(t *testing.T) {
			code := f.exec(tc.doc, tc.vars, wrong)
			require.NotEqual(t, "", code)
			require.NotEqual(t, limit.Code, code, "the bound was checked before authorization")
			ctx := context.WithValue(adminCtx(wrong), ContextAdminKey, f.svc)
			res := f.schema.Exec(ctx, tc.doc, "", tc.vars)
			require.Contains(t, res.Errors[0].Message, auth.ErrForbidden.Error())
		})
	}
}
