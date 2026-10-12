// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	umgraphql "github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

// The sign-out as a client reaches it: the served schema, the real HTTP handler on the
// unauthenticated main endpoint, the real resolver over a real identity.Manager. Only the
// token strings cross between calls, as they do from the console.

type served struct {
	h http.Handler
}

func newServed(t *testing.T) *served {
	t.Helper()
	mgr := identity.NewServedManager(t)
	schema := gqlcore.MustParseSchema(umgraphql.SchemaContent, &umgraphql.SchemaResolver{})
	return &served{h: gqlcore.NewHttpHandler(schema, map[gqlcore.ContextKey]interface{}{umgraphql.ContextIdentityKey: mgr}, nil)}
}

type reply struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (s *served) do(t *testing.T, doc string, vars map[string]any) reply {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": doc, "variables": vars})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	var out reply
	require.NoErrorf(t, json.Unmarshal(rec.Body.Bytes(), &out), "not GraphQL JSON (status %d): %s", rec.Code, rec.Body.String())
	return out
}

// field decodes one top-level data field into v.
func (r reply) field(t *testing.T, name string, v any) {
	t.Helper()
	require.Emptyf(t, r.Errors, "unexpected errors: %+v", r.Errors)
	require.NoError(t, json.Unmarshal(r.Data[name], v))
}

func (r reply) failedWith(t *testing.T, msg string) {
	t.Helper()
	require.Len(t, r.Errors, 1, "want exactly one error, got %+v (data %s)", r.Errors, r.Data)
	require.Equal(t, msg, r.Errors[0].Message)
}

const (
	loginDoc   = `mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}`
	selectDoc  = `mutation($i:String!){selectTenant(identityToken:$i,tenant:"acme"){accessToken refreshToken}}`
	refreshDoc = `mutation($r:String!){refresh(refreshToken:$r){accessToken refreshToken}}`
	logoutDoc  = `mutation($t:String!){logout(token:$t,returnTo:CONSOLE){providerSignOutUrl}}`
)

func (s *served) signIn(t *testing.T) (identityToken, refreshToken string) {
	t.Helper()
	var login struct{ IdentityToken string }
	s.do(t, loginDoc, map[string]any{"e": "pat@example.com", "p": "pw-1"}).field(t, "login", &login)
	var tok struct{ RefreshToken string }
	s.do(t, selectDoc, map[string]any{"i": login.IdentityToken}).field(t, "selectTenant", &tok)
	return login.IdentityToken, tok.RefreshToken
}

// After logout over HTTP: refresh is refused, the identity token cannot start a new chain,
// and a second sign-in of the same account keeps working.
func TestLogoutMutationEndsTheSignInOverHTTP(t *testing.T) {
	s := newServed(t)
	idTok, refresh := s.signIn(t)
	_, otherRefresh := s.signIn(t)

	// A password sign-in has no provider to sign out at, even when an app is named.
	var out struct{ ProviderSignOutUrl *string }
	s.do(t, logoutDoc, map[string]any{"t": refresh}).field(t, "logout", &out)
	require.Nil(t, out.ProviderSignOutUrl)

	s.do(t, refreshDoc, map[string]any{"r": refresh}).failedWith(t, "invalid or expired token")
	s.do(t, selectDoc, map[string]any{"i": idTok}).failedWith(t, "invalid or expired token")

	var pair struct{ RefreshToken string }
	s.do(t, refreshDoc, map[string]any{"r": otherRefresh}).field(t, "refresh", &pair)
	require.NotEmpty(t, pair.RefreshToken, "another sign-in was ended by this one's logout")

	// Repeating it, or sending the identity token of the ended sign-in, still succeeds.
	s.do(t, logoutDoc, map[string]any{"t": refresh}).field(t, "logout", &out)
	s.do(t, logoutDoc, map[string]any{"t": idTok}).field(t, "logout", &out)

	s.do(t, logoutDoc, map[string]any{"t": "not-a-token"}).failedWith(t, "invalid or expired token")
}

// The return app is an ENUM on the served schema, so a caller cannot hand the server an
// address: anything but CONSOLE or DASHBOARD is refused before a resolver runs, and the
// sign-in is not ended by the refused request.
func TestLogoutRefusesAReturnThatIsNotOneOfTheApps(t *testing.T) {
	s := newServed(t)
	idTok, refresh := s.signIn(t)
	const doc = `mutation($t:String!,$r:SignOutReturn){logout(token:$t,returnTo:$r){providerSignOutUrl}}`

	for _, bad := range []string{"https://evil.example/", "/login", "console", ""} {
		r := s.do(t, doc, map[string]any{"t": refresh, "r": bad})
		require.NotEmptyf(t, r.Errors, "returnTo %q was accepted", bad)
	}
	// The refused requests ended nothing: the sign-in still refreshes.
	var pair struct{ RefreshToken string }
	s.do(t, refreshDoc, map[string]any{"r": refresh}).field(t, "refresh", &pair)
	require.NotEmpty(t, pair.RefreshToken)

	// Control: an app the schema names is accepted, and ends the sign-in.
	var out struct{ ProviderSignOutUrl *string }
	s.do(t, doc, map[string]any{"t": idTok, "r": "DASHBOARD"}).field(t, "logout", &out)
	require.Nil(t, out.ProviderSignOutUrl, "a password sign-in has no provider to sign out at")
	s.do(t, refreshDoc, map[string]any{"r": pair.RefreshToken}).failedWith(t, "invalid or expired token")
}
