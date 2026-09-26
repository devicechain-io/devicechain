// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// jwksServer is a real HTTP JWKS endpoint whose served key set a test can swap,
// standing in for user-management across a signing-key rotation. It counts every
// fetch, so a test can assert how many the validator made.
type jwksServer struct {
	*httptest.Server
	fetches atomic.Int64
	delay   time.Duration // held before answering, so concurrent callers overlap one fetch

	mu  sync.Mutex
	doc []byte
}

func newJWKSServer(t *testing.T, pubs ...*rsa.PublicKey) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.serve(t, pubs...)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		s.mu.Lock()
		doc := s.doc
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	}))
	t.Cleanup(s.Close)
	return s
}

// serve replaces the key set the endpoint answers with.
func (s *jwksServer) serve(t *testing.T, pubs ...*rsa.PublicKey) {
	t.Helper()
	doc, err := BuildJWKS(pubs)
	if err != nil {
		t.Fatalf("BuildJWKS: %v", err)
	}
	s.mu.Lock()
	s.doc = doc
	s.mu.Unlock()
}

// validatorFor builds the validator every service builds — the real constructor,
// over the real HTTP fetch, with the production refresh policy.
func validatorFor(t *testing.T, s *jwksServer) *Validator {
	t.Helper()
	v, err := NewValidatorFromJWKSURL(context.Background(), s.URL, 1, 0)
	if err != nil {
		t.Fatalf("NewValidatorFromJWKSURL: %v", err)
	}
	return v
}

func accessTokenFrom(t *testing.T, key *rsa.PrivateKey, user string) string {
	t.Helper()
	tok, err := NewIssuer(key, "test", time.Minute, time.Hour).IssueAccess("tenant-a", user, nil, nil, "jti-"+user)
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	return tok.Token
}

// The rolling-upgrade rotation. The new signing key is minted and a token is signed
// with it while the endpoint the validator refreshes from still serves the OLD set
// (the old user-management pod has not been replaced yet). That refresh cannot find
// the kid, and it must not lock the new kid out for long once the new set is served:
// the token has to validate within a couple of seconds, not after a 30s throttle.
func TestRotation_NewKidValidatesSoonAfterARefreshThatMissedIt(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	srv := newJWKSServer(t, &oldKey.PublicKey)
	v := validatorFor(t, srv)
	tok := accessTokenFrom(t, newKey, "rotated")

	// The refresh this triggers is answered with the old set only.
	_, err := v.Validate(tok)
	if err == nil || !strings.Contains(err.Error(), "no verification key for kid") {
		t.Fatalf("first validation against the old set: err = %v, want a missing-kid refusal", err)
	}
	if got := srv.fetches.Load(); got != 2 {
		t.Fatalf("JWKS fetches after startup + one missed refresh = %d, want 2", got)
	}

	// The new pod takes over.
	srv.serve(t, &newKey.PublicKey)

	deadline := time.Now().Add(3 * time.Second)
	var claims *Claims
	for {
		claims, err = v.Validate(tok)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("token signed by the rotated-in key still refused 3s after the new set was served "+
			"(%d JWKS fetches): %v", srv.fetches.Load(), err)
	}
	if claims.Username != "rotated" || claims.Tenant != "tenant-a" {
		t.Fatalf("claims = %+v, want username rotated in tenant-a", claims)
	}
}

// Requests that arrive while a refresh is already in flight share its result rather
// than being refused: every one of them validates, over exactly one fetch.
func TestRotation_ConcurrentCallersShareOneInFlightRefresh(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	srv := newJWKSServer(t, &oldKey.PublicKey)
	v := validatorFor(t, srv)

	srv.serve(t, &newKey.PublicKey)
	srv.delay = 200 * time.Millisecond
	tok := accessTokenFrom(t, newKey, "concurrent")

	const callers = 32
	var wg sync.WaitGroup
	var ok atomic.Int64
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if c, err := v.Validate(tok); err == nil && c.Username == "concurrent" {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := ok.Load(); got != callers {
		t.Fatalf("%d of %d concurrent callers validated a token signed by the rotated-in key, want all", got, callers)
	}
	if got := srv.fetches.Load(); got != 2 {
		t.Fatalf("JWKS fetches = %d, want 2 (startup + ONE shared refresh)", got)
	}
}

// A flood of tokens bearing kids the endpoint will never serve stays a trickle at the
// endpoint: at most one refetch per jwksRefreshInterval, measured as a count, and the
// refresh still RESUMES after each interval rather than stopping for good.
func TestRotation_ForgedKidFloodIsBoundedFetchesPerSecond(t *testing.T) {
	realKey := mustKey(t)
	srv := newJWKSServer(t, &realKey.PublicKey)
	v := validatorFor(t, srv)

	const window = 2500 * time.Millisecond
	stop := time.Now().Add(window)
	var wg sync.WaitGroup
	var attempts, accepted atomic.Int64
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			forger := mustKey(t) // a fresh key per goroutine: a kid nobody serves
			tok := accessTokenFrom(t, forger, "forged")
			for time.Now().Before(stop) {
				attempts.Add(1)
				if _, err := v.Validate(tok); err == nil {
					accepted.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if accepted.Load() != 0 {
		t.Fatalf("%d forged tokens validated", accepted.Load())
	}
	refetches := srv.fetches.Load() - 1 // minus the startup fetch
	// The interval bounds the rate: over the window, one at the start plus one per
	// elapsed interval. It must also be ≥ 2, or the refresh stopped after the first.
	maxRefetches := int64(window/jwksRefreshInterval) + 1
	if refetches < 2 || refetches > maxRefetches {
		t.Fatalf("%d forged validations over %v caused %d JWKS refetches, want between 2 and %d "+
			"(one per %v)", attempts.Load(), window, refetches, maxRefetches, jwksRefreshInterval)
	}
	if attempts.Load() < 100 {
		t.Fatalf("only %d forged validations ran; the flood did not flood", attempts.Load())
	}
}
