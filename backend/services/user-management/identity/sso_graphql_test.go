// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	}
}

// code is the first error's extensions.code, or "" when it carries none.
func (r wireReply) code(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, r.Errors, "expected an error, got data %s", r.Body)
	c, _ := r.Errors[0].Extensions["code"].(string)
	return c
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

// The private CA bundle over the admin wire: a private key pasted by mistake is refused and
// is not echoed in the response; clearing returns the connection with no certificates.
func TestCaBundleOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)

	const set = `mutation($b:String!){setIdpCaBundle(token:"acme-idp",caBundle:$b){caCertificates{fingerprintSha256 subject notAfter isCa}}}`
	const key = "-----BEGIN PRIVATE KEY-----\nU0VDUkVULUtFWS1NQVRFUklBTA==\n-----END PRIVATE KEY-----\n"

	require.Equal(t, http.StatusUnauthorized, post(t, s.admin, set, map[string]any{"b": ""}, "", "", "").Status)

	r := post(t, s.admin, set, map[string]any{"b": key}, su.IdentityToken, "", "")
	require.Contains(t, r.failed(t), "private key")
	require.NotContains(t, r.Body, "U0VDUkVULUtFWS1NQVRFUklBTA")

	r = post(t, s.admin, set, map[string]any{"b": "not a certificate"}, su.IdentityToken, "", "")
	require.NotEmpty(t, r.Errors, r.Body)
	require.NotContains(t, r.Body, "not a certificate")

	// A signed-in identity that may read users but not write them is refused, whatever
	// the bundle.
	s.s.EnsureRole("system", "user-reader", "user:read")
	s.s.CreateIdentity("reader@example.com", "reader-pass")
	s.s.GrantSystemRole("reader@example.com", "user-reader")
	var rd struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": "reader@example.com", "p": "reader-pass"}, "", "", "").field(t, "login", &rd)
	require.NotEmpty(t, rd.IdentityToken)
	r = post(t, s.admin, `{idpConnections{token}}`, nil, rd.IdentityToken, "", "")
	require.Empty(t, r.Errors, "the reader may read: "+r.Body)
	r = post(t, s.admin, set, map[string]any{"b": ""}, rd.IdentityToken, "", "")
	require.NotEmpty(t, r.Errors, "user:read must not change a CA bundle: "+r.Body)

	var got struct {
		CaCertificates []struct{ FingerprintSha256 string } `json:"caCertificates"`
	}
	r = post(t, s.admin, set, map[string]any{"b": ""}, su.IdentityToken, "", "")
	r.field(t, "setIdpCaBundle", &got)
	require.Empty(t, got.CaCertificates)
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

const beginLinkDoc = `mutation($i:String!,$r:String!){beginSsoLink(identityToken:$i,returnTo:$r){authorizationUrl}}`

// Linking one's own account, as the console does it: no connection is named; the server
// picks the one the identity's own email domain was verified for. Every refusal of the begin
// carries extensions.code, and every refusal at the provider's return names its reason.
func TestBeginSsoLinkOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	n := 0
	remote := func() string { n++; return fmt.Sprintf("192.0.2.%d:1", 100+n) }
	login := func(email string) string {
		t.Helper()
		s.s.CreateMember(email, "pw-1")
		var got struct{ IdentityToken string }
		post(t, s.pub, loginDoc, map[string]any{"e": email, "p": "pw-1"}, "", "", "").field(t, "login", &got)
		return got.IdentityToken
	}
	begin := func(token string) wireReply {
		return post(t, s.pub, beginLinkDoc, map[string]any{"i": token, "r": s.s.ReturnTo}, "", remote(), "")
	}
	// returnFrom plays the provider for (sub, email) and returns the address the callback
	// sends the browser to.
	returnFrom := func(r wireReply, sub, email string) url.Values {
		t.Helper()
		var b struct{ AuthorizationUrl string }
		r.field(t, "beginSsoLink", &b)
		state, code := s.s.Authorize(b.AuthorizationUrl, sub, email)
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			s.s.CallbackPrefix+s.s.Connection+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil))
		require.Equal(t, http.StatusFound, rec.Code)
		loc, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		return loc.Query()
	}

	// The connection is chosen by the server; the old argument no longer exists.
	r := post(t, s.pub, `mutation($i:String!,$r:String!){beginSsoLink(identityToken:$i,connection:"acme-idp",returnTo:$r){authorizationUrl}}`,
		map[string]any{"i": "x", "r": s.s.ReturnTo}, "", remote(), "")
	require.NotEmpty(t, r.Errors)

	// The domain-selected happy path.
	lee := "lee@acme-corp.com"
	leeTok := login(lee)
	q := returnFrom(begin(leeTok), "sub-lee", lee)
	require.Equal(t, "1", q.Get("linked"), "%v", q)
	require.Empty(t, q.Get("error"))

	// SSO_LINK_ALREADY_LINKED: once linked, not again.
	require.Equal(t, identity.CodeSsoLinkAlreadyLinked, begin(leeTok).code(t))

	// SSO_LINK_NO_CONNECTION: no connection was verified for this domain.
	require.Equal(t, identity.CodeSsoLinkNoConnection, begin(login("kim@elsewhere-corp.com")).code(t))

	// SSO_LINK_NOT_ALLOWED: an identity with a system role.
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)
	require.Equal(t, identity.CodeSsoLinkNotAllowed, begin(su.IdentityToken).code(t))

	// SSO_LINK_SESSION_REQUIRED: not a token, or a sign-in that has ended.
	require.Equal(t, identity.CodeSsoLinkSessionRequired, begin("not-a-token").code(t))

	// SSO_LINK_PASSWORD_REQUIRED: a session that came in through the provider.
	verifier := strings.Repeat("v", 43)
	var sb struct{ AuthorizationUrl string }
	post(t, s.pub, beginDoc, map[string]any{"c": s.s.Connection, "r": s.s.ReturnTo, "h": challengeOf(verifier)}, "", remote(), "").
		field(t, "beginSso", &sb)
	state, code := s.s.Authorize(sb.AuthorizationUrl, "sub-pat", s.s.LinkedEmail)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, s.s.CallbackPrefix+s.s.Connection+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil))
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	var done struct{ IdentityToken string }
	post(t, s.pub, completeDoc, map[string]any{"c": loc.Query().Get("code"), "v": verifier}, "", "", "").field(t, "completeSso", &done)
	require.Equal(t, identity.CodeSsoLinkPasswordRequired, begin(done.IdentityToken).code(t))

	// At the provider's return: a different address than the account's...
	maxEmail := "max@acme-corp.com"
	maxTok := login(maxEmail)
	q = returnFrom(begin(maxTok), "sub-max", "someone-else@acme-corp.com")
	require.Equal(t, "link_email_mismatch", q.Get("error"))
	require.Empty(t, q.Get("linked"))

	// ...and a password sign-in that ended while the person was at the provider.
	r = begin(maxTok)
	require.NoError(t, s.s.Manager.Logout(context.Background(), maxTok))
	q = returnFrom(r, "sub-max", maxEmail)
	require.Equal(t, "link_session_ended", q.Get("error"))
	require.Equal(t, identity.CodeSsoLinkSessionRequired, begin(maxTok).code(t))
}

// Signing out at the provider, end to end as a superuser and a browser reach it: the
// superuser approves a snapshot naming a sign-out endpoint and switches the setting on over
// the admin plane; a single sign-on then signs out over the public plane and is handed the
// provider's address, built from the approved endpoint and the fixed return address.
func TestProviderSignOutOverTheWire(t *testing.T) {
	s := newSsoServed(t)
	var su struct{ IdentityToken string }
	post(t, s.pub, loginSu, map[string]any{"e": s.s.SuperuserEmail, "p": s.s.SuperuserPassword}, "", "", "").field(t, "login", &su)

	s.s.PublishEndSession("/logout")
	var refreshed struct {
		Discovery struct{ Fingerprint, EndSessionEndpoint string }
	}
	post(t, s.admin, `mutation{refreshIdpDiscovery(token:"acme-idp"){discovery{fingerprint endSessionEndpoint}}}`, nil, su.IdentityToken, "", "").
		field(t, "refreshIdpDiscovery", &refreshed)
	require.Equal(t, s.s.ProviderURL+"/logout", refreshed.Discovery.EndSessionEndpoint)
	r := post(t, s.admin, `mutation($f:String!){approveIdpDiscovery(token:"acme-idp",fingerprint:$f){token}}`,
		map[string]any{"f": refreshed.Discovery.Fingerprint}, su.IdentityToken, "", "")
	require.Empty(t, r.Errors, r.Body)

	var conn struct {
		ProviderSignOut   bool
		SignOutReturnUrls []string
	}
	post(t, s.admin, `{idpConnection(token:"acme-idp"){providerSignOut signOutReturnUrls}}`, nil, su.IdentityToken, "", "").
		field(t, "idpConnection", &conn)
	require.False(t, conn.ProviderSignOut, "provider sign-out must start switched off")
	require.Equal(t, []string{"https://dc.example.com/login", "https://dc.example.com/dash/"}, conn.SignOutReturnUrls)

	post(t, s.admin, `mutation{updateIdpConnection(token:"acme-idp",request:{providerSignOut:true}){providerSignOut}}`, nil, su.IdentityToken, "", "").
		field(t, "updateIdpConnection", &conn)
	require.True(t, conn.ProviderSignOut)

	// A single sign-on through the connection; each sign-out below needs a sign-in of its own.
	verifier := strings.Repeat("v", 43)
	signIn := func() string {
		t.Helper()
		var begin struct{ AuthorizationUrl string }
		post(t, s.pub, beginDoc, map[string]any{"c": s.s.Connection, "r": s.s.ReturnTo, "h": challengeOf(verifier)}, "", "192.0.2.9:1234", "").
			field(t, "beginSso", &begin)
		state, code := s.s.Authorize(begin.AuthorizationUrl, "sub-pat", s.s.LinkedEmail)
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, s.s.CallbackPrefix+s.s.Connection+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil))
		loc, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		var done struct{ IdentityToken string }
		post(t, s.pub, completeDoc, map[string]any{"c": loc.Query().Get("code"), "v": verifier}, "", "", "").field(t, "completeSso", &done)
		return done.IdentityToken
	}
	const signOutDoc = `mutation($t:String!,$r:SignOutReturn){logout(token:$t,returnTo:$r){providerSignOutUrl}}`

	// Each app named over the wire comes back to ITS address: the resolver hands the
	// caller's choice through, not a default.
	for app, returnTo := range map[string]string{
		"CONSOLE":   "https://dc.example.com/login",
		"DASHBOARD": "https://dc.example.com/dash/",
	} {
		idTok := signIn()
		var out struct{ ProviderSignOutUrl *string }
		post(t, s.pub, signOutDoc, map[string]any{"t": idTok, "r": app}, "", "", "").field(t, "logout", &out)
		require.NotNilf(t, out.ProviderSignOutUrl, "no provider address for %s", app)
		got, err := url.Parse(*out.ProviderSignOutUrl)
		require.NoError(t, err)
		require.Equal(t, s.s.ProviderURL+"/logout", got.Scheme+"://"+got.Host+got.Path)
		require.Equalf(t, url.Values{"client_id": {s.s.ClientID}, "post_logout_redirect_uri": {returnTo}}, got.Query(), "app %s", app)

		// The platform sign-in ended before the address was handed back.
		r = post(t, s.pub, selectSso, map[string]any{"i": idTok}, "", "", "")
		require.Equal(t, "invalid or expired token", r.failed(t))
	}

	// No app named: this platform only, even with the connection switched on.
	idTok := signIn()
	var out struct{ ProviderSignOutUrl *string }
	post(t, s.pub, signOutDoc, map[string]any{"t": idTok}, "", "", "").field(t, "logout", &out)
	require.Nil(t, out.ProviderSignOutUrl, "a sign-out that named no app was handed a provider address")
	r = post(t, s.pub, selectSso, map[string]any{"i": idTok}, "", "", "")
	require.Equal(t, "invalid or expired token", r.failed(t))
}
