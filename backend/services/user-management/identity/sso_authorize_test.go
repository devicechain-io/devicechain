// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

// Single sign-on on the MCP authorize page, driven through the page's real HTTP handler over
// the real manager, the real callback handler, and the stub provider. Every refusal is paired
// with a control showing the same step working until the rule applies.

const authzState = "client-state"

func (e *ssoEnv) authzForm(extra map[string]string) url.Values {
	v := url.Values{
		"response_type": {"code"}, "client_id": {sessClient}, "redirect_uri": {sessRedirect},
		"scope": {auth.ScopeReadOnly}, "state": {authzState},
		"code_challenge": {pkceChallenge(sessVerifier)}, "code_challenge_method": {"S256"},
	}
	for k, val := range extra {
		v.Set(k, val)
	}
	return v
}

func (e *ssoEnv) authzPost(form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, AuthorizePath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	AuthorizeHandler(e.m).ServeHTTP(rec, req)
	return rec
}

func (e *ssoEnv) authzGet(q url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, AuthorizePath+"?"+q.Encode(), nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	AuthorizeHandler(e.m).ServeHTTP(rec, req)
	return rec
}

var ssoGoLink = regexp.MustCompile(`<a href="([^"]+)"`)

// beginAuthz presses "Sign in with SSO" for email. It returns the provider URL the page moves
// the browser to and the binding cookie it set.
func (e *ssoEnv) beginAuthz(t *testing.T, email string) (authURL string, cookie *http.Cookie) {
	t.Helper()
	rec := e.authzPost(e.authzForm(map[string]string{"step": "sso", "email": email}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Empty(t, rec.Header().Get("Location"), "a 302 is blocked by the page's form-action")
	m := ssoGoLink.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2, rec.Body.String())
	authURL = html.UnescapeString(m[1])
	require.Contains(t, rec.Body.String(), `http-equiv="refresh"`)
	for _, c := range rec.Result().Cookies() {
		if c.Name == AuthorizeBrowserCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "the binding cookie was not set")
	return authURL, cookie
}

// idpRoundTrip plays the provider and the callback; it returns where the browser lands.
func (e *ssoEnv) idpRoundTrip(t *testing.T, authURL, sub, email string) *url.URL {
	t.Helper()
	state, code := e.op.authorize(authURL, sub, email)
	req := httptest.NewRequest(http.MethodGet, SsoCallbackPrefix+"acme-idp?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	u, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, ssoTestIssuerURL+AuthorizePath, u.Scheme+"://"+u.Host+u.Path, "the browser goes back to the authorize page")
	return u
}

// resume follows the landing back into the authorize page.
func (e *ssoEnv) resume(landing *url.URL, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return e.authzGet(landing.Query(), cookies...)
}

var consentToken = regexp.MustCompile(`name="identity_token" value="([^"]+)"`)

// consentOK asserts rec is the consent screen and returns the identity token it carries.
func consentOK(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	require.Empty(t, rec.Header().Get("Location"), "consent was skipped")
	require.Contains(t, rec.Body.String(), `name="action" value="allow"`)
	m := consentToken.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2, rec.Body.String())
	return html.UnescapeString(m[1])
}

func (e *ssoEnv) consentPost(idTok, tenant, action string) *httptest.ResponseRecorder {
	return e.authzPost(e.authzForm(map[string]string{
		"step": "consent", "action": action, "identity_token": idTok, "tenant": tenant,
	}))
}

// codeOf asserts rec is the redirect that carries an authorization code.
func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	u, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, authzState, u.Query().Get("state"))
	return u.Query().Get("code")
}

// mcpViaSso runs the page end to end for the linked person and returns the identity token the
// consent carried and the tokens the MCP client redeemed.
func (e *ssoEnv) mcpViaSso(t *testing.T) (string, *OAuthTokens) {
	t.Helper()
	authURL, cookie := e.beginAuthz(t, linkedEmail)
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	idTok := consentOK(t, e.resume(landing, cookie))
	code := codeOf(t, e.consentPost(idTok, "acme", "allow"))
	require.NotEmpty(t, code)
	toks, err := e.redeem(code)
	require.NoError(t, err)
	return idTok, toks
}

func TestAuthorizeSsoResumesToConsentWithOriginalParams(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)

	// The page offers the branch.
	page := e.authzGet(e.authzForm(nil))
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "Sign in with SSO")

	authURL, cookie := e.beginAuthz(t, linkedEmail)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	require.Equal(t, "/api/user-management"+AuthorizePath, cookie.Path, "scoped to the authorize endpoint as the browser reaches it")
	require.True(t, cookie.Secure)

	// None of the pending request is handed to the provider.
	for _, secret := range []string{sessRedirect, pkceChallenge(sessVerifier), authzState, sessClient} {
		require.NotContains(t, authURL, url.QueryEscape(secret), "the pending request travelled through the provider")
	}
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	require.Equal(t, []string{authorizeResumeParam}, keysOf(landing.Query()), "the way back carries the resume code and nothing else")

	rec := e.resume(landing, cookie)
	idTok := consentOK(t, rec)
	body := rec.Body.String()
	require.Contains(t, body, `name="redirect_uri" value="`+sessRedirect+`"`)
	require.Contains(t, body, `name="state" value="`+authzState+`"`)
	require.Contains(t, body, `name="code_challenge" value="`+pkceChallenge(sessVerifier)+`"`)
	require.Contains(t, body, "Signed in as "+linkedEmail)

	// The grant is the SSO session's: same sid as the identity token.
	code := codeOf(t, e.consentPost(idTok, "acme", "allow"))
	toks, err := e.redeem(code)
	require.NoError(t, err)
	sid, _ := e.sessionOf(t, idTok)
	rc, err := e.validator.ValidateRefresh(toks.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, sid, rc.SessionID, "the MCP grant must ride the SSO session")
	require.Equal(t, auth.ScopeReadOnly, rc.Scope)
	ac, err := e.validator.Validate(toks.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "acme", ac.Tenant)

	// The resume code is single use.
	again := e.resume(landing, cookie)
	require.Equal(t, http.StatusBadRequest, again.Code)
}

func TestConsentNeverAutoSkipped(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	authURL, cookie := e.beginAuthz(t, linkedEmail)
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	rec := e.resume(landing, cookie)
	// Silent provider sign-in (no password typed) still ends on a screen, never a code.
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Header().Get("Location"), "code=")
	idTok := consentOK(t, rec)
	// Denying is the client-visible refusal, and issues nothing.
	deny := e.consentPost(idTok, "acme", "deny")
	require.Equal(t, http.StatusFound, deny.Code)
	require.Equal(t, "access_denied", mustParse(t, deny.Header().Get("Location")).Query().Get("error"))
}

func TestEnforcedTenantMcpLoginViaSso(t *testing.T) {
	e := newSsoEnv(t)
	e.createMember(t, pwMember, "pw-1")
	e.linkedPat(t)

	// Control: the password branch reaches acme before enforcement.
	pwTok, _ := e.login(t, pwMember, "pw-1")
	require.NotEmpty(t, codeOf(t, e.consentPost(pwTok, "acme", "allow")))

	e.enforce(t, "acme", true)
	denied := e.consentPost(pwTok, "acme", "allow")
	require.Equal(t, http.StatusFound, denied.Code)
	require.Equal(t, "access_denied", mustParse(t, denied.Header().Get("Location")).Query().Get("error"))

	// An SSO-only tenant can still authorize MCP.
	_, toks := e.mcpViaSso(t)
	require.NotEmpty(t, toks.AccessToken)
}

func TestSsoSessionForOneTenantCannotAuthorizeAnother(t *testing.T) {
	e := newSsoEnv(t)
	e.addTenant(t, "other")
	// Pat is a member of both, but the connection serves only acme.
	e.ssoIdentity(t, linkedEmail, "acme-idp", "sub-pat", "acme", "other")

	authURL, cookie := e.beginAuthz(t, linkedEmail)
	idTok := consentOK(t, e.resume(e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail), cookie))

	other := e.consentPost(idTok, "other", "allow")
	require.Equal(t, http.StatusFound, other.Code)
	require.Equal(t, "access_denied", mustParse(t, other.Header().Get("Location")).Query().Get("error"))
	require.Empty(t, mustParse(t, other.Header().Get("Location")).Query().Get("code"))
	require.NotEmpty(t, codeOf(t, e.consentPost(idTok, "acme", "allow")), "control: the served tenant authorizes")
}

func TestAuthorizeResumeRefusesTamperingAndAnotherBrowser(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	authURL, cookie := e.beginAuthz(t, linkedEmail)
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)

	// Parameters added to the way back are refused outright, and nothing was consumed.
	for name, extra := range map[string]map[string]string{
		"redirect_uri": {"redirect_uri": "https://evil.example/cb"},
		"client_id":    {"client_id": "other"},
		"scope":        {"scope": "read-write"},
		"state":        {"state": "forged"},
		"resource":     {"resource": "https://evil.example"},
	} {
		q := landing.Query()
		for k, v := range extra {
			q.Set(k, v)
		}
		rec := e.authzGet(q, cookie)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
		require.Empty(t, rec.Header().Get("Location"), name)
		require.NotContains(t, rec.Body.String(), "identity_token", name)
	}
	// Nothing was consumed: the untampered way back, in the right browser, still resumes.
	consentOK(t, e.resume(landing, cookie))

	// Another browser (no cookie, or a different secret) cannot redeem the code...
	authURL, cookie = e.beginAuthz(t, linkedEmail)
	landing = e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	require.Equal(t, http.StatusBadRequest, e.resume(landing).Code)
	wrong, _ := verifierPair()
	require.Equal(t, http.StatusBadRequest, e.resume(landing, &http.Cookie{Name: AuthorizeBrowserCookie, Value: wrong}).Code)

	// ...and the code is spent by those attempts (single use), so even the right browser
	// now starts over -- and a fresh flow is the control that the happy path works.
	require.Equal(t, http.StatusBadRequest, e.resume(landing, cookie).Code)
	authURL2, cookie2 := e.beginAuthz(t, linkedEmail)
	consentOK(t, e.resume(e.idpRoundTrip(t, authURL2, "sub-pat", linkedEmail), cookie2))

	// A POST to the return, or a bare error class with parameters, is refused too.
	q := landing.Query()
	req := httptest.NewRequest(http.MethodPost, AuthorizePath, strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	AuthorizeHandler(e.m).ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, http.StatusBadRequest, e.authzGet(url.Values{"error": {"refused"}, "redirect_uri": {"x"}}).Code)
}

func TestAuthorizeResumeReResolvesClient(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	ctx := context.Background()

	// Control: an untouched record resumes.
	authURL, cookie := e.beginAuthz(t, linkedEmail)
	consentOK(t, e.resume(e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail), cookie))

	// A client disabled mid-flow ends in an error page, with no session started.
	authURL, cookie = e.beginAuthz(t, linkedEmail)
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	client, err := e.store.OAuthClientByClientId(ctx, sessClient)
	require.NoError(t, err)
	require.NoError(t, e.store.SetOAuthClientEnabled(ctx, client, false))
	before := len(e.sessionRecordsFor(t, linkedEmail))
	rec := e.resume(landing, cookie)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "not recognized")
	require.Empty(t, rec.Header().Get("Location"), "an unresolved client is never redirected to")
	require.Equal(t, before, len(e.sessionRecordsFor(t, linkedEmail)), "a refused resume started a session")
	require.NoError(t, e.store.SetOAuthClientEnabled(ctx, client, true))

	// A record rewritten to name an unregistered redirect is refused the same way.
	authURL, cookie = e.beginAuthz(t, linkedEmail)
	landing = e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	key := landing.Query().Get(authorizeResumeParam)
	entry, err := e.m.ssoCodesKV.Get(key)
	require.NoError(t, err)
	var sc ssoCode
	require.NoError(t, json.Unmarshal(entry.Value(), &sc))
	sc.Authorize.RedirectURI = "https://evil.example/cb"
	b, _ := json.Marshal(sc)
	_, err = e.m.ssoCodesKV.Put(key, b)
	require.NoError(t, err)
	rec = e.resume(landing, cookie)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Header().Get("Location"))

	// A record whose request no longer validates (here, a downgraded PKCE method) is refused
	// at the resume, before any session starts -- not left for the consent POST to catch.
	authURL, cookie = e.beginAuthz(t, linkedEmail)
	landing = e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	key = landing.Query().Get(authorizeResumeParam)
	entry, err = e.m.ssoCodesKV.Get(key)
	require.NoError(t, err)
	sc = ssoCode{}
	require.NoError(t, json.Unmarshal(entry.Value(), &sc))
	sc.Authorize.CodeChallengeMethod = "plain"
	b, _ = json.Marshal(sc)
	_, err = e.m.ssoCodesKV.Put(key, b)
	require.NoError(t, err)
	before = len(e.sessionRecordsFor(t, linkedEmail))
	rec = e.resume(landing, cookie)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get("Location"))
	require.NotContains(t, rec.Body.String(), "identity_token")
	require.Equal(t, before, len(e.sessionRecordsFor(t, linkedEmail)), "a request that no longer validates started a session")
}

func TestAuthorizeStateNotCompletableViaCompleteSso(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	ctx := context.Background()

	// An authorize-kind code cannot be redeemed as a login...
	authURL, cookie := e.beginAuthz(t, linkedEmail)
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	_, err := e.m.CompleteSso(ctx, landing.Query().Get(authorizeResumeParam), cookie.Value)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)

	// ...nor a login-kind code resumed as an authorize request (control: it completes as a login).
	l, verifier, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = e.m.ResumeAuthorizeSso(ctx, l.Code, verifier)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)
	l, verifier, _, _ = e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = e.m.CompleteSso(ctx, l.Code, verifier)
	require.NoError(t, err)
}

func TestAuthorizeSsoUnavailableAndRefusedPaths(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)

	// An address no connection serves gets the page back with a message, and no cookie.
	for _, email := range []string{"nobody@elsewhere.example", "not-an-email"} {
		rec := e.authzPost(e.authzForm(map[string]string{"step": "sso", "email": email}))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "not available for that address")
		require.Empty(t, rec.Result().Cookies())
	}

	// A request a browser marks cross-site cannot start a sign-in.
	req := httptest.NewRequest(http.MethodPost, AuthorizePath, strings.NewReader(e.authzForm(map[string]string{"step": "sso", "email": linkedEmail}).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	AuthorizeHandler(e.m).ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Result().Cookies())

	// A request that is not valid is refused before anything is parked (the usual checks).
	bad := e.authzForm(map[string]string{"step": "sso", "email": linkedEmail, "redirect_uri": "https://evil.example/cb"})
	rec = e.authzPost(bad)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Result().Cookies())

	// An identity the provider asserts but nobody linked is refused with a named page.
	authURL, cookie := e.beginAuthz(t, linkedEmail)
	landing := e.idpRoundTrip(t, authURL, "sub-stranger", "stranger@"+ssoTestDomain)
	require.Equal(t, "link_required", landing.Query().Get("error"))
	page := e.resume(landing, cookie)
	require.Equal(t, http.StatusForbidden, page.Code)
	require.Contains(t, page.Body.String(), "not linked")
	require.NotContains(t, page.Body.String(), "identity_token")
}

func TestRevokeSessionsEndsTheMcpRefreshViaSso(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	_, toks := e.mcpViaSso(t)
	renewed, err := e.refreshOAuth(toks.RefreshToken)
	require.NoError(t, err, "control: the grant refreshes")

	n, err := e.m.RevokeSessions(context.Background(), linkedEmail)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	_, err = e.refreshOAuth(renewed.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
}

func TestCeilingAppliesToTheMcpRefreshChainViaSso(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	idTok, toks := e.mcpViaSso(t)
	_, rec := e.sessionOf(t, idTok)

	e.atAge(rec, 11*time.Hour)
	toks, err := e.refreshOAuth(toks.RefreshToken)
	require.NoError(t, err, "control: inside the ceiling")
	e.atAge(rec, 12*time.Hour+time.Second)
	_, err = e.refreshOAuth(toks.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
}

var hiddenField = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

// The consent page is what the browser posts back, so the request must survive the round trip
// through the server's record INTO that page: here the page's own hidden fields are submitted,
// as a browser would, rather than the test's copy of the original request.
func TestAuthorizeSsoConsentCarriesTheWholeRequest(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	const resource = "https://mcp.example.com"
	rec := e.authzPost(e.authzForm(map[string]string{"step": "sso", "email": linkedEmail, "resource": resource}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == AuthorizeBrowserCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	m := ssoGoLink.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2)
	page := e.resume(e.idpRoundTrip(t, html.UnescapeString(m[1]), "sub-pat", linkedEmail), cookie)
	consentOK(t, page)

	form := url.Values{}
	for _, f := range hiddenField.FindAllStringSubmatch(page.Body.String(), -1) {
		form.Set(html.UnescapeString(f[1]), html.UnescapeString(f[2]))
	}
	want := e.authzForm(map[string]string{"resource": resource})
	for _, k := range []string{"response_type", "client_id", "redirect_uri", "scope", "state", "code_challenge", "code_challenge_method", "resource"} {
		require.Equal(t, want.Get(k), form.Get(k), k)
	}
	form.Set("action", "allow")
	form.Set("tenant", "acme")
	code := codeOf(t, e.authzPost(form))
	toks, err := e.redeem(code)
	require.NoError(t, err, "the PKCE challenge the client began with must still be the one it redeems with")
	ac, err := e.validator.Validate(toks.AccessToken)
	require.NoError(t, err)
	require.Contains(t, []string(ac.Audience), resource, "the RFC 8707 resource indicator was lost")
}

// hostileBeginSvc is the real manager with a begin whose provider URL carries markup, so the
// hand-off page's escaping is tested on its own rather than through a well-behaved URL.
type hostileBeginSvc struct {
	AuthorizeService
	url string
}

func (h hostileBeginSvc) BeginAuthorizeSso(context.Context, string, AuthorizeParams, string) (*AuthorizeSsoBegin, error) {
	return &AuthorizeSsoBegin{AuthorizationURL: h.url, BrowserSecret: strings.Repeat("s", 43), CookiePath: AuthorizePath, Secure: true}, nil
}

var ssoGoMeta = regexp.MustCompile(`<meta http-equiv="refresh" content="([^"]*)">`)

func TestAuthorizeSsoHandOffEscapesTheProviderURL(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	const hostile = `https://idp.example/auth?a=1&b="><script>alert(1)</script>`
	req := httptest.NewRequest(http.MethodPost, AuthorizePath,
		strings.NewReader(e.authzForm(map[string]string{"step": "sso", "email": linkedEmail}).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	AuthorizeHandler(hostileBeginSvc{AuthorizeService: e.m, url: hostile}).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.NotContains(t, body, "<script>", "the provider URL broke out of its attribute")
	m := ssoGoMeta.FindStringSubmatch(body)
	require.Len(t, m, 2, body)
	require.Equal(t, "0;url="+hostile, html.UnescapeString(m[1]), "the refresh must carry the URL exactly, escaped")
	require.Contains(t, m[1], "&amp;")
}

func keysOf(v url.Values) []string {
	var ks []string
	for k := range v {
		ks = append(ks, k)
	}
	return ks
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}
