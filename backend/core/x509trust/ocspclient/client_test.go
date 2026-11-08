// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ocspclient

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

var base = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type node struct {
	cert *x509.Certificate
	key  crypto.Signer
}

var nextSerial atomic.Int64

func mk(t testing.TB, parent *node, cn string, ca bool, aia ...string) *node {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(nextSerial.Add(1) + 1000), Subject: pkix.Name{CommonName: cn},
		NotBefore: base.Add(-24 * time.Hour), NotAfter: base.Add(240 * time.Hour),
		IsCA: ca, BasicConstraintsValid: true, OCSPServer: aia,
	}
	if ca {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	}
	signer, issuer := crypto.Signer(key), tmpl
	if parent != nil {
		signer, issuer = parent.key, parent.cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, key.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &node{c, key}
}

// responder is an OCSP responder for certs issued by signer. status/validity are per test.
type responder struct {
	*httptest.Server
	hits    atomic.Int64
	issuer  *node
	status  atomic.Int32 // ocsp.Good / Revoked / Unknown
	next    time.Duration
	delay   time.Duration
	mangle  func(w http.ResponseWriter, body []byte) bool // true = handled
	serial  *big.Int                                      // answer for this serial instead (wrong-serial test)
	clk     *clock
	revoked time.Time
}

func newResponder(t *testing.T, clk *clock, issuer *node) *responder {
	r := &responder{issuer: issuer, next: 2 * time.Hour, clk: clk}
	r.status.Store(int32(ocsp.Good))
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		body, _ := io.ReadAll(req.Body)
		if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/ocsp-request" {
			http.Error(w, "bad request", 400)
			return
		}
		if r.delay > 0 {
			select {
			case <-time.After(r.delay):
			case <-req.Context().Done():
				return
			}
		}
		if r.mangle != nil && r.mangle(w, body) {
			return
		}
		parsed, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		serial := parsed.SerialNumber
		if r.serial != nil {
			serial = r.serial
		}
		tmpl := ocsp.Response{
			Status: int(r.status.Load()), SerialNumber: serial,
			ThisUpdate: clk.now().Add(-time.Minute), NextUpdate: clk.now().Add(r.next),
		}
		if tmpl.Status == ocsp.Revoked {
			tmpl.RevokedAt = clk.now().Add(-time.Hour)
			tmpl.RevocationReason = ocsp.KeyCompromise
		}
		der, err := ocsp.CreateResponse(issuer.cert, issuer.cert, tmpl, issuer.key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/ocsp-response")
		_, _ = w.Write(der)
	}))
	t.Cleanup(r.Close)
	return r
}

func newClient(clk *clock, cfg Config, hooks Hooks) *Client {
	cfg.Now = clk.now
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 6 * time.Hour
	}
	if cfg.MaxInFlight == 0 {
		cfg.MaxInFlight = 32
	}
	if cfg.FetchDeadline == 0 {
		cfg.FetchDeadline = 2 * time.Second
	}
	if cfg.CacheEntries == 0 {
		cfg.CacheEntries = 100
	}
	if cfg.MaxHostsPerTenant == 0 {
		cfg.MaxHostsPerTenant = 16
	}
	plain := NewHTTPClient(http.DefaultTransport)
	return New(cfg, plain, plain, hooks)
}

func req(tenant string, targets ...Target) Request {
	return Request{Tenant: tenant, Anchor: "1", Targets: targets}
}

func tgt(cert, issuer *node) Target { return Target{Cert: cert.cert, Issuer: issuer.cert} }

func TestGoodIsFetchedOnceAndCachedUntilItsBound(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	leaf := mk(t, root, "leaf", false, srv.URL)
	var hits, misses atomic.Int64
	c := newClient(clk, Config{}, Hooks{Cache: func(hit bool) {
		if hit {
			hits.Add(1)
		} else {
			misses.Add(1)
		}
	}})
	out := c.Check(context.Background(), req("t1", tgt(leaf, root)))
	if out.Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
		t.Fatalf("not good: %+v", out)
	}
	for i := 0; i < 3; i++ {
		if c.Check(context.Background(), req("t1", tgt(leaf, root))).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
			t.Fatal("cached answer lost")
		}
	}
	if srv.hits.Load() != 1 || hits.Load() != 3 || misses.Load() != 1 {
		t.Fatalf("hits=%d cacheHits=%d misses=%d", srv.hits.Load(), hits.Load(), misses.Load())
	}
	// Past nextUpdate the cached answer is not served: it is fetched again.
	clk.advance(2*time.Hour + time.Minute)
	if c.Check(context.Background(), req("t1", tgt(leaf, root))).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
		t.Fatal("not good after refresh")
	}
	if srv.hits.Load() != 2 {
		t.Fatalf("an answer past nextUpdate was served from cache (hits=%d)", srv.hits.Load())
	}
}

func TestRevokedIsReturnedAndNeverCached(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.status.Store(int32(ocsp.Revoked))
	leaf := mk(t, root, "leaf", false, srv.URL)
	c := newClient(clk, Config{}, Hooks{})
	for i := 1; i <= 2; i++ {
		out := c.Check(context.Background(), req("t1", tgt(leaf, root)))
		if out.Results[tgt(leaf, root).Key()] != x509trust.OCSPRevoked || out.Revoked[tgt(leaf, root).Key()].Reason != x509trust.RevocationKeyCompromise {
			t.Fatalf("round %d: %+v", i, out)
		}
		if srv.hits.Load() != int64(i) {
			t.Fatalf("round %d: a revoked answer was cached (hits=%d)", i, srv.hits.Load())
		}
	}
	// And a later good answer replaces it at once (an operator un-revoke is the store's job; the
	// responder's current word is what is asked).
	srv.status.Store(int32(ocsp.Good))
	if c.Check(context.Background(), req("t1", tgt(leaf, root))).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
		t.Fatal("good after revoked")
	}
}

// Leaf = timeout, intermediate = revoked: the revoked answer must be in the outcome (the
// aggregate is gathered under one deadline, not short-circuited by the leaf's failure).
func TestRevokedIntermediateSurvivesALeafTimeout(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	rootSrv := newResponder(t, clk, root)
	rootSrv.status.Store(int32(ocsp.Revoked))
	inter := mk(t, root, "inter", true, rootSrv.URL)
	leafSrv := newResponder(t, clk, inter)
	leafSrv.delay = 5 * time.Second
	leaf := mk(t, inter, "leaf", false, leafSrv.URL)

	c := newClient(clk, Config{FetchDeadline: 300 * time.Millisecond}, Hooks{})
	start := time.Now()
	out := c.Check(context.Background(), req("t1", tgt(leaf, inter), tgt(inter, root)))
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the connect waited %v for OCSP", took)
	}
	if out.Results[tgt(inter, root).Key()] != x509trust.OCSPRevoked {
		t.Fatalf("the intermediate's revocation was lost: %+v", out)
	}
	if out.Reasons[tgt(leaf, inter).Key()] != ReasonTimeout {
		t.Fatalf("leaf reason = %q", out.Reasons[tgt(leaf, inter).Key()])
	}
}

func TestDeadlineBoundsASlowResponder(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.delay = 3 * time.Second
	leaf := mk(t, root, "leaf", false, srv.URL)
	c := newClient(clk, Config{FetchDeadline: 150 * time.Millisecond}, Hooks{})
	start := time.Now()
	out := c.Check(context.Background(), req("t1", tgt(leaf, root)))
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v with a 150ms deadline", took)
	}
	if out.Reasons[tgt(leaf, root).Key()] != ReasonTimeout {
		t.Fatalf("reason = %q", out.Reasons[tgt(leaf, root).Key()])
	}
}

func TestSaturationIsImmediateAndNeverQueues(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.delay = 400 * time.Millisecond
	a := mk(t, root, "a", false, srv.URL)
	b := mk(t, root, "b", false, srv.URL)
	c := newClient(clk, Config{MaxInFlight: 1, FetchDeadline: 3 * time.Second}, Hooks{})
	done := make(chan Outcome, 1)
	go func() { done <- c.Check(context.Background(), req("t1", tgt(a, root))) }()
	for i := 0; i < 200 && srv.hits.Load() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	out := c.Check(context.Background(), req("t1", tgt(b, root)))
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("the second connect queued for %v", took)
	}
	if out.Reasons[tgt(b, root).Key()] != ReasonSaturated {
		t.Fatalf("reason = %q", out.Reasons[tgt(b, root).Key()])
	}
	if first := <-done; first.Results[tgt(a, root).Key()] != x509trust.OCSPGood {
		t.Fatalf("the first fetch should finish: %+v", first)
	}
}

func TestTenantShareIsolatesTenants(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.delay = 500 * time.Millisecond
	c := newClient(clk, Config{MaxInFlight: 16, FetchDeadline: 3 * time.Second}, Hooks{}) // share = max(4, 16/4) = 4
	var wg sync.WaitGroup
	var sat atomic.Int64
	for i := 0; i < 8; i++ {
		leaf := mk(t, root, "a", false, srv.URL)
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := c.Check(context.Background(), req("tenant-a", tgt(leaf, root)))
			if out.Reasons[tgt(leaf, root).Key()] == ReasonSaturated {
				sat.Add(1)
			}
		}()
	}
	for i := 0; i < 200 && srv.hits.Load() < 4; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	other := mk(t, root, "b", false, srv.URL)
	out := c.Check(context.Background(), req("tenant-b", tgt(other, root)))
	wg.Wait()
	if out.Results[tgt(other, root).Key()] != x509trust.OCSPGood {
		t.Fatalf("tenant B was starved by tenant A: %+v", out)
	}
	if sat.Load() != 4 {
		t.Fatalf("tenant A saturated %d of 8, want exactly its overflow of 4", sat.Load())
	}
}

func TestCacheIsPerTenantAndAnchor(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	leaf := mk(t, root, "leaf", false, srv.URL)
	c := newClient(clk, Config{}, Hooks{})
	c.Check(context.Background(), req("t1", tgt(leaf, root)))
	c.Check(context.Background(), req("t2", tgt(leaf, root)))
	other := req("t1", tgt(leaf, root))
	other.Anchor = "2"
	c.Check(context.Background(), other)
	if srv.hits.Load() != 3 {
		t.Fatalf("an answer crossed a tenant or anchor boundary (hits=%d, want 3)", srv.hits.Load())
	}
}

func TestSingleflight(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.delay = 150 * time.Millisecond
	leaf := mk(t, root, "leaf", false, srv.URL)
	c := newClient(clk, Config{}, Hooks{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Check(context.Background(), req("t1", tgt(leaf, root))).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
				t.Error("not good")
			}
		}()
	}
	wg.Wait()
	if srv.hits.Load() != 1 {
		t.Fatalf("a reconnect storm of one certificate made %d fetches", srv.hits.Load())
	}
}

func TestBreakerOpensOnEndpointFailuresAndProbesOnce(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	var failing atomic.Bool
	failing.Store(true)
	srv.mangle = func(w http.ResponseWriter, _ []byte) bool {
		if failing.Load() {
			http.Error(w, "down", 503)
			return true
		}
		return false
	}
	var opened atomic.Int64
	c := newClient(clk, Config{FailureTTL: 30 * time.Second}, Hooks{BreakerOpened: func() { opened.Add(1) }})
	check := func(i int) Reason {
		leaf := mk(t, root, "leaf", false, srv.URL)
		return c.Check(context.Background(), req("t1", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]
	}
	for i := 0; i < 3; i++ {
		if r := check(i); r != ReasonBadResponse {
			t.Fatalf("failure %d reason %q", i, r)
		}
	}
	hits := srv.hits.Load()
	if r := check(3); r != ReasonBreakerOpen || srv.hits.Load() != hits || opened.Load() != 1 {
		t.Fatalf("breaker did not hold: reason %q hits %d->%d opened %d", r, hits, srv.hits.Load(), opened.Load())
	}
	// another tenant's breaker is its own
	leaf := mk(t, root, "leaf", false, srv.URL)
	if r := c.Check(context.Background(), req("t2", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]; r != ReasonBadResponse {
		t.Fatalf("tenant t2 inherited t1's breaker: %q", r)
	}
	// after the TTL exactly one probe goes out; it fails and re-opens.
	clk.advance(31 * time.Second)
	hits = srv.hits.Load()
	if r := check(4); r != ReasonBadResponse || srv.hits.Load() != hits+1 {
		t.Fatalf("probe: %q hits %d->%d", r, hits, srv.hits.Load())
	}
	if r := check(5); r != ReasonBreakerOpen {
		t.Fatalf("after a failed probe: %q", r)
	}
	// the responder recovers: the next probe closes the breaker.
	failing.Store(false)
	clk.advance(31 * time.Second)
	leaf = mk(t, root, "leaf", false, srv.URL)
	if c.Check(context.Background(), req("t1", tgt(leaf, root))).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
		t.Fatal("probe did not close the breaker")
	}
}

// A certificate-level failure (the responder answers, but not usefully for this certificate) must
// not open the responder's breaker, and is remembered for that certificate only.
func TestCertificateLevelFailureNeverOpensTheBreaker(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.serial = big.NewInt(999999) // a well-signed answer for a different serial
	c := newClient(clk, Config{}, Hooks{})
	for i := 0; i < 6; i++ {
		leaf := mk(t, root, "leaf", false, srv.URL)
		if r := c.Check(context.Background(), req("t1", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]; r != ReasonUnsigned {
			t.Fatalf("attempt %d reason %q", i, r)
		}
	}
	if srv.hits.Load() != 6 {
		t.Fatalf("breaker opened on certificate-level failures (hits=%d)", srv.hits.Load())
	}
	// the same certificate again is the negative entry, not a fetch.
	leaf := mk(t, root, "leaf", false, srv.URL)
	c.Check(context.Background(), req("t1", tgt(leaf, root)))
	c.Check(context.Background(), req("t1", tgt(leaf, root)))
	if srv.hits.Load() != 7 {
		t.Fatalf("a failed certificate was refetched (hits=%d)", srv.hits.Load())
	}
	clk.advance(certFailureTTL + time.Second)
	c.Check(context.Background(), req("t1", tgt(leaf, root)))
	if srv.hits.Load() != 8 {
		t.Fatalf("the negative entry did not expire (hits=%d)", srv.hits.Load())
	}
}

func TestUnknownStatusIsNotGood(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.status.Store(int32(ocsp.Unknown))
	leaf := mk(t, root, "leaf", false, srv.URL)
	c := newClient(clk, Config{}, Hooks{})
	out := c.Check(context.Background(), req("t1", tgt(leaf, root)))
	if _, answered := out.Results[tgt(leaf, root).Key()]; answered || out.Reasons[tgt(leaf, root).Key()] != ReasonUnknown {
		t.Fatalf("%+v", out)
	}
}

func TestBadEndpointResponses(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	other := newResponder(t, clk, root) // a redirect target that must never be reached
	for name, mangle := range map[string]func(http.ResponseWriter, []byte) bool{
		"oversize body": func(w http.ResponseWriter, _ []byte) bool {
			w.Header().Set("Content-Type", "application/ocsp-response")
			_, _ = w.Write(make([]byte, MaxResponseBytes+10))
			return true
		},
		"wrong content type": func(w http.ResponseWriter, _ []byte) bool {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("hello"))
			return true
		},
		"redirect": func(w http.ResponseWriter, r []byte) bool {
			w.Header().Set("Location", other.URL)
			w.WriteHeader(http.StatusFound)
			return true
		},
		"empty 200": func(w http.ResponseWriter, _ []byte) bool {
			w.Header().Set("Content-Type", "application/ocsp-response")
			return true
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newResponder(t, clk, root)
			srv.mangle = mangle
			leaf := mk(t, root, "leaf", false, srv.URL)
			c := newClient(clk, Config{}, Hooks{})
			out := c.Check(context.Background(), req("t1", tgt(leaf, root)))
			if r := out.Reasons[tgt(leaf, root).Key()]; r != ReasonBadResponse {
				t.Fatalf("reason %q", r)
			}
		})
	}
	if other.hits.Load() != 0 {
		t.Fatalf("a redirect was followed (%d hits at the target)", other.hits.Load())
	}
}

func TestOverrideURLWinsAndAIAIsIgnored(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	aiaSrv := newResponder(t, clk, root)
	overrideSrv := newResponder(t, clk, root)
	leaf := mk(t, root, "leaf", false, aiaSrv.URL)
	c := newClient(clk, Config{}, Hooks{})
	r := req("t1", tgt(leaf, root))
	r.OverrideURL = overrideSrv.URL
	if c.Check(context.Background(), r).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
		t.Fatal("not good")
	}
	if overrideSrv.hits.Load() != 1 || aiaSrv.hits.Load() != 0 {
		t.Fatalf("override %d aia %d", overrideSrv.hits.Load(), aiaSrv.hits.Load())
	}
}

func TestNoResponder(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	leaf := mk(t, root, "leaf", false) // no AIA
	c := newClient(clk, Config{}, Hooks{})
	if r := c.Check(context.Background(), req("t1", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]; r != ReasonNoResponder {
		t.Fatalf("reason %q", r)
	}
	bad := mk(t, root, "bad", false, "ldap://x/y")
	if r := c.Check(context.Background(), req("t1", tgt(bad, root))).Reasons[tgt(bad, root).Key()]; r != ReasonNoResponder {
		t.Fatalf("reason %q", r)
	}
	creds := mk(t, root, "creds", false, "http://user:pw@127.0.0.1:1/")
	if r := c.Check(context.Background(), req("t1", tgt(creds, root))).Reasons[tgt(creds, root).Key()]; r != ReasonNoResponder {
		t.Fatalf("userinfo in a responder URL was accepted: %q", r)
	}
}

func TestCacheOnlyNeverFetches(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	leaf := mk(t, root, "leaf", false, srv.URL)
	c := newClient(clk, Config{FetchDeadline: 1}, Hooks{})
	c.cfg.FetchDeadline = 0
	if r := c.Check(context.Background(), req("t1", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]; r != ReasonCacheOnly {
		t.Fatalf("reason %q", r)
	}
	if srv.hits.Load() != 0 {
		t.Fatal("fetched in cache-only mode")
	}
}

func TestHostBudgetBoundsAIAOnly(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	s1, s2, s3 := newResponder(t, clk, root), newResponder(t, clk, root), newResponder(t, clk, root)
	c := newClient(clk, Config{MaxHostsPerTenant: 1}, Hooks{})
	a := mk(t, root, "a", false, s1.URL)
	b := mk(t, root, "b", false, s2.URL)
	if c.Check(context.Background(), req("t1", tgt(a, root))).Results[tgt(a, root).Key()] != x509trust.OCSPGood {
		t.Fatal("first host")
	}
	if r := c.Check(context.Background(), req("t1", tgt(b, root))).Reasons[tgt(b, root).Key()]; r != ReasonHostBudget {
		t.Fatalf("a second distinct AIA host was allowed: %q", r)
	}
	// another tenant has its own budget
	if c.Check(context.Background(), req("t2", tgt(b, root))).Results[tgt(b, root).Key()] != x509trust.OCSPGood {
		t.Fatal("the budget is not per tenant")
	}
	// the operator-set override is not counted against it
	r := req("t1", tgt(b, root))
	r.OverrideURL = s3.URL
	b2 := mk(t, root, "b2", false)
	r.Targets = []Target{tgt(b2, root)}
	if c.Check(context.Background(), r).Results[tgt(b2, root).Key()] != x509trust.OCSPGood {
		t.Fatal("override counted against the AIA budget")
	}
	// the budget frees when a host goes idle
	clk.advance(hostIdle + time.Minute)
	if c.Check(context.Background(), req("t1", tgt(b, root))).Results[tgt(b, root).Key()] != x509trust.OCSPGood {
		t.Fatal("an idle host kept the budget")
	}
}

// The egress guard is the boundary: a responder on a loopback / private address is refused at
// dial, reported as blocked, and the AIA client (a guard with no allow list) cannot reach an
// address the operator allowed for the per-anchor override.
func TestEgressGuardIsTheBoundary(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root) // 127.0.0.1
	leaf := mk(t, root, "leaf", false, srv.URL)

	strict := NewHTTPClient(egress.NewGuard(nil).Transport())
	c := New(Config{FetchDeadline: 2 * time.Second, MaxInFlight: 8, MaxAge: 6 * time.Hour, CacheEntries: 10, MaxHostsPerTenant: 4, Now: clk.now},
		strict, strict, Hooks{})
	if r := c.Check(context.Background(), req("t1", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]; r != ReasonBlocked {
		t.Fatalf("a loopback responder was dialled or misreported: %q", r)
	}
	if srv.hits.Load() != 0 {
		t.Fatal("a connection reached the blocked target")
	}

	allowing := NewHTTPClient(egress.NewGuard([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}).Transport())
	c = New(Config{FetchDeadline: 2 * time.Second, MaxInFlight: 8, MaxAge: 6 * time.Hour, CacheEntries: 10, MaxHostsPerTenant: 4, Now: clk.now},
		allowing, strict, Hooks{})
	// via the AIA client the operator-allowed address is still unreachable ...
	if r := c.Check(context.Background(), req("t1", tgt(leaf, root))).Reasons[tgt(leaf, root).Key()]; r != ReasonBlocked {
		t.Fatalf("AIA reached an operator-allowed address: %q", r)
	}
	// ... and via the per-anchor override it is reachable.
	r := req("t1", tgt(leaf, root))
	r.OverrideURL = srv.URL
	if c.Check(context.Background(), r).Results[tgt(leaf, root).Key()] != x509trust.OCSPGood {
		t.Fatal("the operator-allowed override was refused")
	}
}

func TestCacheIsBounded(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	c := newClient(clk, Config{CacheEntries: 5}, Hooks{})
	for i := 0; i < 20; i++ {
		leaf := mk(t, root, "leaf", false, srv.URL)
		c.Check(context.Background(), req("t1", tgt(leaf, root)))
	}
	c.mu.Lock()
	n := len(c.cache)
	c.mu.Unlock()
	if n > 5 {
		t.Fatalf("cache holds %d entries, bound is 5", n)
	}
}

// One tenant cycling certificates must not push another tenant's warm answers out of a full cache:
// eviction takes from the tenant holding the most answers.
func TestCacheEvictionNeverStarvesASmallerTenant(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	c := newClient(clk, Config{CacheEntries: 6}, Hooks{})
	victims := []*node{mk(t, root, "v1", false, srv.URL), mk(t, root, "v2", false, srv.URL)}
	for _, v := range victims {
		c.Check(context.Background(), req("victim", tgt(v, root)))
	}
	for i := 0; i < 30; i++ {
		c.Check(context.Background(), req("noisy", tgt(mk(t, root, "n", false, srv.URL), root)))
	}
	before := srv.hits.Load()
	for _, v := range victims {
		out := c.Check(context.Background(), req("victim", tgt(v, root)))
		if out.Results[tgt(v, root).Key()] != x509trust.OCSPGood {
			t.Fatalf("victim answer lost: %+v", out)
		}
	}
	if got := srv.hits.Load() - before; got != 0 {
		t.Fatalf("the victim's answers were evicted by another tenant (%d refetches)", got)
	}
	c.mu.Lock()
	n, noisy := len(c.cache), c.perTenC["noisy"]
	c.mu.Unlock()
	if n > 6 || noisy != 4 {
		t.Fatalf("cache %d entries (bound 6), noisy holds %d (want 4)", n, noisy)
	}
}

// Every Check under one connect shares ONE deadline: a second Check after the first spent the
// budget gets none of its own.
func TestConnectBudgetIsSharedAcrossChecks(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	srv := newResponder(t, clk, root)
	srv.delay = 3 * time.Second
	a := mk(t, root, "a", false, srv.URL)
	b := mk(t, root, "b", false, srv.URL)
	c := newClient(clk, Config{FetchDeadline: 200 * time.Millisecond}, Hooks{})
	ctx := WithConnectBudget(context.Background())
	start := time.Now()
	c.Check(ctx, req("t1", tgt(a, root)))
	c.Check(ctx, req("t1", tgt(b, root)))
	if took := time.Since(start); took > 350*time.Millisecond {
		t.Fatalf("two checks under one connect took %v; the budget is 200ms in total", took)
	}
	// The control: without the marker each Check has its own deadline.
	start = time.Now()
	c2 := newClient(clk, Config{FetchDeadline: 200 * time.Millisecond}, Hooks{})
	c2.Check(context.Background(), req("t2", tgt(a, root)))
	c2.Check(context.Background(), req("t2", tgt(b, root)))
	if took := time.Since(start); took < 350*time.Millisecond {
		t.Fatalf("control: two unmarked checks took %v, expected two deadlines", took)
	}
}

// A good answer that lapses while another target of the same connect is still being fetched is no
// answer when the outcome is returned.
func TestGoodThatLapsesDuringTheCheckIsNotGood(t *testing.T) {
	clk := &clock{t: base}
	root := mk(t, nil, "root", true)
	fast := newResponder(t, clk, root)
	fast.next = time.Minute // valid until ~base+1m
	slow := newResponder(t, clk, root)
	var hits atomic.Int64
	slow.mangle = func(w http.ResponseWriter, body []byte) bool {
		// a's cache hit is read first; only then does time pass while b is fetched
		for i := 0; i < 400 && hits.Load() == 0; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		clk.advance(2 * time.Minute)
		return false
	}
	a := mk(t, root, "a", false, fast.URL)
	b := mk(t, root, "b", false, slow.URL)
	c := newClient(clk, Config{}, Hooks{Cache: func(hit bool) {
		if hit {
			hits.Add(1)
		}
	}})
	// fetch a first, so it is answered before b's fetch moves the clock
	if out := c.Check(context.Background(), req("t1", tgt(a, root))); out.Results[tgt(a, root).Key()] != x509trust.OCSPGood {
		t.Fatalf("control: a should be good: %+v", out)
	}
	out := c.Check(context.Background(), req("t1", tgt(a, root), tgt(b, root)))
	if out.Results[tgt(a, root).Key()] == x509trust.OCSPGood || out.Reasons[tgt(a, root).Key()] != ReasonStale {
		t.Fatalf("a lapsed during the check but was reported %v / %q", out.Results[tgt(a, root).Key()], out.Reasons[tgt(a, root).Key()])
	}
}
