// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog/log"
)

// cacheOpTimeout bounds every Get and Set a Cache makes. A healthy direct get or KV put
// answers in single-digit milliseconds, and the cache only ever saves one indexed database
// read, so waiting longer than this is pure loss. A caller's own deadline still wins when
// it is sooner.
const cacheOpTimeout = 500 * time.Millisecond

// cacheDeleteTimeout bounds an eviction, and it is deliberately NOT cacheOpTimeout.
//
// A Get can be answered by any replica, so a short budget only gives up on one that has
// gone silent. A Delete is a write, which only the bucket's LEADER can accept, and a
// leader moving takes the better part of a second on its own. An eviction that gives up
// there leaves the entry it was meant to remove in place, and that entry is then SERVED
// until its TTL runs out: a wrong answer, where a skipped read is only a slower one. An
// eviction is not on the per-event path (it runs after a mutation commits), so it keeps
// the 5 s a JetStream request always had. The fan-out evictions run one after another, so
// this is a bound per entry, not per mutation.
const cacheDeleteTimeout = 5 * time.Second

// cacheBypassFor is how long a cache that timed out or could not be reached is skipped
// before one operation is let through to probe it.
const cacheBypassFor = 5 * time.Second

// cacheOpenTimeout bounds opening the bucket handle in NewCache (one stream-info request).
const cacheOpenTimeout = 10 * time.Second

// ErrCacheUnavailable is what Get and Set return, without a round trip, while the cache is
// bypassed. It is an error and not a miss, so no caller can read a skipped lookup as "the
// key is absent".
var ErrCacheUnavailable = errors.New("messaging: cache unavailable; bypassed after an operation timed out or could not reach it")

// Cache is a distributed key/value cache backed by a NATS JetStream KV bucket
// (ADR-007: NATS KV replaces Redis). One Cache wraps one bucket; the bucket's
// per-entry TTL bounds staleness, so a positive entry is evicted automatically
// even if its source row changes between explicit invalidations.
//
// Values are JSON-encoded, so any json-serializable value round-trips. Cache
// keys are arbitrary caller strings (e.g. "tenant|token"); they are base64url-
// encoded before use because the NATS KV key charset is restricted and the
// caller's keys are not guaranteed to fall within it.
//
// 🔑 EVERY OPERATION IS BOUNDED, AND A CACHE THAT STOPS ANSWERING IS SKIPPED. The ctx on
// every method is honoured: a Get or Set gives up at the sooner of the caller's deadline
// and cacheOpTimeout, and a Delete at cacheDeleteTimeout (see there for why it differs).
//
// The breaker exists because of how a replicated bucket answers a read. Every replica
// serves direct gets, and a request goes to one of them at random. A NATS server that
// drops off the network without closing its connections stays in that draw until the
// other servers stop hearing its pings — a route pings at most every 30 s and is dropped
// after two unanswered pings, so a minute to a minute and a half — and every read sent to
// it in that time is never answered. Bounding each read is not enough on its own: with
// several reads per event and a share of them each costing the whole budget, resolution
// still crawls. So the first timeout opens the breaker, and for cacheBypassFor every Get
// and Set returns ErrCacheUnavailable at once and the caller goes to the database, which
// holds the same data. Then one operation is let through as a probe; only the probe's
// success closes the breaker. Delete is never skipped: see Delete.
//
// Only a failure that says the cache is unreachable opens it: the budget running out, a
// JetStream request timeout, or nobody answering. An error the bucket ANSWERS with — a
// full bucket refusing a write, say — is counted but opens nothing, because the bucket is
// there and its reads are fine. Neither does the CALLER giving up: a request whose own
// context was cancelled or ran out says nothing about the cache, and letting it open the
// breaker would let one slow client switch the cache off for everyone.
type Cache struct {
	kv      cacheStore
	name    string        // the bucket's entry in the kv inventory, for logs and metrics
	timeout time.Duration // cacheOpTimeout; a field only so unit tests can shorten it
	delete  time.Duration // cacheDeleteTimeout; likewise
	bypass  time.Duration // cacheBypassFor; likewise
	now     func() time.Time
	obs     *cacheObserver // nil-safe

	mu        sync.Mutex
	openUntil time.Time // zero while the cache is answering (the breaker is closed)
	probing   bool      // a probe is in flight; every other caller is still bypassed
	since     time.Time // when the breaker opened
	bypassed  int64     // operations skipped since it opened
}

// cacheStore is the part of the jetstream KeyValue a Cache actually uses: three methods
// out of the interface's twenty-odd. jetstream.KeyValue satisfies it structurally, so
// NewCache hands the real bucket straight in.
//
// 🔑 IT IS NARROWED SO THE CACHE CAN BE TESTED AT ALL. Wrapping the full KeyValue meant a
// Cache could only exist with a live JetStream connection behind it, which is why nothing
// in the repository had ever tested a Cache, or any of the decorators built on one — and a
// caching override that silently stopped overriding would have gone on passing every
// test. Depending on the three methods actually called is what makes an in-memory double
// a dozen lines instead of a JetStream server.
type cacheStore interface {
	Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Delete(ctx context.Context, key string, opts ...jetstream.KVDeleteOpt) error
}

// NewCacheOver builds a Cache over any store providing the three methods a Cache uses.
//
// It exists for tests: production goes through NewCache, which supplies the real
// JetStream bucket. It is exported because the decorators worth testing this way live in
// the service modules, not in core.
func NewCacheOver(store cacheStore) *Cache {
	return newCache("test", store, nil)
}

func newCache(name string, store cacheStore, m *streamMetrics) *Cache {
	c := &Cache{
		kv:      store,
		name:    name,
		timeout: cacheOpTimeout,
		delete:  cacheDeleteTimeout,
		bypass:  cacheBypassFor,
		now:     time.Now,
		obs:     m.cacheObserver(name),
	}
	c.obs.init()
	return c
}

// NewCache returns a Cache over a JetStream KV bucket named for this instance,
// functional area, and the given cache name, creating it with the given TTL if
// it does not yet exist. The name is sanitized into the bucket-name charset.
//
// The bucket is created, bounded and tracked through KeyValueStore like every other
// bucket; only the handle the Cache reads and writes through is the jetstream package's,
// because that is the one whose calls take a context.
func (nmgr *NatsManager) NewCache(name string, ttl time.Duration) (*Cache, error) {
	bucket := CacheBucketName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, name)
	if _, err := nmgr.KeyValueStore(name, bucket, ttl); err != nil {
		return nil, err
	}
	js, err := jetstream.New(nmgr.nc)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cacheOpenTimeout)
	defer cancel()
	store, err := js.KeyValue(ctx, bucket)
	if err != nil {
		return nil, err
	}
	return newCache(name, store, nmgr.metrics), nil
}

// Set stores value under key, JSON-encoding it. The entry expires after the
// bucket TTL configured at construction. While the cache is bypassed it returns
// ErrCacheUnavailable without trying.
func (c *Cache) Set(ctx context.Context, key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	probe, ok := c.admit()
	if !ok {
		c.obs.bypassed("set")
		return ErrCacheUnavailable
	}
	opctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	start := time.Now()
	_, err = c.kv.Put(opctx, kvKey(key), data)
	c.obs.observe("set", time.Since(start))
	c.settle(ctx, "set", probe, err)
	return err
}

// Get loads the entry for key into dest (a pointer) and reports whether it was
// present. A miss returns (false, nil). Any other outcome that did not find the value
// returns an error — a transport or decode error, or ErrCacheUnavailable while the cache
// is bypassed — so callers can degrade a miss-or-error to a DB lookup and no failure is
// ever mistaken for the key being absent.
func (c *Cache) Get(ctx context.Context, key string, dest interface{}) (bool, error) {
	probe, ok := c.admit()
	if !ok {
		c.obs.bypassed("get")
		return false, ErrCacheUnavailable
	}
	opctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	start := time.Now()
	entry, err := c.kv.Get(opctx, kvKey(key))
	c.obs.observe("get", time.Since(start))
	c.settle(ctx, "get", probe, err)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	// A decode error is returned but not fed to the breaker: the bucket answered.
	if err := json.Unmarshal(entry.Value(), dest); err != nil {
		return false, err
	}
	return true, nil
}

// Delete evicts an entry, tolerating a miss. Used to invalidate a cached entry
// on mutation so a stale value is not served (bounded further by the TTL).
//
// 🔴 IT IS NEVER SKIPPED, AND IT OUTLIVES ITS CALLER. A skipped or abandoned eviction
// leaves a stale entry that is served, as a wrong answer, once the cache is read again —
// whereas a skipped read is only a slower one. So Delete ignores the breaker, runs on a
// context its caller cannot cancel (the mutation it follows has already committed; a
// client hanging up now must not leave the old value behind), and is bounded by
// cacheDeleteTimeout. Its outcome still feeds the breaker, and a failure is logged here
// because every call site discards it.
func (c *Cache) Delete(ctx context.Context, key string) error {
	opctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.delete)
	defer cancel()
	start := time.Now()
	err := c.kv.Delete(opctx, kvKey(key))
	c.obs.observe("delete", time.Since(start))
	c.settle(context.Background(), "delete", false, err)
	if err != nil && !isNotFound(err) {
		sum := sha256.Sum256([]byte(key))
		log.Warn().Err(err).Str("cache", c.name).Str("keyHash", hex.EncodeToString(sum[:8])).
			Msg("A key-value cache eviction failed; the entry is served until its TTL expires")
		return err
	}
	return nil
}

// admit decides whether an operation may go to the store. It lets everything through
// while the breaker is closed, nothing while it is open, and exactly one probe once the
// bypass has run out.
func (c *Cache) admit() (probe bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openUntil.IsZero() {
		return false, true
	}
	if c.now().Before(c.openUntil) || c.probing {
		c.bypassed++
		return false, false
	}
	c.probing = true
	return true, true
}

// settle feeds one operation's outcome to the breaker. ctx is the CALLER's context,
// which is what tells a cache that did not answer apart from a caller that stopped
// waiting.
//
// 🔴 ONLY THE PROBE CLOSES THE BREAKER. Several resolvers share one Cache, so when one of
// them times out and opens it, the others usually have reads already in flight to
// replicas that are fine, and those come back in milliseconds, after the failure. If any
// success closed the breaker, one of them would close it almost at once, every caller
// would go back to drawing the silent replica a third of the time, and the log would
// flap between "stopped answering" and "answering again" on every failure.
func (c *Cache) settle(ctx context.Context, op string, probe bool, err error) {
	if err != nil && !isNotFound(err) {
		if ctx.Err() == nil {
			c.obs.failure(op, failureReason(err))
		}
	}
	fault := err != nil && !isNotFound(err) && ctx.Err() == nil && isCacheFault(err)

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	switch {
	case fault && c.openUntil.IsZero():
		c.openUntil = now.Add(c.bypass)
		c.since = now
		c.bypassed = 0
		c.obs.setUnavailable(true)
		log.Warn().Err(err).Str("cache", c.name).Str("op", op).Dur("bypassFor", c.bypass).
			Msg("A key-value cache stopped answering; its lookups go to the database until it does")
	case fault && probe:
		c.openUntil = now.Add(c.bypass)
		c.probing = false
	case probe && err != nil && !isNotFound(err):
		// The probe failed for a reason that is not the cache's (the caller gave up, or
		// the bucket answered with an error): no verdict, so the next caller probes.
		c.probing = false
	case probe:
		// The probe got an answer, a miss included: the cache is answering again.
		c.obs.setUnavailable(false)
		log.Info().Str("cache", c.name).Dur("unavailableFor", now.Sub(c.since)).Int64("bypassed", c.bypassed).
			Msg("A key-value cache is answering again")
		c.openUntil = time.Time{}
		c.probing = false
	}
}

// isNotFound reports a miss, which is a healthy answer. The second arm is for test
// doubles written against the older sentinel.
func isNotFound(err error) bool {
	return errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, nats.ErrKeyNotFound)
}

// isCacheFault reports an error that says the cache could not be reached in time, as
// opposed to one the bucket answered with.
func isCacheFault(err error) bool {
	return isTimeout(err) ||
		errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, jetstream.ErrNoStreamResponse) ||
		errors.Is(err, nats.ErrConnectionClosed) ||
		errors.Is(err, nats.ErrConnectionDraining)
}

func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout)
}

// failureReason is the metric label for a failed operation: two values, on purpose.
func failureReason(err error) string {
	if isTimeout(err) {
		return "timeout"
	}
	return "error"
}
