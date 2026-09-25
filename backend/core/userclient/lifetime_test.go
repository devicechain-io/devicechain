// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func expiresIn(d time.Duration) string { return time.Now().Add(d).Format(time.RFC3339) }

// A cached token that would expire before the caller is done with it is renewed once, and
// the renewed token is the one handed back.
func TestAccessTokenValidForRenewsATokenThatWouldExpireTooSoon(t *testing.T) {
	stub := &authStub{selectExp: expiresIn(5 * time.Minute)}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()

	first, err := s.AccessToken(ctx) // access-1, five minutes left: fine for one request
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	got, err := s.AccessTokenValidFor(ctx, 10*time.Minute)
	if err != nil {
		t.Fatalf("a renewal to a token with an hour left must satisfy ten minutes: %v", err)
	}
	if got == first {
		t.Fatalf("the five-minute token %q was handed back for a ten-minute need", got)
	}
	if stub.refreshes != 1 || stub.logins != 1 {
		t.Fatalf("expected exactly one renewal by refresh, got refreshes=%d logins=%d", stub.refreshes, stub.logins)
	}
}

// A cached token with enough time left is handed back with no exchange at all.
func TestAccessTokenValidForKeepsATokenThatLivesLongEnough(t *testing.T) {
	stub := &authStub{selectExp: expiresIn(5 * time.Minute)}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()

	first, err := s.AccessToken(ctx)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	got, err := s.AccessTokenValidFor(ctx, time.Minute)
	if err != nil {
		t.Fatalf("a five-minute token must satisfy one minute: %v", err)
	}
	if got != first {
		t.Fatalf("a token with enough time left was renewed: %q != %q", got, first)
	}
	if stub.refreshes != 0 || stub.logins != 1 {
		t.Fatalf("expected no exchange beyond the first sign-in, got refreshes=%d logins=%d", stub.refreshes, stub.logins)
	}
}

// When the server issues tokens that live shorter than the need, even a fresh one is not
// handed back: the caller gets ErrTokenLifetimeTooShort naming both durations, after ONE
// renewal rather than a loop of them.
func TestAccessTokenValidForRefusesWhenTheServerLifetimeIsShorter(t *testing.T) {
	stub := &authStub{selectExp: expiresIn(15 * time.Minute), refreshExp: expiresIn(15 * time.Minute)}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()

	if _, err := s.AccessToken(ctx); err != nil {
		t.Fatalf("first: %v", err)
	}
	tok, err := s.AccessTokenValidFor(ctx, 20*time.Minute)
	if !errors.Is(err, ErrTokenLifetimeTooShort) {
		t.Fatalf("want ErrTokenLifetimeTooShort, got token %q and error %v", tok, err)
	}
	if tok != "" {
		t.Fatalf("a token known to expire early was handed back: %q", tok)
	}
	if !strings.Contains(err.Error(), "20m0s") {
		t.Fatalf("the error does not name the 20m0s that was needed: %v", err)
	}
	// 14m-something or 15m0s, depending on how far into its second the token was issued.
	if !strings.Contains(err.Error(), "14m") && !strings.Contains(err.Error(), "15m") {
		t.Fatalf("the error does not name the fresh token's lifetime (about 15m): %v", err)
	}
	if stub.refreshes != 1 {
		t.Fatalf("expected exactly one renewal before refusing, got %d", stub.refreshes)
	}
}

// Concurrent callers needing a long-lived token share ONE renewal: two exchanges would
// spend the same refresh token, and the one that lost the server's single-use claim would
// fall back to a full sign-in.
func TestConcurrentAccessTokenValidForShareOneRenewal(t *testing.T) {
	stub := &authStub{selectExp: expiresIn(5 * time.Minute)}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	s := NewTenantSession(nil, srv.URL, "u@x", "pw", "acme")
	ctx := context.Background()
	if _, err := s.AccessToken(ctx); err != nil {
		t.Fatalf("first: %v", err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AccessTokenValidFor(ctx, 10*time.Minute); err != nil {
				t.Errorf("concurrent renewal: %v", err)
			}
		}()
	}
	wg.Wait()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.refreshes != 1 || stub.logins != 1 {
		t.Fatalf("concurrent callers ran more than one exchange: refreshes=%d logins=%d", stub.refreshes, stub.logins)
	}
}
