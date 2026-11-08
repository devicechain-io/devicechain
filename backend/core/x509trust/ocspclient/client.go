// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ocspclient asks a certificate issuer's OCSP responder whether a certificate is revoked,
// on the connect path of a device authenticating with a client certificate.
//
// The chain decision stays in core/x509trust (pure, replayable); this package owns the I/O and
// every bound that keeps that I/O from becoming an outage or a hole:
//
//   - NEVER AN UNBOUNDED WAIT. One deadline covers every fetch a connect makes (they run
//     concurrently), and a connect never queues for a fetch slot: a full replica, a full tenant
//     share or an open breaker is an answer ("no usable answer") at once, and what that means is
//     the anchor's mode, not this package's.
//   - EVERY DIAL IS GUARDED. The http clients are handed in, built over egress.Guard.Transport()
//     (no proxy, dial-time address check). Redirects are refused. The responder URL is never taken
//     from a device-presented value; an AIA URL is dialled through a SEPARATE client whose guard
//     has no operator allow list, so a tenant-signed certificate cannot reach an address the
//     operator allowed for something else.
//   - NOTHING IS TRUSTED UNVERIFIED. Every response goes through x509trust.VerifyOCSPResponse.
//   - THE CACHE HOLDS GOOD ONLY, and expires at the answer's signed bounds. A revoked answer is
//     returned and never cached, so no stale revoked can outlive an operator's decision.
//   - ISOLATED BY TENANT. Cache, breaker and the in-flight share are keyed by tenant (and anchor),
//     so one tenant's responder, or one tenant's storm, never spends another's budget.
package ocspclient

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// Reason is why a target has no usable answer. A closed vocabulary, used as a metric label.
type Reason string

const (
	ReasonNone Reason = ""
	// ReasonNoResponder: no override URL and no usable AIA entry.
	ReasonNoResponder Reason = "no_responder"
	// ReasonBlocked: the egress guard refused the responder's address.
	ReasonBlocked Reason = "blocked"
	// ReasonTimeout: the connect's OCSP deadline ran out.
	ReasonTimeout Reason = "timeout"
	// ReasonUnreachable: dial, TLS or transport failure other than a block or a timeout.
	ReasonUnreachable Reason = "unreachable"
	// ReasonBadResponse: the endpoint answered, but not with a well-formed 200 OCSP response
	// (status, content type, size).
	ReasonBadResponse Reason = "bad_response"
	// ReasonBreakerOpen: the responder failed recently; no request was made.
	ReasonBreakerOpen Reason = "breaker_open"
	// ReasonSaturated: the replica's (or the tenant's share of the) fetch slots are all busy.
	ReasonSaturated Reason = "saturated"
	// ReasonHostBudget: the tenant has used its distinct responder host budget.
	ReasonHostBudget Reason = "host_budget"
	// ReasonCacheOnly: fetching is disabled (fetch deadline 0) and nothing is cached.
	ReasonCacheOnly Reason = "cache_only"
	// ReasonUnsigned: a response arrived but VerifyOCSPResponse refused it (wrong certificate,
	// untrusted signer, bad signature, stale, not yet valid, error status, malformed).
	ReasonUnsigned Reason = "unverified"
	// ReasonUnknown: the responder verifiably said "unknown" for the certificate.
	ReasonUnknown Reason = "unknown_cert"
	// ReasonStale: a good answer lapsed (its ValidUntil passed) while the connect's other
	// certificates were still being checked.
	ReasonStale Reason = "stale"
)

// Reasons lists every Reason, for metrics that pre-register their series.
var Reasons = []Reason{ReasonNoResponder, ReasonBlocked, ReasonTimeout, ReasonUnreachable, ReasonBadResponse,
	ReasonBreakerOpen, ReasonSaturated, ReasonHostBudget, ReasonCacheOnly, ReasonUnsigned, ReasonUnknown, ReasonStale}

const (
	// MaxResponseBytes bounds a responder's body.
	MaxResponseBytes = x509trust.MaxOCSPResponseBytes
	// breakerThreshold is how many consecutive endpoint failures open a responder's breaker.
	breakerThreshold = 3
	// certFailureTTL is how long one certificate's own failed check is remembered, so a
	// reconnect loop for a certificate the responder cannot answer for is not a fetch per connect.
	certFailureTTL = 60 * time.Second
	// hostIdle is how long a tenant's responder host counts against its host budget after its
	// last use.
	hostIdle = 10 * time.Minute
	// sweepOver is the size of the breaker and negative maps above which expired entries are swept.
	sweepOver = 4096
)

// Config bounds the client. Every field has a floor applied by New.
type Config struct {
	// FetchDeadline is the one deadline for all of a connect's fetches. Zero selects cache-only:
	// nothing is ever fetched on the connect path.
	FetchDeadline time.Duration
	// MaxInFlight is the replica-wide number of fetches at once.
	MaxInFlight int
	// MaxAge is the replay bound handed to the verifier.
	MaxAge time.Duration
	// FailureTTL is how long a breaker stays open before one probe is admitted.
	FailureTTL time.Duration
	// CacheEntries bounds the answers held.
	CacheEntries int
	// MaxHostsPerTenant bounds the distinct AIA-sourced responder hosts a tenant may use.
	MaxHostsPerTenant int
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Hooks are optional observers; any may be nil.
type Hooks struct {
	// Fetch is called after every network fetch with its duration and whether it ended in a verified answer.
	Fetch func(d time.Duration, ok bool)
	// Cache is called once per lookup: hit or miss.
	Cache func(hit bool)
	// InFlight is called with +1 / -1 around a fetch.
	InFlight func(delta int)
	// Breaker is called when a breaker opens.
	BreakerOpened func()
}

// HTTPDoer is the part of *http.Client this package uses.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewHTTPClient wraps a guarded transport in a client that refuses redirects.
func NewHTTPClient(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Client is the checker. Safe for concurrent use.
type Client struct {
	cfg      Config
	override HTTPDoer
	aia      HTTPDoer
	hooks    Hooks

	mu       sync.Mutex
	inflight int
	perTen   map[string]int
	// cache holds good answers: each element's Value is a *cacheEntry, in its tenant's lru list
	// (front = least recently used), so eviction is O(1) within a tenant.
	cache    map[cacheKey]*list.Element
	lru      map[string]*list.List
	negative map[cacheKey]negEntry
	breakers map[breakerKey]*breaker
	hosts    map[string]map[string]time.Time
	flight   singleflight.Group
}

type cacheKey struct {
	tenant, anchor string
	issuer         x509trust.IssuerID
	serial         string
}

type cacheEntry struct {
	key   cacheKey
	until time.Time
}

// maxBreakers bounds the breaker map, whatever keys appear: past it a new endpoint's failure is
// not recorded, which costs a fetch and never a wait.
const maxBreakers = 4096

// sweepPerStore is how many of the victim tenant's least-recently-used answers one store looks
// at for expiry before evicting: expired answers are swept a few at a time, never all at once.
const sweepPerStore = 8

// fullBudgetSlack is how far short of FetchDeadline a fetch's remaining time may be and still
// count as having had the full deadline (the time spent getting from Check to the dial).
const fullBudgetSlack = 50 * time.Millisecond

type negEntry struct {
	until  time.Time
	reason Reason
}

// breakerKey is one tenant's anchor's view of one responder ENDPOINT (scheme and host): the many
// paths a host serves share one breaker, and the map's size is bounded by hosts, not URLs.
type breakerKey struct{ tenant, anchor, host string }

type breaker struct {
	failures int
	openedAt time.Time
	open     bool
	probing  bool
}

// New builds a client. override is used for the tenant-set per-anchor URLs (the reach of a tenant
// webhook), aia for URLs read from
// certificates; both MUST be built over a guarded transport (NewHTTPClient(guard.Transport())),
// and aia over a guard with no operator allow list. A nil doer refuses every fetch it would make.
func New(cfg Config, override, aia HTTPDoer, hooks Hooks) *Client {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxInFlight < 1 {
		cfg.MaxInFlight = 1
	}
	if cfg.FailureTTL <= 0 {
		cfg.FailureTTL = 30 * time.Second
	}
	if cfg.CacheEntries < 1 {
		cfg.CacheEntries = 1
	}
	if cfg.MaxHostsPerTenant < 1 {
		cfg.MaxHostsPerTenant = 1
	}
	return &Client{
		cfg: cfg, override: override, aia: aia, hooks: hooks,
		perTen:   map[string]int{},
		cache:    map[cacheKey]*list.Element{},
		lru:      map[string]*list.List{},
		negative: map[cacheKey]negEntry{},
		breakers: map[breakerKey]*breaker{},
		hosts:    map[string]map[string]time.Time{},
	}
}

// Target is one certificate to check.
type Target = x509trust.OCSPTarget

// Request is one connect's check.
type Request struct {
	Tenant string
	// Anchor identifies the trust anchor (its id), part of every key.
	Anchor string
	// OverrideURL is the anchor's own responder, set by the tenant on the anchor; empty means each
	// certificate's AIA.
	OverrideURL string
	Targets     []Target
}

// Outcome is the result of a Request.
type Outcome struct {
	// Results is what was learned, by target. A target with no usable answer is absent.
	Results x509trust.OCSPResults
	// Reasons is why each unanswered target has no answer.
	Reasons map[x509trust.OCSPTargetKey]Reason
	// Revoked carries the verified answer of each revoked target.
	Revoked map[x509trust.OCSPTargetKey]*x509trust.OCSPAnswer
}

// Check checks every target CONCURRENTLY under one deadline and returns what it learned. It
// always waits for every target (bounded by the deadline), so a revoked answer is never hidden by
// another target's failure.
func (c *Client) Check(ctx context.Context, req Request) Outcome {
	out := Outcome{
		Results: x509trust.OCSPResults{},
		Reasons: map[x509trust.OCSPTargetKey]Reason{},
		Revoked: map[x509trust.OCSPTargetKey]*x509trust.OCSPAnswer{},
	}
	fetchCtx := ctx
	if c.cfg.FetchDeadline > 0 {
		deadline := time.Now().Add(c.cfg.FetchDeadline) // wall clock: a context deadline is real time, not the injected clock
		if b, ok := ctx.Value(budgetKey{}).(*connectBudget); ok {
			deadline = b.deadline(deadline)
		}
		var cancel context.CancelFunc
		fetchCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	goodUntil := map[x509trust.OCSPTargetKey]time.Time{}
	for _, t := range req.Targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			ans, reason := c.checkOne(fetchCtx, req, t)
			key := t.Key()
			mu.Lock()
			defer mu.Unlock()
			switch {
			case ans != nil && ans.Status == x509trust.OCSPStatusGood:
				out.Results[key] = x509trust.OCSPGood
				goodUntil[key] = ans.ValidUntil
			case ans != nil && ans.Status == x509trust.OCSPStatusRevoked:
				out.Results[key] = x509trust.OCSPRevoked
				out.Revoked[key] = ans
			default:
				out.Reasons[key] = reason
			}
		}(t)
	}
	wg.Wait()
	// A good answer is good only until its ValidUntil, and the targets finish at different times:
	// one that lapsed while another was still being fetched is no answer now.
	now := c.cfg.Now()
	for key, until := range goodUntil {
		if !now.Before(until) {
			delete(out.Results, key)
			out.Reasons[key] = ReasonStale
		}
	}
	return out
}

func keyOf(req Request, t Target) (cacheKey, bool) {
	serial, ok := x509trust.SerialHex(t.Cert.SerialNumber)
	return cacheKey{tenant: req.Tenant, anchor: req.Anchor, issuer: x509trust.IssuerIDOfCA(t.Issuer), serial: serial}, ok
}

func (c *Client) checkOne(ctx context.Context, req Request, t Target) (*x509trust.OCSPAnswer, Reason) {
	key, ok := keyOf(req, t)
	if !ok {
		return nil, ReasonUnsigned
	}
	now := c.cfg.Now()
	c.mu.Lock()
	if el, hit := c.cache[key]; hit {
		e := el.Value.(*cacheEntry)
		if now.Before(e.until) {
			c.lru[key.tenant].MoveToBack(el)
			until := e.until
			c.mu.Unlock()
			c.observeCache(true)
			return &x509trust.OCSPAnswer{Status: x509trust.OCSPStatusGood, ValidUntil: until}, ReasonNone
		}
		c.evict(key)
	}
	if n, hit := c.negative[key]; hit {
		if now.Before(n.until) {
			c.mu.Unlock()
			c.observeCache(true)
			return nil, n.reason
		}
		delete(c.negative, key)
	}
	c.mu.Unlock()
	c.observeCache(false)

	if c.cfg.FetchDeadline <= 0 {
		return nil, ReasonCacheOnly
	}
	// A connect whose budget is already spent does not dial at all: there is no time for an answer
	// to arrive in, and a fetch started now would only fail and be blamed on the responder.
	if ctx.Err() != nil {
		return nil, ReasonTimeout
	}
	// One fetch per key, however many connects ask. The fetch runs on its OWN full deadline,
	// detached from whichever waiter started it: a waiter that leaves early stops waiting, but the
	// answer still arrives and is cached for the next connect. Waiters wait only as long as their
	// own context.
	ch := c.flight.DoChan(fmt.Sprintf("%q|%q|%s|%s|%s", key.tenant, key.anchor, key.issuer.Key, key.issuer.Name, key.serial),
		func() (any, error) {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.FetchDeadline)
			defer cancel()
			ans, reason := c.fetch(fctx, req, t, key)
			return fetched{ans, reason}, nil
		})
	select {
	case r := <-ch:
		f := r.Val.(fetched)
		return f.ans, f.reason
	case <-ctx.Done():
		return nil, ReasonTimeout
	}
}

type fetched struct {
	ans    *x509trust.OCSPAnswer
	reason Reason
}

func (c *Client) observeCache(hit bool) {
	if c.hooks.Cache != nil {
		c.hooks.Cache(hit)
	}
}

// resolve picks the responder URL and the client that may dial it.
func (c *Client) resolve(req Request, t Target) (string, HTTPDoer, bool) {
	if req.OverrideURL != "" {
		return req.OverrideURL, c.override, false
	}
	return x509trust.OCSPResponderURL(t.Cert), c.aia, true
}

func (c *Client) fetch(ctx context.Context, req Request, t Target, key cacheKey) (*x509trust.OCSPAnswer, Reason) {
	target, doer, fromAIA := c.resolve(req, t)
	if target == "" || doer == nil {
		return nil, ReasonNoResponder
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, ReasonNoResponder
	}
	body, err := x509trust.BuildOCSPRequest(t.Cert, t.Issuer)
	if err != nil {
		return nil, ReasonUnsigned
	}
	bk := breakerKey{tenant: req.Tenant, anchor: req.Anchor, host: u.Scheme + "://" + u.Host}
	// Whether this fetch had the whole FetchDeadline to work with. Only then is its timeout the
	// responder's fault and counted toward the breaker; a fetch cut short by a caller's deadline
	// says nothing about the responder.
	full := true
	if dl, ok := ctx.Deadline(); ok {
		full = time.Until(dl) >= c.cfg.FetchDeadline-fullBudgetSlack
	}

	now := c.cfg.Now()
	if reason := c.admit(req.Tenant, u.Host, fromAIA, bk, now); reason != ReasonNone {
		return nil, reason
	}
	started := time.Now()
	if c.hooks.InFlight != nil {
		c.hooks.InFlight(1)
	}
	raw, reason := c.roundTrip(ctx, doer, target, body)
	if c.hooks.InFlight != nil {
		c.hooks.InFlight(-1)
	}
	c.release(req.Tenant)

	if reason != ReasonNone {
		if reason != ReasonTimeout || full || ctx.Err() == nil {
			c.endpointResult(bk, false)
		} else {
			c.mu.Lock()
			c.unprobe(bk)
			c.mu.Unlock()
		}
		c.observeFetch(time.Since(started), false)
		return nil, reason
	}
	c.endpointResult(bk, true)

	ans, err := x509trust.VerifyOCSPResponse(raw, t.Cert, t.Issuer, c.cfg.Now(), x509trust.OCSPOptions{MaxAge: c.cfg.MaxAge})
	if err != nil {
		c.observeFetch(time.Since(started), false)
		c.remember(key, ReasonUnsigned)
		return nil, ReasonUnsigned
	}
	switch ans.Status {
	case x509trust.OCSPStatusGood:
		c.observeFetch(time.Since(started), true)
		c.store(key, ans.ValidUntil)
		return ans, ReasonNone
	case x509trust.OCSPStatusRevoked:
		c.observeFetch(time.Since(started), true)
		return ans, ReasonNone
	default:
		c.observeFetch(time.Since(started), false)
		c.remember(key, ReasonUnknown)
		return nil, ReasonUnknown
	}
}

func (c *Client) observeFetch(d time.Duration, ok bool) {
	if c.hooks.Fetch != nil {
		c.hooks.Fetch(d, ok)
	}
}

// admit decides whether a fetch may start, and reserves its slot. It never waits.
func (c *Client) admit(tenant, host string, fromAIA bool, bk breakerKey, now time.Time) Reason {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b := c.breakers[bk]; b != nil && b.open {
		if now.Sub(b.openedAt) < c.cfg.FailureTTL || b.probing {
			return ReasonBreakerOpen
		}
		b.probing = true // half-open: exactly one request is admitted
	}
	if fromAIA {
		set := c.hosts[tenant]
		if set == nil {
			set = map[string]time.Time{}
			c.hosts[tenant] = set
		}
		if _, known := set[host]; !known {
			for h, last := range set {
				if now.Sub(last) > hostIdle {
					delete(set, h)
				}
			}
			if len(set) >= c.cfg.MaxHostsPerTenant {
				c.unprobe(bk)
				return ReasonHostBudget
			}
		}
		set[host] = now
	}
	share := max(4, c.cfg.MaxInFlight/4)
	if c.inflight >= c.cfg.MaxInFlight || c.perTen[tenant] >= share {
		c.unprobe(bk)
		return ReasonSaturated
	}
	c.inflight++
	c.perTen[tenant]++
	return ReasonNone
}

// unprobe gives back a half-open probe that was admitted but did not go out. Held under c.mu.
func (c *Client) unprobe(bk breakerKey) {
	if b := c.breakers[bk]; b != nil {
		b.probing = false
	}
}

func (c *Client) release(tenant string) {
	c.mu.Lock()
	c.inflight--
	if c.perTen[tenant]--; c.perTen[tenant] <= 0 {
		delete(c.perTen, tenant)
	}
	c.mu.Unlock()
}

// endpointResult records the outcome of a request to a responder. Only ENDPOINT failures count
// toward its breaker; a verified or unverifiable answer proves the endpoint is up.
func (c *Client) endpointResult(bk breakerKey, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.breakers[bk]
	if ok {
		if b != nil {
			delete(c.breakers, bk)
		}
		return
	}
	if b == nil {
		if len(c.breakers) >= maxBreakers {
			c.sweepBreakers()
			if len(c.breakers) >= maxBreakers {
				return // bounded: this endpoint's failure goes unrecorded, a fetch and never a wait
			}
		}
		b = &breaker{}
		c.breakers[bk] = b
	}
	b.probing = false
	b.failures++
	if b.failures >= breakerThreshold || b.open {
		if !b.open && c.hooks.BreakerOpened != nil {
			c.hooks.BreakerOpened()
		}
		b.open, b.openedAt = true, c.cfg.Now()
	}
}

func (c *Client) sweepBreakers() {
	now := c.cfg.Now()
	for k, b := range c.breakers {
		if now.Sub(b.openedAt) > 10*c.cfg.FailureTTL && !b.probing {
			delete(c.breakers, k)
		}
	}
}

// store caches a good answer. When the cache is full it evicts from the tenant holding the MOST
// answers (the storing tenant on a tie), least recently used first, after sweeping up to
// sweepPerStore of that tenant's oldest answers for expiry: a tenant cycling certificates evicts
// its own answers once it is the largest holder, and never pushes a smaller tenant's warm answers
// out. Eviction is O(1) per answer plus O(tenants) to pick the tenant; nothing walks the cache.
func (c *Client) store(key cacheKey, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, have := c.cache[key]; have {
		el.Value.(*cacheEntry).until = until
		c.lru[key.tenant].MoveToBack(el)
		return
	}
	if len(c.cache) >= c.cfg.CacheEntries {
		now := c.cfg.Now()
		victim, most := key.tenant, 0
		if l := c.lru[key.tenant]; l != nil {
			most = l.Len()
		}
		for t, l := range c.lru {
			if l.Len() > most {
				victim, most = t, l.Len()
			}
		}
		if l := c.lru[victim]; l != nil {
			swept := false
			el := l.Front()
			for i := 0; i < sweepPerStore && el != nil; i++ {
				next := el.Next()
				if e := el.Value.(*cacheEntry); !now.Before(e.until) {
					c.evict(e.key)
					swept = true
				}
				el = next
			}
			if !swept {
				if front := l.Front(); front != nil {
					c.evict(front.Value.(*cacheEntry).key)
				}
			}
		}
		if len(c.cache) >= c.cfg.CacheEntries {
			return // never overfill
		}
	}
	l := c.lru[key.tenant]
	if l == nil {
		l = list.New()
		c.lru[key.tenant] = l
	}
	c.cache[key] = l.PushBack(&cacheEntry{key: key, until: until})
}

// evict removes one cached answer. Held under c.mu.
func (c *Client) evict(k cacheKey) {
	el, have := c.cache[k]
	if !have {
		return
	}
	delete(c.cache, k)
	if l := c.lru[k.tenant]; l != nil {
		l.Remove(el)
		if l.Len() == 0 {
			delete(c.lru, k.tenant)
		}
	}
}

type budgetKey struct{}

// connectBudget is one connect's OCSP deadline, fixed by its first Check.
type connectBudget struct {
	once sync.Once
	at   time.Time
}

func (b *connectBudget) deadline(proposed time.Time) time.Time {
	b.once.Do(func() { b.at = proposed })
	return b.at
}

// WithConnectBudget marks ctx as one connect. Every Check made under it shares ONE deadline, set
// by the first: a connect that checks several candidates (registration on first connect checks
// each candidate, then the grant checks again) never spends more than FetchDeadline on OCSP in
// total.
func WithConnectBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, budgetKey{}, &connectBudget{})
}

// remember records one certificate's own failed check for certFailureTTL.
func (c *Client) remember(key cacheKey, r Reason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.negative) > sweepOver {
		now := c.cfg.Now()
		for k, e := range c.negative {
			if !now.Before(e.until) {
				delete(c.negative, k)
			}
		}
		if len(c.negative) > sweepOver {
			return // never grow without bound: an unrecorded failure only costs a fetch
		}
	}
	c.negative[key] = negEntry{until: c.cfg.Now().Add(certFailureTTL), reason: r}
}

// roundTrip performs the POST and returns the body of a 200 OCSP response, or the reason it is
// not one. The deadline is the context's.
func (c *Client) roundTrip(ctx context.Context, doer HTTPDoer, target string, body []byte) ([]byte, Reason) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, ReasonNoResponder
	}
	hreq.Header.Set("Content-Type", "application/ocsp-request")
	hreq.Header.Set("Accept", "application/ocsp-response")
	resp, err := doer.Do(hreq)
	if err != nil {
		return nil, classify(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ReasonBadResponse
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "application/ocsp-response" {
		return nil, ReasonBadResponse
	}
	if resp.ContentLength > MaxResponseBytes {
		return nil, ReasonBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, classify(ctx, err)
	}
	if len(raw) > MaxResponseBytes || len(raw) == 0 {
		return nil, ReasonBadResponse
	}
	return raw, ReasonNone
}

func classify(ctx context.Context, err error) Reason {
	var ne net.Error
	switch {
	case errors.Is(err, egress.ErrBlocked):
		return ReasonBlocked
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &ne) && ne.Timeout():
		return ReasonTimeout
	default:
		return ReasonUnreachable
	}
}
