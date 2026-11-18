// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
)

type unreadCORSBody struct{ t *testing.T }

func (b unreadCORSBody) Read([]byte) (int, error) {
	b.t.Error("preflight body was read")
	return 0, http.ErrBodyReadAfterClose
}

func (unreadCORSBody) Close() error { return nil }

func assertEndpointCORS(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	if got := h.Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id, WWW-Authenticate" {
		t.Errorf("Expose-Headers = %q", got)
	}
	if _, present := h["Access-Control-Allow-Credentials"]; present {
		t.Error("bearer-only endpoint must never allow credentials")
	}
}

func TestRoutesBrowserCORS(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	verifications := 0
	mux := http.NewServeMux()
	Routes(mux, testResource, "https://as.example.com", func() *coreauth.Validator {
		verifications++
		return validator()
	}, NewGraphQLClient(), DefaultLimits(), sessionCapOption(1, 1))
	issue := func(scope string) string {
		t.Helper()
		tok, err := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read"},
			scope, []string{testResource}, false, "mcp", "j-cors")
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + tok.Token
	}
	valid := issue(coreauth.ScopeReadOnly)

	t.Run("preflight bypasses auth and body", func(t *testing.T) {
		for _, auth := range []string{"", valid} {
			r := httptest.NewRequest(http.MethodOptions, "/", nil)
			r.Body = unreadCORSBody{t}
			r.Header.Set("Origin", "https://client.example")
			r.Header.Set("Access-Control-Request-Method", "POST")
			r.Header.Set("Access-Control-Request-Headers", "authorization, content-type, mcp-session-id")
			r.Header.Set("Authorization", auth)
			before := verifications
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
				t.Errorf("preflight = %d %s, want empty 204", w.Code, w.Body.String())
			}
			if verifications != before {
				t.Error("preflight reached bearer verifier")
			}
			assertEndpointCORS(t, w.Header())
			for name, want := range map[string]string{
				"Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
				"Access-Control-Allow-Headers": "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID",
			} {
				if got := w.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			age, err := strconv.Atoi(w.Header().Get("Access-Control-Max-Age"))
			if err != nil || age <= 0 || age > 86400 {
				t.Errorf("Max-Age must be positive and bounded to one day: %q", w.Header().Get("Access-Control-Max-Age"))
			}
		}
	})

	t.Run("only complete preflights bypass bearer", func(t *testing.T) {
		for _, tc := range []struct{ method, origin, requested string }{
			{http.MethodOptions, "", ""},
			{http.MethodOptions, "https://client.example", ""},
			{http.MethodOptions, "", "POST"},
			{http.MethodPost, "https://client.example", "POST"},
			{http.MethodGet, "https://client.example", ""},
			{http.MethodDelete, "https://client.example", ""},
		} {
			r := httptest.NewRequest(tc.method, "/", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Access-Control-Request-Method", tc.requested)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
				t.Errorf("%+v: want 401 challenge, got %d %v", tc, w.Code, w.Header())
			}
			assertEndpointCORS(t, w.Header())
		}
	})

	post := func(auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(sessionInitialize))
		r.Header.Set("Origin", "https://client.example")
		r.Header.Set("Authorization", auth)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		assertEndpointCORS(t, w.Header())
		return w
	}
	t.Run("scope floor still challenges", func(t *testing.T) {
		w := post(issue("some-other-scope"))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Header().Get("WWW-Authenticate"), "insufficient_scope") {
			t.Errorf("want 403 scope challenge, got %d %v", w.Code, w.Header())
		}
	})
	t.Run("valid token initializes and session cap still applies", func(t *testing.T) {
		id := requireSession(t, post(valid))
		defer func() {
			r := httptest.NewRequest(http.MethodDelete, "/", nil)
			r.Header.Set("Authorization", valid)
			r.Header.Set("Mcp-Session-Id", id)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			assertEndpointCORS(t, w.Header())
			if w.Code != http.StatusNoContent {
				t.Errorf("DELETE = %d, want 204", w.Code)
			}
		}()
		requireSessionRefusal(t, post(valid))
	})
}
