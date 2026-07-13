// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package httpsink

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultClientBlocksRedirects(t *testing.T) {
	if DefaultClient.CheckRedirect == nil {
		t.Fatal("expected a CheckRedirect that blocks redirects")
	}
	if err := DefaultClient.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect = %v, want ErrUseLastResponse", err)
	}
}

func TestIsReservedHeader(t *testing.T) {
	for _, name := range []string{"Authorization", "authorization", "X-DC-Tenant", "x-dc-service"} {
		if !IsReservedHeader(name) {
			t.Errorf("IsReservedHeader(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"X-Custom", "Content-Type", "X-Api-Key"} {
		if IsReservedHeader(name) {
			t.Errorf("IsReservedHeader(%q) = true, want false", name)
		}
	}
}

func TestAuthHeaderValue(t *testing.T) {
	// Zero Auth ⇒ Authorization: Bearer <secret>.
	if n, v := (Auth{}).HeaderValue("s3cr3t"); n != "Authorization" || v != "Bearer s3cr3t" {
		t.Fatalf("default = (%q,%q), want (Authorization, Bearer s3cr3t)", n, v)
	}
	// Custom header, empty scheme ⇒ raw token.
	if n, v := (Auth{Header: "X-API-Key"}).HeaderValue("raw"); n != "X-API-Key" || v != "raw" {
		t.Fatalf("custom = (%q,%q), want (X-API-Key, raw)", n, v)
	}
	// Custom scheme on the default header.
	if n, v := (Auth{Scheme: "Token"}).HeaderValue("t"); n != "Authorization" || v != "Token t" {
		t.Fatalf("scheme = (%q,%q), want (Authorization, Token t)", n, v)
	}
}

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"http://x/y", "https://x.example/z"} {
		if _, err := ValidateURL(ok); err != nil {
			t.Errorf("ValidateURL(%q) errored: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://x", "file:///etc/passwd", "not a url", "", "https://", "https://user:pass@host/x", "http://bob@host/x"} {
		if _, err := ValidateURL(bad); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want error", bad)
		}
	}
}

func TestValidateHeader(t *testing.T) {
	ok := []struct{ name, value string }{
		{"X-Custom", "ok"},
		{"X-Api-Key", "abc-123"},
		{"Content-Type", "application/json"},
		{"X-Tab", "a\tb"}, // HTAB is allowed in a value
	}
	for _, h := range ok {
		if err := ValidateHeader(h.name, h.value); err != nil {
			t.Errorf("ValidateHeader(%q,%q) errored: %v", h.name, h.value, err)
		}
	}
	bad := []struct{ name, value string }{
		{"", "v"},               // empty name
		{"bad name", "v"},       // space in name
		{"X-Bad\n", "v"},        // newline in name
		{"X-CRLF", "a\r\nEvil"}, // CRLF injection in value
		{"X-Nul", "a\x00b"},     // NUL in value
	}
	for _, h := range bad {
		if err := ValidateHeader(h.name, h.value); err == nil {
			t.Errorf("ValidateHeader(%q,%q) = nil, want error", h.name, h.value)
		}
	}
}

func TestSendPostsWithAuthAndIdempotencyKey(t *testing.T) {
	var gotAuth, gotCT, gotKey, gotCustom string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotKey = r.Header.Get("X-DC-Idempotency-Key")
		gotCustom = r.Header.Get("X-Custom")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := Send(context.Background(), srv.Client(), Request{
		URL:            srv.URL,
		Headers:        map[string]string{"X-Custom": "ok"},
		Body:           []byte(`{"a":1}`),
		Secret:         "s3cr3t",
		IdempotencyKey: "idem-1",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotKey != "idem-1" {
		t.Errorf("idempotency key = %q", gotKey)
	}
	if gotCustom != "ok" {
		t.Errorf("custom header = %q", gotCustom)
	}
	if string(gotBody) != `{"a":1}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestSendDropsReservedHeaders(t *testing.T) {
	var gotAuth, gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-DC-Tenant")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A caller-supplied Authorization / X-DC-* must never reach the wire; the secret
	// still populates Authorization.
	err := Send(context.Background(), srv.Client(), Request{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer forged", "X-DC-Tenant": "victim"},
		Secret:  "real",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotAuth != "Bearer real" {
		t.Errorf("Authorization = %q, want the secret (forged config dropped)", gotAuth)
	}
	if gotTenant != "" {
		t.Errorf("X-DC-Tenant should be dropped, got %q", gotTenant)
	}
}

func TestSendSuppressesBodyOnSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "leaked-secret-echo", http.StatusInternalServerError)
	}))
	defer srv.Close()

	// With a secret, the response body is never surfaced (it could reflect the auth header).
	err := Send(context.Background(), srv.Client(), Request{URL: srv.URL, Secret: "s"})
	if err == nil {
		t.Fatal("expected an error on 500")
	}
	if strings.Contains(err.Error(), "leaked-secret-echo") {
		t.Fatalf("error leaked the response body: %v", err)
	}

	// Without a secret, the body snippet is included for diagnostics.
	err = Send(context.Background(), srv.Client(), Request{URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "leaked-secret-echo") {
		t.Fatalf("non-secret error should include the body snippet: %v", err)
	}
}

func TestSendRejectsNonHTTPURL(t *testing.T) {
	if err := Send(context.Background(), nil, Request{URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("expected a scheme-validation error")
	}
}

// A caller-supplied client that does NOT set a no-redirect policy must still not
// follow a 3xx onto another target — Send forces the policy on.
func TestSendForcesNoRedirectOnCallerClient(t *testing.T) {
	var internalHit bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// A plain client follows redirects by default; Send must override that.
	err := Send(context.Background(), &http.Client{}, Request{URL: redirector.URL})
	if err == nil {
		t.Fatal("expected the 302 to surface as a non-2xx error, not be followed")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Fatalf("error should report the 302, got %v", err)
	}
	if internalHit {
		t.Fatal("Send followed the redirect onto the internal target (SSRF guard defeated)")
	}
}

// A URL-embedded credential is rejected outright, and the rejection error never echoes the
// password (nor does the defense-in-depth redaction on the transport path).
func TestSendRejectsAndRedactsURLCredentials(t *testing.T) {
	err := Send(context.Background(), nil, Request{URL: "https://user:sup3rsecret@127.0.0.1:1/x"})
	if err == nil {
		t.Fatal("expected a userinfo URL to be rejected")
	}
	if strings.Contains(err.Error(), "sup3rsecret") {
		t.Fatalf("error leaked the URL password: %v", err)
	}
}
