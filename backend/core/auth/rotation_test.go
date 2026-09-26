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
	// failure, when set, is how the endpoint answers instead of serving the set:
	// failWith500 or failByClosing. It stands in for an old user-management pod that
	// is terminating while a validator refetches from it.
	failure atomic.Int32

	mu       sync.Mutex
	doc      []byte
	arrivals []time.Time // when each fetch reached the endpoint
}

const (
	failNone int32 = iota
	failWith500
	failByClosing
)

func newJWKSServer(t *testing.T, pubs ...*rsa.PublicKey) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	s.serve(t, pubs...)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		s.arrivals = append(s.arrivals, time.Now())
		s.mu.Unlock()
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		switch s.failure.Load() {
		case failWith500:
			http.Error(w, "terminating", http.StatusInternalServerError)
			return
		case failByClosing:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
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
	logs := logSink.Capture(t)

	// The refresh this triggers is answered with the old set only.
	_, err := v.Validate(tok)
	if err == nil || !strings.Contains(err.Error(), "no verification key for kid") {
		t.Fatalf("first validation against the old set: err = %v, want a missing-kid refusal", err)
	}
	if got := srv.fetches.Load(); got != 2 {
		t.Fatalf("JWKS fetches after startup + one missed refresh = %d, want 2", got)
	}
	// The operator's diagnostic for exactly this refusal, quoted verbatim in the
	// release notes of both docs locales, so its wording is pinned here: it has to
	// name the kid that was missing.
	assertMissedKidLogged(t, logs.String(), Thumbprint(&newKey.PublicKey))

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
	// A caller left waiting on a refresh that never releases it is a hang, and a hang
	// must fail THIS test by name rather than time out the package.
	waitOrFail(t, 10*time.Second, "concurrent callers of one in-flight refresh", wg.Wait)

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

// missedKidLog is the line a refetch that does not yield the requested kid logs. The
// release notes quote it to operators as the way to tell a refusal during a key
// change from a bad token, so a change to it is a change to published docs.
const missedKidLog = "JWKS refetched on an unknown kid, and the fetched set does not hold it."

func assertMissedKidLogged(t *testing.T, logs, kid string) {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, missedKidLog) && strings.Contains(line, kid) {
			return
		}
	}
	t.Fatalf("no log line %q naming kid %s; captured:\n%s", missedKidLog, kid, logs)
}

// waitOrFail runs wait and fails t if it has not returned within d.
func waitOrFail(t *testing.T, d time.Duration, what string, wait func()) {
	t.Helper()
	finished := make(chan struct{})
	go func() {
		wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(d):
		t.Fatalf("%s: still blocked after %v", what, d)
	}
}

// The other half of a rolling upgrade: the refetch reaches an old user-management pod
// that is going away, and fails. A failed refetch must leave the key set as it was.
// Were it cleared, every token would be refused — the ones signed by a key the
// validator already held included — until some later fetch succeeded, so a rotation
// that the old pod happened to answer would lock out the whole platform, not just
// the new key. Once the endpoint recovers, the new kid is trusted on the next refetch
// the interval allows.
func TestRotation_FailedRefetchKeepsTheKeysItHad(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure int32
		// endpoint requests the one failed refetch makes. A GET whose reused
		// keep-alive connection is closed under it is retried once by net/http
		// itself, so that case reaches the endpoint twice for one refetch.
		refetchRequests int64
	}{
		{"answered with 500", failWith500, 1},
		{"connection closed", failByClosing, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldKey, newKey := mustKey(t), mustKey(t)
			srv := newJWKSServer(t, &oldKey.PublicKey)
			v := validatorFor(t, srv)
			oldTok := accessTokenFrom(t, oldKey, "held")
			newTok := accessTokenFrom(t, newKey, "rotated")

			srv.failure.Store(tc.failure)
			logs := logSink.Capture(t)
			if _, err := v.Validate(newTok); err == nil {
				t.Fatal("a token for a kid the failed refetch never served validated")
			}
			if got, want := srv.fetches.Load(), 1+tc.refetchRequests; got != want {
				t.Fatalf("JWKS requests after startup + one failed refetch = %d, want %d", got, want)
			}
			// Positive half first, so an empty capture cannot pass the negative one.
			if !strings.Contains(logs.String(), "JWKS refresh on unknown kid failed; keeping existing keys.") {
				t.Fatalf("the failed refetch was not logged; captured:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), missedKidLog) {
				t.Fatalf("a FAILED refetch was reported as one that fetched a set without the kid:\n%s", logs.String())
			}

			// Inside the interval, so no refetch can repair a cleared set: only the
			// keys the validator kept can answer this.
			claims, err := v.Validate(oldTok)
			if err != nil {
				t.Fatalf("token signed by a key the validator already held refused after a failed refetch: %v", err)
			}
			if claims.Username != "held" {
				t.Fatalf("claims.Username = %q, want held", claims.Username)
			}

			// The endpoint recovers, publishing both keys as a user-management that has
			// rotated does.
			srv.failure.Store(failNone)
			srv.serve(t, &oldKey.PublicKey, &newKey.PublicKey)
			time.Sleep(jwksRefreshInterval + 100*time.Millisecond)

			claims, err = v.Validate(newTok)
			if err != nil {
				t.Fatalf("rotated-in token still refused after the endpoint recovered and %v passed: %v",
					jwksRefreshInterval, err)
			}
			if claims.Username != "rotated" {
				t.Fatalf("claims.Username = %q, want rotated", claims.Username)
			}
			if _, err := v.Validate(oldTok); err != nil {
				t.Fatalf("token signed by the retained key refused after the recovery refetch: %v", err)
			}
		})
	}
}

// The interval runs from the END of a refetch, not its start. Measured from the start,
// a refetch slower than the interval would be followed by the next one the instant it
// returned, and a forged-kid flood against a slow endpoint would keep it permanently
// busy with back-to-back fetches — the moment it is least able to afford them.
func TestRotation_RefetchIntervalRunsFromCompletion(t *testing.T) {
	realKey := mustKey(t)
	srv := newJWKSServer(t, &realKey.PublicKey)
	v := validatorFor(t, srv)
	const delay = 1500 * time.Millisecond // longer than the interval
	srv.delay = delay

	const window = 5500 * time.Millisecond
	stop := time.Now().Add(window)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := accessTokenFrom(t, mustKey(t), "forged")
			for time.Now().Before(stop) {
				_, _ = v.Validate(tok)
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	waitOrFail(t, window+2*jwksRequestTimeout, "forged-kid flood against a slow endpoint", wg.Wait)

	srv.mu.Lock()
	refetches := append([]time.Time(nil), srv.arrivals[1:]...) // minus the startup fetch
	srv.mu.Unlock()
	if len(refetches) < 2 {
		t.Fatalf("%d refetches over %v, want at least 2 to measure a gap", len(refetches), window)
	}
	// Each refetch starts no sooner than delay (the previous one running) plus the
	// interval (the pause after it finished); a little slack for the scheduler.
	const slack = 100 * time.Millisecond
	want := delay + jwksRefreshInterval - slack
	for i := 1; i < len(refetches); i++ {
		if gap := refetches[i].Sub(refetches[i-1]); gap < want {
			t.Fatalf("refetch %d started %v after refetch %d, want ≥ %v (a %v fetch, then the %v interval)",
				i+1, gap, i, want, delay, jwksRefreshInterval)
		}
	}
}

// A refresh that panics must still release the refetch it claimed. The claimant's
// panic is its own caller's problem; what must not follow is every later unknown kid
// waiting forever on a refetch nobody is running.
func TestRotation_PanickingRefreshDoesNotWedgeLaterCallers(t *testing.T) {
	key := mustKey(t)
	var calls atomic.Int64
	v := NewRefreshingValidator(map[string]*rsa.PublicKey{}, func() (map[string]*rsa.PublicKey, error) {
		if calls.Add(1) == 1 {
			panic("refresh blew up")
		}
		return map[string]*rsa.PublicKey{Thumbprint(&key.PublicKey): &key.PublicKey}, nil
	}, 0)
	tok := accessTokenFrom(t, key, "after-panic")

	var recovered any
	waitOrFail(t, 5*time.Second, "the call whose refresh panicked", func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { recovered = recover() }()
			_, _ = v.Validate(tok)
		}()
		<-done
	})
	if recovered != "refresh blew up" {
		t.Fatalf("recovered %v, want the refresh's own panic", recovered)
	}

	var claims *Claims
	var err error
	waitOrFail(t, 5*time.Second, "the next unknown-kid call after a panicking refresh", func() {
		claims, err = v.Validate(tok)
	})
	if err != nil {
		t.Fatalf("validation after a panicking refresh: %v", err)
	}
	if claims.Username != "after-panic" || calls.Load() != 2 {
		t.Fatalf("claims.Username = %q after %d refreshes, want after-panic after 2", claims.Username, calls.Load())
	}
}
