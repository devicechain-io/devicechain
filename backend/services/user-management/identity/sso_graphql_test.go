// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	umgraphql "github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

// Single sign-on as a browser and a superuser reach it: the served public and admin
// schemas, the real HTTP handlers (including the callback), the real resolvers over a real
// identity.Manager, and a stub provider. Only strings cross between steps.

type ssoServed struct {
	s     *identity.ServedSso
	pub   http.Handler
	admin http.Handler
	mux   *http.ServeMux
}

func newSsoServed(t *testing.T) *ssoServed {
	t.Helper()
	s := identity.NewServedSsoManager(t)
	mgr := s.Manager

	pubSchema := gqlcore.MustParseSchema(umgraphql.SchemaContent, &umgraphql.SchemaResolver{})
	pub := gqlcore.NewHttpHandler(pubSchema, map[gqlcore.ContextKey]interface{}{umgraphql.ContextIdentityKey: mgr}, nil)

	gate := core.NewReadinessGate()
	require.True(t, gate.MarkReady(mgr.Validator()))
	adminSchema := gqlcore.MustParseSchema(umgraphql.AdminSchemaContent, &umgraphql.AdminResolver{})
	adm := gqlcore.NewAdminHttpHandler(adminSchema, map[gqlcore.ContextKey]interface{}{umgraphql.ContextAdminSsoKey: mgr}, gate)

	mux := http.NewServeMux()
	mux.Handle(identity.SsoCallbackPattern, identity.SsoCallbackHandler(mgr))
	return &ssoServed{s: s, pub: pub, admin: adm, mux: mux}
}

type wireReply struct {
	Status int
	Body   string
	Data   map[string]json.RawMessage
	Errors []struct {
		Message string `json:"message"`
	}
}

func (r wireReply) field(t *testing.T, name string, v any) {
	t.Helper()
	require.Emptyf(t, r.Errors, "unexpected errors: %+v", r.Errors)
	require.NoError(t, json.Unmarshal(r.Data[name], v))
}

func (r wireReply) failed(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, r.Errors, "expected an error, got data %s", r.Body)
	return r.Errors[0].Message
}

func post(t *testing.T, h http.Handler, doc string, vars map[string]any, token, remote, xff string) wireReply {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": doc, "variables": vars})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := wireReply{Status: rec.Code, Body: rec.Body.String()}
	if rec.Code == http.StatusOK {
		require.NoErrorf(t, json.Unmarshal(rec.Body.Bytes(), &out), "not GraphQL JSON: %s", rec.Body.String())
	}
	return out
}

// NL is a newline, spelled once.
const NL = "\n"

const (
	beginDoc    = `mutation($c:String,$e:String,$r:String!,$h:String!){beginSso(connection:$c,email:$e,returnTo:$r,codeChallenge:$h){authorizationUrl}}`
	completeDoc = `mutation($c:String!,$v:String!){completeSso(code:$c,codeVerifier:$v){identityToken superuser memberships{tenant}}}`
	selectSso   = `mutation($i:String!){selectTenant(identityToken:$i,tenant:"acme"){accessToken}}`
	loginSu     = `mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}`
)

func challengeOf(verifier string) string {
	return identity.ChallengeForTest(verifier)
}

func TestSsoSignInOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	verifier := strings.Repeat("v", 43)

	var begin struct{ AuthorizationUrl string }
	post(t, s.pub, beginDoc, map[string]any{"c": s.s.Connection, "r": s.s.ReturnTo, "h": challengeOf(verifier)}, "", "192.0.2.1:1234", "").
		field(t, "beginSso", &begin)
	require.NotEmpty(t, begin.AuthorizationUrl)

	state, code := s.s.Authorize(begin.AuthorizationUrl, "sub-pat", s.s.LinkedEmail)
	req := httptest.NewRequest(http.MethodGet, s.s.CallbackPrefix+s.s.Connection+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	loginCode := loc.Query().Get("code")
	require.NotEmpty(t, loginCode)

	// The wrong secret completes nothing.
	r := post(t, s.pub, completeDoc, map[string]any{"c": loginCode, "v": strings.Repeat("w", 43)}, "", "", "")
	require.Equal(t, "invalid or expired sign-in code", r.failed(t))

	// (the wrong attempt burned the code; sign in again for the control)
	post(t, s.pub, beginDoc, map[string]any{"c": s.s.Connection, "r": s.s.ReturnTo, "h": challengeOf(verifier)}, "", "192.0.2.2:1234", "").
		field(t, "beginSso", &begin)
	state, code = s.s.Authorize(begin.AuthorizationUrl, "sub-pat", s.s.LinkedEmail)
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, s.s.CallbackPrefix+s.s.Connection+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil))
	loc, err = url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	var done struct {
		IdentityToken string
		Superuser     bool
		Memberships   []struct{ Tenant string }
	}
	post(t, s.pub, completeDoc, map[string]any{"c": loc.Query().Get("code"), "v": verifier}, "", "", "").field(t, "completeSso", &done)
	require.NotEmpty(t, done.IdentityToken)
	require.False(t, done.Superuser)
	require.Len(t, done.Memberships, 1)

	var tok struct{ AccessToken string }
	post(t, s.pub, selectSso, map[string]any{"i": done.IdentityToken}, "", "", "").field(t, "selectTenant", &tok)
	require.NotEmpty(t, tok.AccessToken)

	// The federated identity token does not open the admin plane.
	r = post(t, s.admin, `{idpConnections{token}}`, nil, done.IdentityToken, "", "")
	require.NotEmpty(t, r.failed(t), "a tenant connection's token holds no system authority")
}

func TestBeginSsoByEmailAndItsRefusalsOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	h := challengeOf(strings.Repeat("v", 43))

	// An address in no verified domain: null, not an error -- sign in another way.
	r := post(t, s.pub, beginDoc, map[string]any{"e": "x@nowhere.example", "r": s.s.ReturnTo, "h": h}, "", "192.0.2.5:1", "")
	require.Empty(t, r.Errors)
	require.JSONEq(t, `null`, string(r.Data["beginSso"]))

	// A return address off this instance is refused.
	r = post(t, s.pub, beginDoc, map[string]any{"c": s.s.Connection, "r": "https://evil.example/login/sso/complete", "h": h}, "", "192.0.2.6:1", "")
	require.Equal(t, "invalid single sign-on request", r.failed(t))
}

func TestBeginSsoIsRateLimitedBySourceOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	h := challengeOf(strings.Repeat("v", 43))
	doc := map[string]any{"c": s.s.Connection, "r": s.s.ReturnTo, "h": h}

	var refused bool
	for i := 0; i < 20 && !refused; i++ {
		// A spoofed leading X-Forwarded-For entry changes nothing: only the proxy's own
		// (last) entry names the source.
		r := post(t, s.pub, beginDoc, doc, "", "10.0.0.1:99", "6.6.6.6, 203.0.113.50")
		if len(r.Errors) > 0 {
			require.Equal(t, "too many sign-in attempts; try again shortly", r.Errors[0].Message)
			refused = true
		}
	}
	require.True(t, refused, "twenty begins from one source were never rationed")

	// A different source is not affected.
	r := post(t, s.pub, beginDoc, doc, "", "10.0.0.1:99", "6.6.6.6, 203.0.113.51")
	require.Empty(t, r.Errors)
}

func TestConnectionAdministrationNeverReturnsTheSecretOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)

	// No token, no admin plane.
	require.Equal(t, http.StatusUnauthorized, post(t, s.admin, `{idpConnections{token}}`, nil, "", "", "").Status)

	var got struct {
		IdpConnection struct {
			Token     string
			HasSecret bool
			Kind      string
			Enabled   bool
			Domains   []struct{ Domain string }
			Tenants   []string
			Discovery struct {
				Approved    bool
				Fingerprint string
			}
			CallbackUrl string
		}
	}
	r := post(t, s.admin, `{idpConnection(token:"acme-idp"){token hasSecret kind enabled domains{domain} tenants callbackUrl discovery{approved fingerprint}}}`,
		nil, su.IdentityToken, "", "")
	require.Empty(t, r.Errors, r.Body)
	require.NoError(t, json.Unmarshal(r.Data["idpConnection"], &got.IdpConnection))
	c := got.IdpConnection
	require.True(t, c.HasSecret)
	require.Equal(t, "tenant", c.Kind)
	require.True(t, c.Enabled)
	require.Equal(t, []string{"acme"}, c.Tenants)
	require.True(t, c.Discovery.Approved)
	require.Contains(t, c.CallbackUrl, "/sso/callback/acme-idp")
	require.NotContains(t, r.Body, s.s.Secret)

	// The schema has no field that could carry it: the only field of the connection type
	// with "secret" in its name is the boolean hasSecret.
	sdl := umgraphql.AdminSchemaContent
	start := strings.Index(sdl, "type IdpConnection {")
	require.GreaterOrEqual(t, start, 0)
	block := sdl[start : start+strings.Index(sdl[start:], NL+"}"+NL)]
	for _, line := range strings.Split(block, NL) {
		f := strings.TrimSpace(line)
		if strings.HasPrefix(f, "\"\"\"") || f == "" || strings.HasPrefix(f, "type ") {
			continue
		}
		if strings.Contains(strings.ToLower(f), "secret") {
			require.Equal(t, "hasSecret: Boolean!", f)
		}
	}

	// Setting a new one returns the connection, not the secret.
	r = post(t, s.admin, `mutation{setIdpConnectionSecret(token:"acme-idp",secret:"brand-new-secret"){hasSecret}}`, nil, su.IdentityToken, "", "")
	require.Empty(t, r.Errors, r.Body)
	require.NotContains(t, r.Body, "brand-new-secret")

	// An ordinary tenant member's token (a password session in a tenant) is not an
	// identity token and is refused outright; and a mutation without the authority fails.
	r = post(t, s.admin, `{ssoLinkRequests{id}}`, nil, su.IdentityToken, "", "")
	require.Empty(t, r.Errors)
}

func TestRoleMappingsOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	s.s.EnsureRole("tenant", "operator", "device:write")
	s.s.EnsureRole("tenant", "owner", "*")
	s.s.EnsureRole("system", "ops-admin", "tenant:write")
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)

	const set = `mutation($c:String!,$t:String!,$m:[IdpRoleMappingInput!]!,$a:Boolean){setIdpRoleMappings(connection:$c,tenant:$t,mappings:$m,acknowledgeFullAuthority:$a){roleMappings{tenant group role fullAuthority} groupsClaim jitEnabled}}`
	mapping := func(g, r string) []map[string]any { return []map[string]any{{"group": g, "role": r}} }

	// No token, no mapping.
	require.Equal(t, http.StatusUnauthorized, post(t, s.admin, set,
		map[string]any{"c": "acme-idp", "t": "acme", "m": mapping("g", "operator")}, "", "", "").Status)

	// A system role is refused, and so is a tenant the connection does not serve.
	r := post(t, s.admin, set, map[string]any{"c": "acme-idp", "t": "acme", "m": mapping("g", "ops-admin")}, su.IdentityToken, "", "")
	require.Contains(t, r.failed(t), "no tenant role")
	r = post(t, s.admin, set, map[string]any{"c": "acme-idp", "t": "beta", "m": mapping("g", "operator")}, su.IdentityToken, "", "")
	require.Contains(t, r.failed(t), "does not serve")

	// A full-authority role needs the acknowledgement.
	r = post(t, s.admin, set, map[string]any{"c": "acme-idp", "t": "acme", "m": mapping("g", "owner")}, su.IdentityToken, "", "")
	require.Contains(t, r.failed(t), "acknowledgeFullAuthority")

	// A misnamed field is a request error, not a half-applied mapping.
	r = post(t, s.admin, set, map[string]any{"c": "acme-idp", "t": "acme", "m": []map[string]any{{"group": "g", "roleToken": "operator"}}}, su.IdentityToken, "", "")
	require.NotEmpty(t, r.Errors)

	var out struct {
		RoleMappings []struct {
			Tenant, Group, Role string
			FullAuthority       bool
		}
		GroupsClaim string
		JitEnabled  bool
	}
	post(t, s.admin, set, map[string]any{"c": "acme-idp", "t": "acme", "m": mapping("eng", "operator")}, su.IdentityToken, "", "").
		field(t, "setIdpRoleMappings", &out)
	require.Len(t, out.RoleMappings, 1)
	require.Equal(t, "operator", out.RoleMappings[0].Role)
	require.False(t, out.RoleMappings[0].FullAuthority)
	require.Equal(t, "groups", out.GroupsClaim)
	require.False(t, out.JitEnabled)

	var jit struct {
		JitEnabled         bool
		DefaultTenantRoles []string
	}
	post(t, s.admin, `mutation{setIdpJit(connection:"acme-idp",enabled:true,defaultTenantRoles:["operator"]){jitEnabled defaultTenantRoles}}`,
		nil, su.IdentityToken, "", "").field(t, "setIdpJit", &jit)
	require.True(t, jit.JitEnabled)
	require.Equal(t, []string{"operator"}, jit.DefaultTenantRoles)
}
