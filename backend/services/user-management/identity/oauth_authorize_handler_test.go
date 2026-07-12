// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
)

// fakeAuthorizeSvc is an injectable AuthorizeService for driving the handler.
type fakeAuthorizeSvc struct {
	client      *iam.OAuthClient
	resolveErr  error
	loginResult *IdentityAuth
	loginErr    error
	email       string
	emailErr    error
	code        string
	codeErr     error
}

func (f *fakeAuthorizeSvc) ResolveAuthorizeClient(context.Context, string, string) (*iam.OAuthClient, error) {
	return f.client, f.resolveErr
}
func (f *fakeAuthorizeSvc) Login(context.Context, string, string) (*IdentityAuth, error) {
	return f.loginResult, f.loginErr
}
func (f *fakeAuthorizeSvc) IdentityEmail(string) (string, error) { return f.email, f.emailErr }
func (f *fakeAuthorizeSvc) IssueAuthorizationCode(context.Context, *iam.OAuthClient, AuthorizeParams, string, string) (string, error) {
	return f.code, f.codeErr
}

// validClient is registered for read-only with a loopback redirect.
func validClient() *iam.OAuthClient {
	return &iam.OAuthClient{ClientId: "mcp", Scopes: []string{"read-only"}, RedirectURIs: []string{"http://127.0.0.1/cb"}, Enabled: true}
}

// validParams is a well-formed authorization request.
func validParams() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {"mcp"},
		"redirect_uri":          {"http://127.0.0.1:5000/cb"},
		"scope":                 {"read-only"},
		"state":                 {"the-state"},
		"code_challenge":        {"abc"},
		"code_challenge_method": {"S256"},
	}
}

func getAuthorize(svc AuthorizeService, q url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	AuthorizeHandler(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, AuthorizePath+"?"+q.Encode(), nil))
	return rec
}

func postAuthorize(svc AuthorizeService, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, AuthorizePath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	AuthorizeHandler(svc).ServeHTTP(rec, req)
	return rec
}

func TestAuthorize_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	AuthorizeHandler(&fakeAuthorizeSvc{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, AuthorizePath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// An unknown/unregistered client renders an error PAGE and must NEVER redirect
// (the redirect_uri is untrusted).
func TestAuthorize_ClientUnknownRendersErrorNoRedirect(t *testing.T) {
	svc := &fakeAuthorizeSvc{resolveErr: ErrAuthorizeClientUnknown}
	rec := getAuthorize(svc, validParams())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("must not redirect on an untrusted client/redirect; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "Do not proceed") {
		t.Errorf("expected the error page body")
	}
}

// A parameter error (here response_type != code), once the client+redirect are
// trusted, redirects back with an RFC 6749 §4.1.2.1 error code + state.
func TestAuthorize_ParamErrorRedirectsBack(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient()}
	q := validParams()
	q.Set("response_type", "token")
	rec := getAuthorize(svc, q)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "unsupported_response_type" {
		t.Errorf("error = %q, want unsupported_response_type", loc.Query().Get("error"))
	}
	if loc.Query().Get("state") != "the-state" {
		t.Errorf("state not echoed on the error redirect")
	}
}

func TestAuthorize_GETRendersLogin(t *testing.T) {
	rec := getAuthorize(&fakeAuthorizeSvc{client: validClient()}, validParams())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="password"`) || !strings.Contains(body, `value="login"`) {
		t.Errorf("expected the login form")
	}
	// The state is reflected into a hidden field and MUST be HTML-escaped in context
	// (auto-escaping by html/template) — smoke-check a scripty state can't break out.
	q := validParams()
	q.Set("state", `"><script>x</script>`)
	rec = getAuthorize(&fakeAuthorizeSvc{client: validClient()}, q)
	if strings.Contains(rec.Body.String(), "<script>x</script>") {
		t.Errorf("reflected state was not escaped — XSS")
	}
}

func TestAuthorize_LoginSuccessRendersConsent(t *testing.T) {
	svc := &fakeAuthorizeSvc{
		client:      validClient(),
		loginResult: &IdentityAuth{IdentityToken: "id-tok", Memberships: []MembershipInfo{{Tenant: "acme"}}},
	}
	form := validParams()
	form.Set("step", "login")
	form.Set("email", "a@b.c")
	form.Set("password", "pw")
	rec := postAuthorize(svc, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="consent"`) || !strings.Contains(body, `value="allow"`) {
		t.Errorf("expected the consent form")
	}
	if !strings.Contains(body, "acme") || !strings.Contains(body, "id-tok") {
		t.Errorf("consent form should carry the tenant options + identity token")
	}
	if !strings.Contains(body, "read-only") {
		t.Errorf("consent form should list the requested scope")
	}
}

func TestAuthorize_LoginFailureRerendersLogin(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient(), loginErr: ErrInvalidCredentials}
	form := validParams()
	form.Set("step", "login")
	rec := postAuthorize(svc, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Errorf("expected the login error message")
	}
}

func TestAuthorize_ConsentDenyRedirectsAccessDenied(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient()}
	form := validParams()
	form.Set("step", "consent")
	form.Set("action", "deny")
	rec := postAuthorize(svc, form)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "access_denied" {
		t.Errorf("error = %q, want access_denied", loc.Query().Get("error"))
	}
}

func TestAuthorize_ConsentAllowRedirectsCode(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient(), email: "a@b.c", code: "the-code"}
	form := validParams()
	form.Set("step", "consent")
	form.Set("action", "allow")
	form.Set("identity_token", "id-tok")
	form.Set("tenant", "acme")
	rec := postAuthorize(svc, form)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("code") != "the-code" {
		t.Errorf("code = %q, want the-code", loc.Query().Get("code"))
	}
	if loc.Query().Get("state") != "the-state" {
		t.Errorf("state not echoed on the success redirect")
	}
	if loc.Host != "127.0.0.1:5000" || loc.Path != "/cb" {
		t.Errorf("redirect went to the wrong place: %s", loc)
	}
}

// A consent POST whose (attacker-tampered) tenant the subject cannot access must
// fail closed: IssueAuthorizationCode denies it → access_denied redirect, never a
// code. This locks the invariant at the handler layer.
func TestAuthorize_ConsentTenantTamperFailsClosed(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient(), email: "a@b.c", codeErr: errTenantAccessDenied}
	form := validParams()
	form.Set("step", "consent")
	form.Set("action", "allow")
	form.Set("identity_token", "id-tok")
	form.Set("tenant", "a-tenant-the-user-cannot-access")
	rec := postAuthorize(svc, form)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Has("code") {
		t.Errorf("a code must NOT be issued for an inaccessible tenant")
	}
	if loc.Query().Get("error") != "access_denied" {
		t.Errorf("error = %q, want access_denied", loc.Query().Get("error"))
	}
}

// The rendered pages carry a live identity token + reflect an auth code, so they
// must be non-cacheable and framing-denied.
func TestAuthorize_PagesAreNonCacheable(t *testing.T) {
	rec := getAuthorize(&fakeAuthorizeSvc{client: validClient()}, validParams())
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", xfo)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
}

// A bad/expired identity token on consent renders an error page (does not redirect
// with a code).
func TestAuthorize_ConsentBadIdentityToken(t *testing.T) {
	svc := &fakeAuthorizeSvc{client: validClient(), emailErr: ErrInvalidToken}
	form := validParams()
	form.Set("step", "consent")
	form.Set("action", "allow")
	rec := postAuthorize(svc, form)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("must not redirect with a bad identity token")
	}
}

func TestBuildRedirect(t *testing.T) {
	got := buildRedirect("http://127.0.0.1:5000/cb", map[string]string{"code": "c", "state": "s", "empty": ""})
	u, _ := url.Parse(got)
	if u.Query().Get("code") != "c" || u.Query().Get("state") != "s" {
		t.Errorf("params not appended: %s", got)
	}
	if u.Query().Has("empty") {
		t.Errorf("empty param should be omitted")
	}
	// A redirect URI with a pre-existing query keeps it.
	got = buildRedirect("https://c.example.com/cb?x=1", map[string]string{"code": "c"})
	u, _ = url.Parse(got)
	if u.Query().Get("x") != "1" || u.Query().Get("code") != "c" {
		t.Errorf("existing query not preserved: %s", got)
	}
}
