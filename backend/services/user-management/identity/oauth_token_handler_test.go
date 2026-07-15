// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// okTokens is a canned successful grant result.
func okTokens() *OAuthTokens {
	return &OAuthTokens{AccessToken: "at", RefreshToken: "rt", Scope: "read-only", ExpiresIn: 900}
}

// postForm drives the handler with a urlencoded form body.
func postForm(h http.Handler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newTestHandler(
	redeem func(ctx context.Context, code, clientID, redirectURI, verifier string) (*OAuthTokens, error),
	refresh func(ctx context.Context, refreshToken, scope, clientID string) (*OAuthTokens, error),
) http.Handler {
	// Default authenticator: accept everyone (public-client behavior) so the existing
	// grant-shaping tests are unaffected. Confidential-client auth is exercised by
	// newTestHandlerAuth + the Manager.AuthenticateClient tests.
	return newTestHandlerAuth(nil, redeem, refresh)
}

// newTestHandlerAuth builds a handler with an explicit client authenticator, so a
// test can assert the pre-grant client-auth step.
func newTestHandlerAuth(
	authClient func(ctx context.Context, clientID, secret string, presented bool) error,
	redeem func(ctx context.Context, code, clientID, redirectURI, verifier string) (*OAuthTokens, error),
	refresh func(ctx context.Context, refreshToken, scope, clientID string) (*OAuthTokens, error),
) http.Handler {
	if authClient == nil {
		authClient = func(context.Context, string, string, bool) error { return nil }
	}
	if redeem == nil {
		redeem = func(context.Context, string, string, string, string) (*OAuthTokens, error) { return okTokens(), nil }
	}
	if refresh == nil {
		refresh = func(context.Context, string, string, string) (*OAuthTokens, error) { return okTokens(), nil }
	}
	return TokenHandler(authClient, redeem, refresh)
}

func TestTokenHandler_MethodNotPost(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, TokenPath, nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET status = %d, want 400", rec.Code)
	}
	assertOAuthError(t, rec, "invalid_request")
}

func TestTokenHandler_MissingGrantType(t *testing.T) {
	rec := postForm(newTestHandler(nil, nil), url.Values{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	assertOAuthError(t, rec, "invalid_request")
}

func TestTokenHandler_UnsupportedGrantType(t *testing.T) {
	rec := postForm(newTestHandler(nil, nil), url.Values{"grant_type": {"password"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	assertOAuthError(t, rec, "unsupported_grant_type")
}

func TestTokenHandler_AuthCodeMissingParams(t *testing.T) {
	// Missing code_verifier / client_id / redirect_uri → invalid_request before the
	// grant func is called.
	called := false
	h := newTestHandler(func(context.Context, string, string, string, string) (*OAuthTokens, error) {
		called = true
		return okTokens(), nil
	}, nil)
	rec := postForm(h, url.Values{"grant_type": {"authorization_code"}, "code": {"abc"}})
	if rec.Code != http.StatusBadRequest || called {
		t.Errorf("status=%d called=%v, want 400 and grant func not called", rec.Code, called)
	}
	assertOAuthError(t, rec, "invalid_request")
}

func TestTokenHandler_AuthCodeSuccess(t *testing.T) {
	var gotCode, gotClient, gotRedirect, gotVerifier string
	h := newTestHandler(func(_ context.Context, code, clientID, redirectURI, verifier string) (*OAuthTokens, error) {
		gotCode, gotClient, gotRedirect, gotVerifier = code, clientID, redirectURI, verifier
		return okTokens(), nil
	}, nil)
	rec := postForm(h, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"the-code"},
		"code_verifier": {"the-verifier"},
		"client_id":     {"mcp"},
		"redirect_uri":  {"http://127.0.0.1:5000/cb"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotCode != "the-code" || gotClient != "mcp" || gotRedirect != "http://127.0.0.1:5000/cb" || gotVerifier != "the-verifier" {
		t.Errorf("params not passed through: %q %q %q %q", gotCode, gotClient, gotRedirect, gotVerifier)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body.AccessToken != "at" || body.TokenType != "Bearer" || body.ExpiresIn != 900 ||
		body.RefreshToken != "rt" || body.Scope != "read-only" {
		t.Errorf("unexpected token response: %+v", body)
	}
}

func TestTokenHandler_RefreshSuccessAndScopePassthrough(t *testing.T) {
	var gotToken, gotScope, gotClientID string
	// A confidential client refreshing over Basic: the authenticated client_id must
	// reach the refresh grant so it can enforce the token's client binding.
	h := newTestHandlerAuth(nil, nil, func(_ context.Context, refreshToken, scope, clientID string) (*OAuthTokens, error) {
		gotToken, gotScope, gotClientID = refreshToken, scope, clientID
		return okTokens(), nil
	})
	rec := postFormBasic(h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"the-refresh"},
		"scope":         {"read-only"},
	}, "grafana", "the-secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotToken != "the-refresh" || gotScope != "read-only" || gotClientID != "grafana" {
		t.Errorf("refresh params not passed: token=%q scope=%q clientID=%q", gotToken, gotScope, gotClientID)
	}
}

func TestTokenHandler_RefreshMissingToken(t *testing.T) {
	rec := postForm(newTestHandler(nil, nil), url.Values{"grant_type": {"refresh_token"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	assertOAuthError(t, rec, "invalid_request")
}

func TestTokenHandler_MapsOAuthError(t *testing.T) {
	h := newTestHandler(func(context.Context, string, string, string, string) (*OAuthTokens, error) {
		return nil, errInvalidGrant("bad code")
	}, nil)
	rec := postForm(h, url.Values{
		"grant_type": {"authorization_code"}, "code": {"x"}, "code_verifier": {"v"},
		"client_id": {"c"}, "redirect_uri": {"https://c/cb"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	assertOAuthError(t, rec, "invalid_grant")
}

func TestTokenHandler_GenericErrorBecomesServerError(t *testing.T) {
	h := newTestHandlerAuth(nil, nil, func(context.Context, string, string, string) (*OAuthTokens, error) {
		return nil, context.DeadlineExceeded // a non-oauthError
	})
	rec := postForm(h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	assertOAuthError(t, rec, "server_error")
}

// postFormBasic drives the handler with a urlencoded body AND an HTTP Basic
// Authorization header (client_secret_basic).
func postFormBasic(h http.Handler, form url.Values, clientID, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A failed client authentication (invalid_client) is a 401 with a WWW-Authenticate
// challenge, and the grant func is never reached.
func TestTokenHandler_ClientAuthRejected(t *testing.T) {
	grantCalled := false
	h := newTestHandlerAuth(
		func(context.Context, string, string, bool) error { return errInvalidClient("nope") },
		func(context.Context, string, string, string, string) (*OAuthTokens, error) {
			grantCalled = true
			return okTokens(), nil
		}, nil)
	rec := postFormBasic(h, url.Values{
		"grant_type": {"authorization_code"}, "code": {"c"}, "code_verifier": {"v"}, "redirect_uri": {"https://c/cb"},
	}, "grafana", "bad-secret")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if wa := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(wa, "Basic") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge", wa)
	}
	if grantCalled {
		t.Error("grant func must not run after client-auth failure")
	}
	assertOAuthError(t, rec, "invalid_client")
}

// A Basic-authenticated client's client_id (header, not body) is what reaches the
// grant — a confidential client need not repeat client_id in the form.
func TestTokenHandler_BasicClientIdReachesGrant(t *testing.T) {
	var gotClientID, gotSecret string
	var gotPresented bool
	h := newTestHandlerAuth(
		func(_ context.Context, clientID, secret string, presented bool) error {
			gotClientID, gotSecret, gotPresented = clientID, secret, presented
			return nil
		},
		func(_ context.Context, _, clientID, _, _ string) (*OAuthTokens, error) {
			if clientID != "grafana" {
				t.Errorf("grant clientID = %q, want grafana (from Basic)", clientID)
			}
			return okTokens(), nil
		}, nil)
	rec := postFormBasic(h, url.Values{
		"grant_type": {"authorization_code"}, "code": {"c"}, "code_verifier": {"v"}, "redirect_uri": {"https://c/cb"},
	}, "grafana", "the-secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotClientID != "grafana" || gotSecret != "the-secret" || !gotPresented {
		t.Errorf("authenticateClient got (%q,%q,%v), want (grafana,the-secret,true)", gotClientID, gotSecret, gotPresented)
	}
}

// extractClientCredentials enforces the RFC 6749 §2.3.1 rules: one auth method
// only, and a Basic client_id must not disagree with a body client_id.
func TestExtractClientCredentials(t *testing.T) {
	build := func(form url.Values, basic bool, bid, bsec string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, TokenPath, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if basic {
			r.SetBasicAuth(bid, bsec)
		}
		_ = r.ParseForm()
		return r
	}

	// Basic only.
	c, e := extractClientCredentials(build(url.Values{}, true, "grafana", "sec"))
	if e != nil || c.clientID != "grafana" || c.secret != "sec" || !c.presented {
		t.Errorf("basic-only: (%+v, %v)", c, e)
	}
	// Post only.
	c, e = extractClientCredentials(build(url.Values{"client_id": {"g"}, "client_secret": {"s"}}, false, "", ""))
	if e != nil || c.clientID != "g" || c.secret != "s" || !c.presented {
		t.Errorf("post-only: (%+v, %v)", c, e)
	}
	// Public (client_id, no secret).
	c, e = extractClientCredentials(build(url.Values{"client_id": {"mcp"}}, false, "", ""))
	if e != nil || c.clientID != "mcp" || c.presented {
		t.Errorf("public: (%+v, %v)", c, e)
	}
	// Both methods → invalid_request.
	if _, e = extractClientCredentials(build(url.Values{"client_secret": {"s"}}, true, "g", "sec")); e == nil || e.Code != "invalid_request" {
		t.Errorf("both methods: got %v, want invalid_request", e)
	}
	// Basic id disagrees with body client_id → invalid_request.
	if _, e = extractClientCredentials(build(url.Values{"client_id": {"other"}}, true, "grafana", "sec")); e == nil || e.Code != "invalid_request" {
		t.Errorf("id mismatch: got %v, want invalid_request", e)
	}
	// Matching body client_id alongside Basic is fine.
	if c, e = extractClientCredentials(build(url.Values{"client_id": {"grafana"}}, true, "grafana", "sec")); e != nil || c.clientID != "grafana" {
		t.Errorf("matching id: (%+v, %v)", c, e)
	}
}

func assertOAuthError(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("error Cache-Control = %q, want no-store", cc)
	}
	var body tokenErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body not JSON: %v", err)
	}
	if body.Error != wantCode {
		t.Errorf("error = %q, want %q", body.Error, wantCode)
	}
}
