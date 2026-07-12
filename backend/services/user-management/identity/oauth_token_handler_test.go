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
	refresh func(ctx context.Context, refreshToken, scope string) (*OAuthTokens, error),
) http.Handler {
	if redeem == nil {
		redeem = func(context.Context, string, string, string, string) (*OAuthTokens, error) { return okTokens(), nil }
	}
	if refresh == nil {
		refresh = func(context.Context, string, string) (*OAuthTokens, error) { return okTokens(), nil }
	}
	return TokenHandler(redeem, refresh)
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
	var gotToken, gotScope string
	h := newTestHandler(nil, func(_ context.Context, refreshToken, scope string) (*OAuthTokens, error) {
		gotToken, gotScope = refreshToken, scope
		return okTokens(), nil
	})
	rec := postForm(h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"the-refresh"},
		"scope":         {"read-only"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotToken != "the-refresh" || gotScope != "read-only" {
		t.Errorf("refresh params not passed: %q %q", gotToken, gotScope)
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
	h := newTestHandler(nil, func(context.Context, string, string) (*OAuthTokens, error) {
		return nil, context.DeadlineExceeded // a non-oauthError
	})
	rec := postForm(h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	assertOAuthError(t, rec, "server_error")
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
