// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The authorization server's resource allowlist (rule 1) and the client_credentials scope
// deprecation (rule 3), driven through the REAL handlers over the real manager: the browser
// flow (GET -> login POST -> consent POST), the token endpoint, refresh, client_credentials and
// the single sign-on resume. Every enforce-mode refusal is paired with the observe-mode
// request being SERVED exactly as before and counted exactly once, and with a listed request
// passing in both modes.

const (
	rBase     = "https://iot.example.com/api"
	rMCP      = rBase + "/mcp"
	rArea     = rBase + "/device-management"
	rExt      = "https://reports.example.org"
	rEvil     = "https://evil.example.net"
	rPassword = "pw-1"
	rEmail    = "pw@example.com"

	checksMetric   = "devicechain_usermanagement_oauth_resource_checks_total"
	unscopedMetric = "devicechain_usermanagement_oauth_unscoped_client_credentials_total"
	warnResource   = "OAuth resource is not one this instance serves"
	warnUnscoped   = "without a scope"
)

func rCfg(mode string) config.TokenAudienceConfiguration {
	return config.TokenAudienceConfiguration{
		Enforcement:       mode,
		AreaBaseUrl:       rBase,
		McpResources:      []string{rMCP},
		ExternalResources: []string{rExt},
	}
}

// asEnv is the browser-and-token-endpoint harness: an SSO-capable manager (a linked SSO
// person and a password member), real JetStream stores, the real token handler, and the
// resource counters on a registry of their own so a test reads back exactly what was recorded.
type asEnv struct {
	*ssoEnv
	reg    *prometheus.Registry
	met    *OAuthResourceMetrics
	tokens http.Handler
}

func newASEnv(t *testing.T, mode string) *asEnv {
	t.Helper()
	base := newSsoEnv(t)
	base.linkedPat(t)
	base.createMember(t, rEmail, rPassword)
	e := &asEnv{ssoEnv: base, reg: prometheus.NewRegistry()}
	e.met = NewOAuthResourceMetrics(e.reg, "usermanagement")
	e.setConfig(t, rCfg(mode))
	e.tokens = TokenHandler(e.m.AuthenticateClient, e.m.RedeemAuthorizationCode, e.m.RefreshOAuth, e.m.ClientCredentials)
	return e
}

// setConfig replaces the allowlist, as a config roll does, keeping the same counters.
func (e *asEnv) setConfig(t *testing.T, cfg config.TokenAudienceConfiguration) {
	t.Helper()
	require.NoError(t, e.m.SetTokenAudience(cfg, e.met))
}

func gatherSeries(t *testing.T, reg *prometheus.Registry, name string) map[string]float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			var parts []string
			for _, l := range m.GetLabel() {
				parts = append(parts, l.GetValue())
			}
			if v := m.GetCounter().GetValue(); v != 0 {
				out[strings.Join(parts, "/")] = v
			}
		}
	}
	return out
}

// counts is every checks series that has moved, as "grant/resource/verdict" -> value.
func (e *asEnv) counts(t *testing.T) map[string]float64 {
	t.Helper()
	return gatherSeries(t, e.reg, checksMetric)
}

func (e *asEnv) unscoped(t *testing.T) map[string]float64 {
	t.Helper()
	return gatherSeries(t, e.reg, unscopedMetric)
}

func (e *asEnv) query(resources ...string) url.Values {
	v := e.authzForm(nil)
	for _, r := range resources {
		v.Add("resource", r)
	}
	return v
}

var hiddenInput = hiddenField

// hiddenForm reads back the hidden fields of the FIRST form of a rendered page, as the browser
// would post them (a browser submits one form, never the page's fields merged).
func hiddenForm(body string) url.Values { return hiddenFormN(body, 0) }

// hiddenFormN is hiddenForm for the nth form (the login page's second one is "Sign in with SSO").
func hiddenFormN(body string, n int) url.Values {
	parts := strings.Split(body, "<form")
	v := url.Values{}
	if n+1 >= len(parts) {
		return v
	}
	form := parts[n+1]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	for _, m := range hiddenInput.FindAllStringSubmatch(form, -1) {
		v.Add(m[1], html.UnescapeString(m[2]))
	}
	return v
}

// flow is the whole browser exchange: entry GET, login POST, consent POST. Each response is
// kept; a later one is nil when an earlier one ended the flow.
type flow struct {
	entry, login, consent *httptest.ResponseRecorder
}

// browse runs the flow for the password member, posting the pages' OWN hidden fields back as
// a browser does. extra is added to every POST (a stale page's empty `resource=`, say).
func (e *asEnv) browse(t *testing.T, q url.Values, extra url.Values) flow {
	t.Helper()
	f := flow{entry: e.authzGet(q)}
	if f.entry.Code != http.StatusOK {
		return f
	}
	step := hiddenForm(f.entry.Body.String())
	step.Set("step", "login")
	step.Set("email", rEmail)
	step.Set("password", rPassword)
	for k, vs := range extra {
		step[k] = vs
	}
	f.login = e.authzPost(step)
	if f.login.Code != http.StatusOK {
		return f
	}
	ct := hiddenForm(f.login.Body.String())
	ct.Set("step", "consent")
	ct.Set("tenant", "acme")
	ct.Set("action", "allow")
	for k, vs := range extra {
		ct[k] = vs
	}
	f.consent = e.authzPost(ct)
	return f
}

// code returns the authorization code the flow ended with, failing the test if it ended any
// other way.
func (f flow) code(t *testing.T) string {
	t.Helper()
	require.NotNil(t, f.consent, "the flow ended before consent")
	require.Equal(t, http.StatusFound, f.consent.Code, f.consent.Body.String())
	u, err := url.Parse(f.consent.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, authzState, u.Query().Get("state"))
	require.Empty(t, u.Query().Get("error"), f.consent.Header().Get("Location"))
	require.NotEmpty(t, u.Query().Get("code"))
	return u.Query().Get("code")
}

func (e *asEnv) tokenPost(form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.77:4000"
	rec := httptest.NewRecorder()
	e.tokens.ServeHTTP(rec, req)
	return rec
}

func (e *asEnv) redeemHTTP(code string, resources ...string) *httptest.ResponseRecorder {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {sessVerifier},
		"redirect_uri": {sessRedirect}, "client_id": {sessClient},
	}
	for _, r := range resources {
		form.Add("resource", r)
	}
	return e.tokenPost(form)
}

func (e *asEnv) refreshHTTP(refresh string, resources ...string) *httptest.ResponseRecorder {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {sessClient}}
	for _, r := range resources {
		form.Add("resource", r)
	}
	return e.tokenPost(form)
}

// session runs the whole authorize -> token flow for the given authorize-time resources and
// returns the tokens.
func (e *asEnv) session(t *testing.T, resources ...string) tokenBody {
	t.Helper()
	code := e.browse(t, e.query(resources...), nil).code(t)
	rec := e.redeemHTTP(code)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode(t, rec)
}

func (e *asEnv) audOf(t *testing.T, access string) []string {
	t.Helper()
	c, err := e.validator.Validate(access)
	require.NoError(t, err)
	return []string(c.Audience)
}

func (e *asEnv) refreshAudOf(t *testing.T, refresh string) []string {
	t.Helper()
	c, err := e.validator.ValidateRefresh(refresh)
	require.NoError(t, err)
	return []string(c.Audience)
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	require.Equal(t, code, decode(t, rec).Error, rec.Body.String())
}

type resourceCase struct {
	name string
	res  []string
	// class is how the authorization request is counted; listed is whether enforce passes it.
	class  string
	listed bool
	// first is the value the hidden field and the token's aud carry (the first value).
	first string
}

var resourceCases = []resourceCase{
	{"absent", nil, "none", true, ""},
	{"an empty value is absent", []string{""}, "none", true, ""},
	// The ambiguous shape: Resource keeps the (empty) first value, so the hidden field would drop
	// the second and the token would be audience-less while the request asked for one. Not absent.
	{"an empty value then a listed one", []string{"", rMCP}, "malformed", false, ""},
	{"an empty value then a foreign one", []string{"", rEvil}, "malformed", false, ""},
	{"an empty value, a listed one and another", []string{"", rMCP, rArea}, "multiple", false, ""},
	{"a listed one then an empty value", []string{rMCP, ""}, "mcp", true, rMCP},
	{"mcp", []string{rMCP}, "mcp", true, rMCP},
	{"an area", []string{rArea}, "area", true, rArea},
	{"an external resource", []string{rExt}, "external", true, rExt},
	{"unlisted", []string{rEvil}, "unlisted", false, rEvil},
	{"a trailing-slash spelling of a listed one", []string{rMCP + "/"}, "unlisted", false, rMCP + "/"},
	{"an upper-case host", []string{"https://IOT.example.com/api/mcp"}, "unlisted", false, "https://IOT.example.com/api/mcp"},
	{"not a URI", []string{"not-a-uri"}, "malformed", false, "not-a-uri"},
	{"http, not loopback", []string{"http://iot.example.com/api/mcp"}, "malformed", false, "http://iot.example.com/api/mcp"},
	{"a fragment", []string{rMCP + "#x"}, "malformed", false, rMCP + "#x"},
	{"oversized", []string{"https://a.example/" + strings.Repeat("a", maxResourceLen)}, "malformed", false,
		"https://a.example/" + strings.Repeat("a", maxResourceLen)},
	{"a listed one and a foreign one", []string{rMCP, rEvil}, "multiple", false, rMCP},
	{"two listed ones", []string{rMCP, rArea}, "multiple", false, rMCP},
}

// ---------------------------------------------------------------------------------------------
// /authorize
// ---------------------------------------------------------------------------------------------

func TestAuthorizeEnforcesTheResourceAllowlist(t *testing.T) {
	for _, tc := range resourceCases {
		t.Run(tc.name, func(t *testing.T) {
			e := newASEnv(t, "enforce")
			rec := e.authzGet(e.query(tc.res...))
			if tc.listed {
				// Counterweight: a listed (or absent) resource proceeds to the login page.
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Empty(t, rec.Header().Get("Location"))
				require.Contains(t, rec.Body.String(), "Sign in to DeviceChain")
				require.Equal(t, map[string]float64{"authorization_code/" + tc.class + "/accepted": 1}, e.counts(t))
				return
			}
			// Negative control: an unlisted one is refused on the (trusted) redirect URI, with the
			// client's state, and never reaches a login page.
			require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
			u, err := url.Parse(rec.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, sessRedirect, u.Scheme+"://"+u.Host+u.Path)
			require.Equal(t, "invalid_target", u.Query().Get("error"))
			require.Equal(t, authzState, u.Query().Get("state"))
			require.Empty(t, u.Query().Get("code"))
			require.Equal(t, map[string]float64{"authorization_code/" + tc.class + "/rejected": 1}, e.counts(t))
		})
	}
}

// Observe mode is the v1.2 default and must change NO served behaviour: every request the
// enforce-mode test refuses is answered with the same login page a listed one gets, carries
// its resource through the hidden field verbatim, and is counted would_reject exactly once.
func TestAuthorizeObserveServesEverythingAndCountsOnce(t *testing.T) {
	for _, tc := range resourceCases {
		t.Run(tc.name, func(t *testing.T) {
			e := newASEnv(t, "observe")
			rec := e.authzGet(e.query(tc.res...))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Empty(t, rec.Header().Get("Location"))
			require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			require.Contains(t, rec.Body.String(), "Sign in to DeviceChain")
			if tc.first != "" {
				require.Contains(t, rec.Body.String(),
					`name="resource" value="`+html.EscapeString(tc.first)+`"`, "the first value rides the form as before")
			}

			verdict := "accepted"
			if !tc.listed {
				verdict = "would_reject"
			}
			want := map[string]float64{"authorization_code/" + tc.class + "/" + verdict: 1}
			require.Equal(t, want, e.counts(t))

			// The whole flow completes and counts nothing more: not the login POST, not the
			// consent POST, not the mint.
			f := e.browse(t, e.query(tc.res...), nil)
			code := f.code(t)
			want["authorization_code/"+tc.class+"/"+verdict]++ // the second entry request of this test
			require.Equal(t, want, e.counts(t), "only the entry request counts a flow")

			// The token carries exactly the audience it carried before the rule existed: the first
			// value, raw, or none.
			rec2 := e.redeemHTTP(code)
			require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
			toks := decode(t, rec2)
			wantAud := []string(nil)
			if tc.first != "" {
				wantAud = []string{tc.first}
			}
			require.Equal(t, wantAud, e.audOf(t, toks.AccessToken))
			require.Equal(t, wantAud, e.refreshAudOf(t, toks.RefreshToken))
		})
	}
}

// The browser flow in ENFORCE mode for a request that carries no resource (Grafana's), end to
// end, in both shapes of page: today's, which renders no resource field, and an older one that
// posts back an empty `resource=` at every step. Neither may be refused invalid_target.
func TestAResourcelessFlowIsNeverRefused(t *testing.T) {
	for _, mode := range []string{"enforce", "observe"} {
		for name, extra := range map[string]url.Values{
			"the page's own fields":      nil,
			"an empty resource posted":   {"resource": {""}},
			"two empty resources posted": {"resource": {"", ""}},
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				e := newASEnv(t, mode)
				f := e.browse(t, e.query(), extra)
				require.NotContains(t, f.entry.Body.String(), `name="resource"`, "the hidden field is rendered only when set")
				require.NotContains(t, f.login.Body.String(), `name="resource"`)
				code := f.code(t)

				rec := e.redeemHTTP(code)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				toks := decode(t, rec)
				require.Empty(t, e.audOf(t, toks.AccessToken), "a token minted without a resource has no audience")
				require.Equal(t, map[string]float64{"authorization_code/none/accepted": 1}, e.counts(t))
			})
		}
	}
}

// A page that DOES carry a resource renders the field, and only then.
func TestTheResourceFieldIsRenderedOnlyWhenSet(t *testing.T) {
	e := newASEnv(t, "enforce")
	with := e.authzGet(e.query(rMCP))
	require.Contains(t, with.Body.String(), `name="resource" value="`+rMCP+`"`)
	without := e.authzGet(e.query())
	require.NotContains(t, without.Body.String(), `name="resource"`)
}

// A POST in the middle of the flow that tampers the hidden field to an unlisted one is
// refused in enforce mode and NOT counted: only the entry request is, so a client cannot move
// the counter by replaying steps.
func TestAMidFlowTamperIsRefusedAndNotCounted(t *testing.T) {
	e := newASEnv(t, "enforce")
	f := e.browse(t, e.query(rMCP), nil)
	f.code(t) // control: the untampered flow issues a code
	before := e.counts(t)

	entry := e.authzGet(e.query(rMCP))
	step := hiddenForm(entry.Body.String())
	step.Set("step", "login")
	step.Set("email", rEmail)
	step.Set("password", rPassword)
	login := e.authzPost(step)
	ct := hiddenForm(login.Body.String())
	ct.Set("step", "consent")
	ct.Set("tenant", "acme")
	ct.Set("action", "allow")
	ct.Set("resource", rEvil)
	rec := e.authzPost(ct)
	require.Equal(t, http.StatusFound, rec.Code)
	u, _ := url.Parse(rec.Header().Get("Location"))
	require.Equal(t, "invalid_target", u.Query().Get("error"))
	require.Empty(t, u.Query().Get("code"))

	after := e.counts(t)
	before["authorization_code/mcp/accepted"]++ // the one extra entry request above
	require.Equal(t, before, after, "a tampered POST counted")
}

// The handler counts on the ENTRY request only. This drives the real handler over a service
// that records what it was asked to count.
func TestOnlyTheEntryRequestCountsTheFlow(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient(), loginResult: &IdentityAuth{IdentityToken: "tok"}, email: "pat@example.com", code: "c"}
	q := validParams()
	q.Set("resource", "https://mcp.example.com")

	require.Equal(t, http.StatusOK, getAuthorize(svc, q).Code)
	require.Len(t, svc.recorded, 1, "the entry GET counts")

	post := url.Values{}
	for k, v := range q {
		post[k] = v
	}
	post.Set("step", "login")
	post.Set("email", "pat@example.com")
	post.Set("password", "pw")
	require.Equal(t, http.StatusOK, postAuthorize(svc, post).Code)
	post.Set("step", "consent")
	post.Set("action", "allow")
	post.Set("identity_token", "tok")
	post.Set("tenant", "acme")
	require.Equal(t, http.StatusFound, postAuthorize(svc, post).Code)
	require.Len(t, svc.recorded, 1, "the login and consent POSTs must not count the same flow again")

	// A POST that names no step is an entry request too.
	noStep := url.Values{}
	for k, v := range q {
		noStep[k] = v
	}
	require.Equal(t, http.StatusOK, postAuthorize(svc, noStep).Code)
	require.Len(t, svc.recorded, 2)
}

// ---------------------------------------------------------------------------------------------
// the mint re-assert
// ---------------------------------------------------------------------------------------------

func (e *asEnv) mintParams(resource string, count int) AuthorizeParams {
	return AuthorizeParams{
		ResponseType: "code", ClientID: sessClient, RedirectURI: sessRedirect, Scope: auth.ScopeReadOnly,
		CodeChallenge: pkceChallenge(sessVerifier), CodeChallengeMethod: "S256",
		Resource: resource, ResourceCount: count,
	}
}

func (e *asEnv) storedCodes(t *testing.T) []string {
	t.Helper()
	keys, err := e.m.codesKV.Keys()
	if err == nats.ErrNoKeysFound {
		return nil
	}
	require.NoError(t, err)
	return keys
}

func (e *asEnv) directIssue(t *testing.T, p AuthorizeParams) (string, error) {
	t.Helper()
	ctx := context.Background()
	ia, err := e.m.Login(ctx, rEmail, rPassword)
	require.NoError(t, err)
	subject, err := e.m.IdentitySubject(ia.IdentityToken)
	require.NoError(t, err)
	client, err := e.m.ResolveAuthorizeClient(ctx, sessClient, sessRedirect)
	require.NoError(t, err)
	return e.m.IssueAuthorizationCode(ctx, client, p, subject, "acme")
}

func TestTheMintReassertsTheWholeResourceCheck(t *testing.T) {
	refused := map[string]AuthorizeParams{
		"an unlisted resource":                    {Resource: rEvil, ResourceCount: 1},
		"a second resource behind a listed first": {Resource: rMCP, ResourceCount: 2},
		"a malformed resource":                    {Resource: "not-a-uri", ResourceCount: 1},
	}
	for name, delta := range refused {
		t.Run("enforce/"+name, func(t *testing.T) {
			e := newASEnv(t, "enforce")
			p := e.mintParams(delta.Resource, delta.ResourceCount)
			_, err := e.directIssue(t, p)
			var re *authorizeRedirectError
			require.ErrorAs(t, err, &re)
			require.Equal(t, "invalid_target", re.Code)
			require.Empty(t, e.storedCodes(t), "a code was stored for a refused resource")
			require.Empty(t, e.counts(t), "the mint records nothing; the entry request counts the flow")

			// Counterweight: the same call with a listed single resource mints.
			code, err := e.directIssue(t, e.mintParams(rMCP, 1))
			require.NoError(t, err)
			require.Len(t, e.storedCodes(t), 1)
			require.NotEmpty(t, code)
		})
		t.Run("observe/"+name, func(t *testing.T) {
			e := newASEnv(t, "observe")
			_, err := e.directIssue(t, e.mintParams(delta.Resource, delta.ResourceCount))
			require.NoError(t, err, "observe mode never refuses")
			require.Len(t, e.storedCodes(t), 1)
			require.Empty(t, e.counts(t))
		})
	}
}

// A parked or hand-built record that predates the count (a resource with no count) is one
// resource, not zero: the mint cannot be walked past by leaving the count out.
func TestAResourceWithNoCountIsOneResource(t *testing.T) {
	e := newASEnv(t, "enforce")
	_, err := e.directIssue(t, e.mintParams(rEvil, 0))
	var re *authorizeRedirectError
	require.ErrorAs(t, err, &re)
	_, err = e.directIssue(t, e.mintParams(rMCP, 0))
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------------------------
// single sign-on
// ---------------------------------------------------------------------------------------------

// ssoFlow runs the SSO branch from the entry request to the consent page, posting the pages'
// own hidden fields. It returns the entry response, the browser's cookie and the landing URL.
func (e *asEnv) ssoBegin(t *testing.T, q url.Values) (entry *httptest.ResponseRecorder, authURL string, cookie *http.Cookie) {
	t.Helper()
	entry = e.authzGet(q)
	require.Equal(t, http.StatusOK, entry.Code, entry.Body.String())
	form := hiddenFormN(entry.Body.String(), 1)
	form.Set("step", "sso")
	form.Set("email", linkedEmail)
	rec := e.authzPost(form)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := ssoGoLink.FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2, rec.Body.String())
	authURL = html.UnescapeString(m[1])
	for _, c := range rec.Result().Cookies() {
		if c.Name == AuthorizeBrowserCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	return entry, authURL, cookie
}

func TestSsoBeginAndResumeCountTheFlowOnce(t *testing.T) {
	e := newASEnv(t, "observe")
	_, authURL, cookie := e.ssoBegin(t, e.query(rEvil))
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	page := e.resume(landing, cookie)
	idTok := consentOK(t, page)
	// The resume re-validated against the allowlist without counting it.
	require.Equal(t, map[string]float64{"authorization_code/unlisted/would_reject": 1}, e.counts(t))

	form := hiddenForm(page.Body.String())
	form.Set("step", "consent")
	form.Set("tenant", "acme")
	form.Set("action", "allow")
	form.Set("identity_token", idTok)
	rec := e.authzPost(form)
	code := codeOf(t, rec)
	require.Equal(t, http.StatusOK, e.redeemHTTP(code).Code)
	require.Equal(t, map[string]float64{"authorization_code/unlisted/would_reject": 1}, e.counts(t),
		"begin, resume, consent and redemption together count the flow once")
}

func TestSsoResumeRedirectsInvalidTargetWhenTheAllowlistMovedUnderIt(t *testing.T) {
	e := newASEnv(t, "enforce")
	_, authURL, cookie := e.ssoBegin(t, e.query(rMCP))
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	before := len(e.sessionRecordsFor(t, linkedEmail))

	// A config roll de-lists the resource while the person is at their provider.
	moved := rCfg("enforce")
	moved.McpResources = nil
	e.setConfig(t, moved)

	rec := e.resume(landing, cookie)
	require.Equal(t, http.StatusFound, rec.Code, "invalid_target is redirected to the client, not rendered as a 403 page: %s", rec.Body.String())
	u, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, sessRedirect, u.Scheme+"://"+u.Host+u.Path)
	require.Equal(t, "invalid_target", u.Query().Get("error"))
	require.Equal(t, authzState, u.Query().Get("state"), "the parked request's state")
	require.NotContains(t, rec.Body.String(), "identity_token")
	require.Equal(t, before, len(e.sessionRecordsFor(t, linkedEmail)), "a refused resume started a session")
	require.Equal(t, map[string]float64{"authorization_code/mcp/accepted": 1}, e.counts(t), "the resume counted nothing")
}

func TestSsoResumeWithAnUnmovedAllowlistStillReachesConsent(t *testing.T) {
	e := newASEnv(t, "enforce")
	_, authURL, cookie := e.ssoBegin(t, e.query(rMCP))
	landing := e.idpRoundTrip(t, authURL, "sub-pat", linkedEmail)
	consentOK(t, e.resume(landing, cookie))
}

// ---------------------------------------------------------------------------------------------
// redemption
// ---------------------------------------------------------------------------------------------

func TestRedemptionRechecksTheCurrentAllowlist(t *testing.T) {
	// A code issued by an observing replica (or before the resource was de-listed) ...
	e := newASEnv(t, "observe")
	unlisted := e.browse(t, e.query(rEvil), nil).code(t)
	listed := e.browse(t, e.query(rMCP), nil).code(t)
	issued := e.counts(t)
	// ... redeemed by an enforcing one.
	e.setConfig(t, rCfg("enforce"))

	rec := e.redeemHTTP(unlisted)
	errorOf(t, rec, http.StatusBadRequest, "invalid_grant")
	want := map[string]float64{}
	for k, v := range issued {
		want[k] = v
	}
	want["authorization_code/unlisted/rejected"] = 1
	require.Equal(t, want, e.counts(t), "only a refusal is recorded at redemption")
	// The code was claimed: it cannot be retried, even once the resource is listed.
	withEvil := rCfg("enforce")
	withEvil.ExternalResources = append(withEvil.ExternalResources, rEvil)
	e.setConfig(t, withEvil)
	errorOf(t, e.redeemHTTP(unlisted), http.StatusBadRequest, "invalid_grant")

	// Counterweight: a code for a listed resource redeems normally and counts nothing more.
	rec = e.redeemHTTP(listed)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{rMCP}, e.audOf(t, decode(t, rec).AccessToken))
	require.Equal(t, want, e.counts(t), "a passing redemption is not counted twice")
}

func TestRedemptionInObserveModeIsUnchanged(t *testing.T) {
	e := newASEnv(t, "observe")
	code := e.browse(t, e.query(rEvil), nil).code(t)
	before := e.counts(t)
	rec := e.redeemHTTP(code)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{rEvil}, e.audOf(t, decode(t, rec).AccessToken))
	require.Equal(t, before, e.counts(t))
}

// ---------------------------------------------------------------------------------------------
// `resource` on the token request for a code or a refresh token
// ---------------------------------------------------------------------------------------------

func TestTokenRequestResourceIsCheckedAgainstTheGrantNeverAdded(t *testing.T) {
	type tc struct {
		name      string
		bound     []string // authorize-time resource
		requested []string // token-request resource
		ok        bool
		class     string
	}
	cases := []tc{
		{"absent", []string{rMCP}, nil, true, ""},
		{"an empty value", []string{rMCP}, []string{""}, true, ""},
		{"equal to the binding", []string{rMCP}, []string{rMCP}, true, ""},
		{"a different listed resource", []string{rMCP}, []string{rArea}, false, "area"},
		{"an unlisted resource", []string{rMCP}, []string{rEvil}, false, "unlisted"},
		{"two values, one equal", []string{rMCP}, []string{rMCP, rMCP}, false, "multiple"},
		{"a value on an unbound grant", nil, []string{rMCP}, false, "mcp"},
	}
	for _, c := range cases {
		t.Run("code/enforce/"+c.name, func(t *testing.T) {
			e := newASEnv(t, "enforce")
			code := e.browse(t, e.query(c.bound...), nil).code(t)
			before := e.counts(t)
			rec := e.redeemHTTP(code, c.requested...)
			if c.ok {
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, c.bound, e.audOf(t, decode(t, rec).AccessToken))
				require.Equal(t, before, e.counts(t))
				return
			}
			errorOf(t, rec, http.StatusBadRequest, "invalid_target")
			want := map[string]float64{}
			for k, v := range before {
				want[k] = v
			}
			want["authorization_code/"+c.class+"/rejected"] = 1
			require.Equal(t, want, e.counts(t))
		})
		t.Run("code/observe/"+c.name, func(t *testing.T) {
			e := newASEnv(t, "observe")
			code := e.browse(t, e.query(c.bound...), nil).code(t)
			before := e.counts(t)
			rec := e.redeemHTTP(code, c.requested...)
			// Observe serves the request as before, ignoring the parameter, and never adds it.
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, c.bound, e.audOf(t, decode(t, rec).AccessToken), "the token endpoint added an audience")
			want := map[string]float64{}
			for k, v := range before {
				want[k] = v
			}
			if !c.ok {
				want["authorization_code/"+c.class+"/would_reject"] = 1
			}
			require.Equal(t, want, e.counts(t))
		})
		t.Run("refresh/enforce/"+c.name, func(t *testing.T) {
			e := newASEnv(t, "enforce")
			toks := e.session(t, c.bound...)
			before := e.counts(t)
			rec := e.refreshHTTP(toks.RefreshToken, c.requested...)
			if c.ok {
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, c.bound, e.audOf(t, decode(t, rec).AccessToken))
				return
			}
			errorOf(t, rec, http.StatusBadRequest, "invalid_target")
			want := map[string]float64{}
			for k, v := range before {
				want[k] = v
			}
			want["refresh_token/"+c.class+"/rejected"] = 1
			require.Equal(t, want, e.counts(t))
			// The grant is intact: a mismatched request does not burn it.
			again := e.refreshHTTP(toks.RefreshToken)
			require.Equal(t, http.StatusOK, again.Code, "an invalid_target burned a healthy refresh token: %s", again.Body.String())
		})
		t.Run("refresh/observe/"+c.name, func(t *testing.T) {
			e := newASEnv(t, "observe")
			toks := e.session(t, c.bound...)
			rec := e.refreshHTTP(toks.RefreshToken, c.requested...)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, c.bound, e.audOf(t, decode(t, rec).AccessToken))
			if !c.ok {
				require.EqualValues(t, 1, e.counts(t)["refresh_token/"+c.class+"/would_reject"])
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// refresh
// ---------------------------------------------------------------------------------------------

func (e *asEnv) refreshEntryExists(t *testing.T, refresh string) bool {
	t.Helper()
	c, err := e.validator.ValidateRefresh(refresh)
	require.NoError(t, err)
	_, err = e.m.refreshKV.Get(c.ID)
	if err == nats.ErrKeyNotFound {
		return false
	}
	require.NoError(t, err)
	return true
}

// The refresh token's bound audience is re-checked BEFORE the token is claimed, and a refusal
// burns it. The assertion is on the STORE; then the audience is allowlisted and the SAME token
// presented again, which is what tells "burned" from "refused this once" (a re-check that
// refused but left the token redeemable would pass the first half and fail the second).
func TestRefreshRechecksTheBoundAudienceAndBurnsTheToken(t *testing.T) {
	// A pre-v1.2 refresh token: minted, before the allowlist existed, for whatever the client
	// asked for.
	e := newASEnv(t, "observe")
	toks := e.session(t, rEvil)
	good := e.session(t, rMCP)
	require.True(t, e.refreshEntryExists(t, toks.RefreshToken), "control: the refresh entry exists")
	e.setConfig(t, rCfg("enforce"))

	errorOf(t, e.refreshHTTP(toks.RefreshToken), http.StatusBadRequest, "invalid_grant")
	require.False(t, e.refreshEntryExists(t, toks.RefreshToken), "the refused refresh token was not burned in the store")
	require.EqualValues(t, 1, e.counts(t)["refresh_token/unlisted/rejected"])

	// Allowlist the audience and present the same token again: it stays dead.
	relisted := rCfg("enforce")
	relisted.ExternalResources = append(relisted.ExternalResources, rEvil)
	e.setConfig(t, relisted)
	errorOf(t, e.refreshHTTP(toks.RefreshToken), http.StatusBadRequest, "invalid_grant")

	// Counterweight: a token bound to a listed audience rotates, once, keeping its audience.
	require.True(t, e.refreshEntryExists(t, good.RefreshToken))
	rec := e.refreshHTTP(good.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	next := decode(t, rec)
	require.Equal(t, []string{rMCP}, e.audOf(t, next.AccessToken))
	require.False(t, e.refreshEntryExists(t, good.RefreshToken), "rotation consumes the old token")
	require.True(t, e.refreshEntryExists(t, next.RefreshToken))
	require.EqualValues(t, 1, e.counts(t)["refresh_token/mcp/accepted"])
}

func TestRefreshInObserveModeRotatesOnceAndCounts(t *testing.T) {
	e := newASEnv(t, "observe")
	toks := e.session(t, rEvil)
	before := e.counts(t)

	rec := e.refreshHTTP(toks.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	next := decode(t, rec)
	require.Equal(t, []string{rEvil}, e.audOf(t, next.AccessToken), "the audience carries forward as before")
	require.Equal(t, []string{rEvil}, e.refreshAudOf(t, next.RefreshToken))
	require.False(t, e.refreshEntryExists(t, toks.RefreshToken))
	require.True(t, e.refreshEntryExists(t, next.RefreshToken))
	after := e.counts(t)
	require.EqualValues(t, 1, after["refresh_token/unlisted/would_reject"], "counted exactly once")
	for k, v := range before {
		require.Equal(t, v, after[k], "an authorize-time series moved: %s", k)
	}
	// A reused (already rotated) token is refused as before, and not counted again.
	errorOf(t, e.refreshHTTP(toks.RefreshToken), http.StatusBadRequest, "invalid_grant")
	require.EqualValues(t, 1, e.counts(t)["refresh_token/unlisted/would_reject"])
}

// In observe mode a mismatched `resource` is counted only if the request then succeeds: a
// refresh of an already-rotated token, or one for a tenant that is gone, records nothing, and a
// successful one records the mismatch once beside its own acceptance.
func TestAnObserveMismatchIsCountedOnlyWhenTheRequestSucceeds(t *testing.T) {
	e := newASEnv(t, "observe")
	toks := e.session(t, rMCP)
	base := e.counts(t)

	rec := e.refreshHTTP(toks.RefreshToken, rArea)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after := e.counts(t)
	require.EqualValues(t, 1, after["refresh_token/area/would_reject"], "a served mismatch is counted once")
	require.EqualValues(t, 1, after["refresh_token/mcp/accepted"])
	for k, v := range base {
		require.Equal(t, v, after[k], "an earlier series moved: %s", k)
	}

	// The same, already-rotated token: refused as before, and counted NOTHING.
	errorOf(t, e.refreshHTTP(toks.RefreshToken, rArea), http.StatusBadRequest, "invalid_grant")
	require.Equal(t, after, e.counts(t), "a refresh that failed was counted")

	// An authorization code whose redemption fails after the resource check counts nothing.
	code := e.browse(t, e.query(rMCP), nil).code(t)
	before := e.counts(t)
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {sessVerifier},
		"redirect_uri": {sessRedirect}, "client_id": {sessClient}, "resource": {rArea},
	}
	_, err := e.m.RevokeSessions(context.Background(), rEmail)
	require.NoError(t, err)
	errorOf(t, e.tokenPost(form), http.StatusBadRequest, "invalid_grant")
	require.Equal(t, before, e.counts(t), "a redemption that failed (its sign-in was revoked) was counted")
}

// An OAuth session that never named a resource (Grafana's) refreshes in both modes, and its
// token keeps no audience.
func TestARefreshWithNoAudienceIsUnchanged(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			e := newASEnv(t, mode)
			toks := e.session(t)
			rec := e.refreshHTTP(toks.RefreshToken)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Empty(t, e.audOf(t, decode(t, rec).AccessToken))
			require.EqualValues(t, 1, e.counts(t)["refresh_token/none/accepted"])
		})
	}
}

// ---------------------------------------------------------------------------------------------
// client_credentials
// ---------------------------------------------------------------------------------------------

type ccAS struct {
	*ccEnv
	reg *prometheus.Registry
	met *OAuthResourceMetrics
}

func newCCAS(t *testing.T, cfg config.TokenAudienceConfiguration) *ccAS {
	t.Helper()
	e := &ccAS{ccEnv: newCCEnv(t), reg: prometheus.NewRegistry()}
	e.met = NewOAuthResourceMetrics(e.reg, "usermanagement")
	require.NoError(t, e.m.SetTokenAudience(cfg, e.met))
	return e
}

func (e *ccAS) counts(t *testing.T) map[string]float64 {
	return gatherSeries(t, e.reg, checksMetric)
}
func (e *ccAS) unscoped(t *testing.T) map[string]float64 {
	return gatherSeries(t, e.reg, unscopedMetric)
}

func TestClientCredentialsEnforcesTheAllowlist(t *testing.T) {
	e := newCCAS(t, rCfg("enforce"))
	id, secret := e.account(t, "acme", "ci", nil, "device:read")

	// Negative control: an unlisted resource is refused for an AUTHENTICATED sender, and counted.
	rec := e.grant(t, id, secret, "scope", auth.ScopeReadOnly, "resource", rEvil)
	errorOf(t, rec, http.StatusBadRequest, "invalid_target")
	require.Equal(t, map[string]float64{"client_credentials/unlisted/rejected": 1}, e.counts(t))

	// An unauthenticated sender asking for the same gets the one invalid_client every failure
	// gets, and moves nothing.
	bad := e.grant(t, id, secret+"x", "scope", auth.ScopeReadOnly, "resource", rEvil)
	errorOf(t, bad, http.StatusUnauthorized, "invalid_client")
	require.Equal(t, map[string]float64{"client_credentials/unlisted/rejected": 1}, e.counts(t))

	// Counterweights: listed and absent pass, with the audience unchanged from today's.
	for name, c := range map[string]struct {
		res   []string
		class string
		aud   []string
	}{
		"mcp":      {[]string{"resource", rMCP}, "mcp", []string{rMCP}},
		"area":     {[]string{"resource", rArea}, "area", []string{rArea}},
		"external": {[]string{"resource", rExt}, "external", []string{rExt}},
		"none":     {nil, "none", nil},
	} {
		t.Run(name, func(t *testing.T) {
			before := e.counts(t)
			rec := e.grant(t, id, secret, append([]string{"scope", auth.ScopeReadOnly}, c.res...)...)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, c.aud, []string(mustClaims(t, e.ccEnv, rec).Audience))
			require.EqualValues(t, before["client_credentials/"+c.class+"/accepted"]+1,
				e.counts(t)["client_credentials/"+c.class+"/accepted"])
		})
	}
}

func TestClientCredentialsObserveServesAnUnlistedResourceAndCountsIt(t *testing.T) {
	e := newCCAS(t, rCfg("observe"))
	id, secret := e.account(t, "acme", "ci", nil, "device:read")
	rec := e.grant(t, id, secret, "scope", auth.ScopeReadOnly, "resource", rEvil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{rEvil}, []string(mustClaims(t, e.ccEnv, rec).Audience), "today's behaviour: the raw resource is the audience")
	require.Equal(t, map[string]float64{"client_credentials/unlisted/would_reject": 1}, e.counts(t))
}

// The pre-existing refusals (more than one resource, a malformed one) are shipped behaviour
// and stay unconditional in every mode; they are not new tightening and count nothing.
func TestClientCredentialsKeepsItsExistingRefusalsInEveryMode(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		e := newCCAS(t, rCfg(mode))
		id, secret := e.account(t, "acme", "ci", nil, "device:read")
		errorOf(t, e.grant(t, id, secret, "resource", rMCP, "resource", rArea), http.StatusBadRequest, "invalid_target")
		errorOf(t, e.grant(t, id, secret, "resource", "not-a-uri"), http.StatusBadRequest, "invalid_target")
		errorOf(t, e.grant(t, id, secret, "resource", ""), http.StatusBadRequest, "invalid_target")
		require.Empty(t, e.counts(t))
	}
}

// Counters record only after the client authenticated. A wrong secret with an unlisted
// resource and no scope moves NEITHER counter and writes no warning, in either mode.
func TestClientCredentialsPreAuthCountsNothing(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			e := newCCAS(t, rCfg(mode))
			id, secret := e.account(t, "acme", "ci", nil, "device:read")
			logs := logSink.Capture(t)

			// An unlisted resource with no scope, wrong secret.
			rec := e.grant(t, id, secret+"x", "resource", rEvil)
			// The same answer in both modes: authentication fails before the allowlist is acted on.
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			require.Empty(t, e.counts(t))
			require.Empty(t, e.unscoped(t))

			// A listed resource with no scope, wrong secret: the rule 3 path.
			rec = e.grant(t, "dcsa_nobody_acme", "dcsas_nope", "resource", rMCP)
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			require.Empty(t, e.counts(t))
			require.Empty(t, e.unscoped(t))

			// Positive control: the sink is attached, and an authenticated request that WOULD be
			// counted writes its line. Without it the absence above proves nothing.
			if mode == "observe" {
				ok := e.grant(t, id, secret, "resource", rEvil)
				require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
				require.Contains(t, logs.String(), warnResource, "the sink is detached: an empty capture proves nothing")
				require.NotContains(t, strings.SplitN(logs.String(), warnResource, 2)[0], "dcsa_nobody_acme")
			}
		})
	}
}

// Rule 3 is DEPRECATED, not enforced, in this release: a resource with no scope is counted and
// warned about, and served exactly as before in EVERY mode, with the account's full authorities.
func TestRule3IsCountedAndNeverRefused(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		for _, rule3 := range []string{"", "observe"} {
			t.Run(mode+"/unscopedClientCredentials="+rule3, func(t *testing.T) {
				cfg := rCfg(mode)
				cfg.UnscopedClientCredentials = rule3
				e := newCCAS(t, cfg)
				id, secret := e.account(t, "acme", "ci", nil, "device:read", "command:write")
				logs := logSink.Capture(t)

				for _, scope := range []string{"", "   "} {
					rec := e.grant(t, id, secret, "resource", rMCP, "scope", scope)
					require.Equal(t, http.StatusOK, rec.Code, "scope %q: %s", scope, rec.Body.String())
					c := mustClaims(t, e.ccEnv, rec)
					require.ElementsMatch(t, []string{"device:read", "command:write"}, c.Authorities,
						"an unscoped, resource-bound token still carries the account's full authorities in this release")
				}
				require.Equal(t, map[string]float64{"would_reject": 2}, e.unscoped(t))
				require.Contains(t, logs.String(), warnUnscoped)

				// Counterweights: a scope, or no resource, is not counted.
				before := e.unscoped(t)
				require.Equal(t, http.StatusOK, e.grant(t, id, secret, "resource", rMCP, "scope", auth.ScopeReadOnly).Code)
				require.Equal(t, http.StatusOK, e.grant(t, id, secret).Code, "no resource and no scope: today's behaviour")
				require.Equal(t, http.StatusOK, e.grant(t, id, secret, "scope", "   ").Code)
				require.Equal(t, before, e.unscoped(t))
			})
		}
	}
}

// ---------------------------------------------------------------------------------------------
// the log warning
// ---------------------------------------------------------------------------------------------

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

func newClockedPolicy(t *testing.T, cfg config.TokenAudienceConfiguration) (*resourcePolicy, *testClock) {
	t.Helper()
	p, err := newResourcePolicy(cfg, NewOAuthResourceMetrics(prometheus.NewRegistry(), "usermanagement"))
	require.NoError(t, err)
	c := &testClock{t: time.Unix(1_800_000_000, 0)}
	p.lim = auth.NewWarnLimiter(c.now)
	return p, c
}

func lineCount(logs, needle string) int { return strings.Count(logs, needle) }

// The limiter is keyed on the client, so one noisy client cannot suppress another's line.
func TestTheWarningIsRateLimitedPerClient(t *testing.T) {
	p, clock := newClockedPolicy(t, rCfg("observe"))
	logs := logSink.Capture(t)
	rc := p.check(rEvil, 1)

	for i := 0; i < 50; i++ {
		p.record(GrantAuthorizationCode, "client-a", "acme", rc)
	}
	require.Equal(t, 1, lineCount(logs.String(), `"client_id":"client-a"`), "client A flooded: one line a minute")
	p.record(GrantAuthorizationCode, "client-b", "acme", rc)
	require.Equal(t, 1, lineCount(logs.String(), `"client_id":"client-b"`), "client B's line was suppressed by client A")

	clock.t = clock.t.Add(2 * time.Minute)
	p.record(GrantAuthorizationCode, "client-a", "acme", rc)
	require.Equal(t, 2, lineCount(logs.String(), `"client_id":"client-a"`))
	require.Contains(t, logs.String(), `"suppressed":49`, "the line reports what it held back")
}

// The authorization endpoint's entry request is pre-login: its warning says the sender is not
// authenticated, where the token endpoint's (client secret checked) says it is.
func TestTheEntryWarningSaysTheSenderIsNotAuthenticated(t *testing.T) {
	e := newASEnv(t, "observe")
	logs := logSink.Capture(t)
	require.Equal(t, http.StatusOK, e.authzGet(e.query(rEvil)).Code)
	out := logs.String()
	require.Contains(t, out, warnResource, "the sink is detached: an empty capture proves nothing")
	require.Contains(t, out, `"sender_authenticated":false`)
	require.Contains(t, out, `"client_id":"`+sessClient+`"`)
}

// Another grant or class for the same client is its own key, and an acceptance never warns.
func TestTheWarningKeyIncludesGrantAndClassAndAcceptancesDoNotWarn(t *testing.T) {
	p, _ := newClockedPolicy(t, rCfg("observe"))
	logs := logSink.Capture(t)
	p.record(GrantAuthorizationCode, "c", "acme", p.check(rEvil, 1))
	p.record(GrantRefreshToken, "c", "acme", p.check(rEvil, 1))
	p.record(GrantAuthorizationCode, "c", "acme", p.check("not-a-uri", 1))
	require.Equal(t, 3, lineCount(logs.String(), warnResource))
	p.record(GrantAuthorizationCode, "c", "acme", p.check(rMCP, 1))
	p.record(GrantAuthorizationCode, "c", "acme", p.check("", 0))
	require.Equal(t, 3, lineCount(logs.String(), warnResource), "an accepted resource wrote a warning")
}

// The resource is attacker-chosen: it is quoted and truncated in the line, never a label.
func TestTheWarningIsInjectionSafe(t *testing.T) {
	p, _ := newClockedPolicy(t, rCfg("observe"))
	logs := logSink.Capture(t)
	evil := "https://evil.example.net\"}\n{\"level\":\"fatal\",\"message\":\"forged\"}" + strings.Repeat("é", 400) + "TAILMARK"
	p.record(GrantClientCredentials, "c", "acme", p.check(evil, 1))

	out := logs.String()
	require.Contains(t, out, warnResource, "the sink is detached: an empty capture proves nothing")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 1, "the resource injected a line")
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec), lines[0])
	require.Equal(t, "warn", rec["level"])
	require.NotContains(t, rec["resource"].(string), "TAILMARK", "the resource was not truncated")
	require.NotContains(t, out, `"level":"fatal"`)
}

// ---------------------------------------------------------------------------------------------
// metric cardinality
// ---------------------------------------------------------------------------------------------

// Whatever a sender puts in `resource`, the label values stay inside the closed sets: the raw
// string is never a label.
func TestMetricLabelsStayInTheClosedSets(t *testing.T) {
	e := newASEnv(t, "observe")
	hostile := []string{
		rEvil, "x", "", "https://" + strings.Repeat("h", 500), "\x00\x01", "ünï://çödé", rMCP + "?q=1",
		"https://a.example/" + strings.Repeat("a", 3000), "javascript:alert(1)", "https://evil.example.net/" + fmt.Sprint(time.Now().UnixNano()),
	}
	for _, r := range hostile {
		e.authzGet(e.query(r))
		e.authzGet(e.query(r, rEvil))
	}
	grants := map[string]bool{"authorization_code": true, "refresh_token": true, "client_credentials": true}
	classes := map[string]bool{"none": true, "area": true, "mcp": true, "external": true, "unlisted": true, "malformed": true, "multiple": true}
	verdicts := map[string]bool{"accepted": true, "would_reject": true, "rejected": true}
	seen := 0
	for key := range e.counts(t) {
		parts := strings.Split(key, "/")
		// a resource label can never contain "/" if it is in the closed set, so a split of
		// anything else would not be exactly three parts
		require.Len(t, parts, 3, key)
		require.True(t, grants[parts[0]], key)
		require.True(t, classes[parts[1]], key)
		require.True(t, verdicts[parts[2]], key)
		seen++
	}
	require.Greater(t, seen, 0, "nothing was recorded, so the check above proved nothing")
	require.LessOrEqual(t, seen, 3*7*3)
}

// ---------------------------------------------------------------------------------------------
// unit
// ---------------------------------------------------------------------------------------------

func TestResourcePolicyCheck(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		p, err := newResourcePolicy(rCfg(mode), nil)
		require.NoError(t, err)
		bad := auth.VerdictWouldReject
		if mode == "enforce" {
			bad = auth.VerdictRejected
		}
		for _, c := range []struct {
			resource string
			count    int
			class    auth.ResourceClass
			verdict  auth.AudienceVerdict
		}{
			{"", 0, auth.ResNone, auth.VerdictAccepted},
			{"", 1, auth.ResNone, auth.VerdictAccepted},
			{rMCP, 1, auth.ResMCP, auth.VerdictAccepted},
			{rArea, 1, auth.ResArea, auth.VerdictAccepted},
			{rExt, 1, auth.ResExternal, auth.VerdictAccepted},
			{rEvil, 1, auth.ResUnlisted, bad},
			{"not-a-uri", 1, auth.ResMalformed, bad},
			{rMCP, 2, auth.ResMultiple, bad},
			{"", 2, auth.ResMultiple, bad},
			{rEvil, 2, auth.ResMultiple, bad},
		} {
			got := p.check(c.resource, c.count)
			require.Equal(t, c.class, got.Class, "%s %q x%d", mode, c.resource, c.count)
			require.Equal(t, c.verdict, got.Verdict, "%s %q x%d", mode, c.resource, c.count)
			require.True(t, got.Checked)
			require.Equal(t, mode == "enforce" && c.verdict != auth.VerdictAccepted, got.Refused())
		}
	}
}

func TestACountedParameterIsNotAnEmptyOne(t *testing.T) {
	p := ParseAuthorizeParams(url.Values{"resource": {"", "a", "", "b"}})
	require.Equal(t, 2, p.ResourceCount)
	require.Equal(t, "", p.Resource, "Resource keeps the first value, as before")
	require.Equal(t, 0, ParseAuthorizeParams(url.Values{"resource": {"", ""}}).ResourceCount)
	require.Equal(t, 0, ParseAuthorizeParams(url.Values{}).ResourceCount)
	require.Equal(t, 1, AuthorizeParams{Resource: "a"}.resourceCount(), "a parked record that predates the count")
}

func TestABoundAudienceWithAnEmptyMemberIsNotNone(t *testing.T) {
	p, err := newResourcePolicy(rCfg("enforce"), nil)
	require.NoError(t, err)
	rc := p.checkBound([]string{""})
	require.Equal(t, auth.ResMalformed, rc.Class)
	require.True(t, rc.Refused())
	require.Equal(t, auth.ResNone, p.checkBound(nil).Class)
	require.Equal(t, auth.ResMultiple, p.checkBound([]string{rMCP, rArea}).Class)
}

func TestSetTokenAudienceRefusesAnInvalidBlock(t *testing.T) {
	m := &Manager{}
	bad := rCfg("enforce")
	bad.Enforcement = "sometimes"
	require.Error(t, m.SetTokenAudience(bad, nil))
	require.Nil(t, m.resources, "a refused block must not install a policy")
	require.NoError(t, m.SetTokenAudience(rCfg("enforce"), nil))
	require.True(t, m.resourcePolicy().enforcing())
	require.False(t, (&Manager{}).resourcePolicy().enforcing(), "an unconfigured manager runs the release default")
}

func mustClaims(t *testing.T, e *ccEnv, rec *httptest.ResponseRecorder) *auth.Claims {
	t.Helper()
	c, err := e.validator.Validate(decode(t, rec).AccessToken)
	require.NoError(t, err)
	return c
}

// An allowlist entry the configuration accepts must be one a request can carry: the two length caps
// are one number, so a configured identifier is never longer than ValidateResource admits.
func TestAllowlistLengthCapMatchesTheRequestCap(t *testing.T) {
	require.Equal(t, maxResourceLen, config.MaxTokenAudienceResourceLen)
}
