// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/devicechain-io/dc-microservice/governance"
)

// 🔴🔴 THIS FILE IS THE ONE PLACE IN THE CONTAINMENT LAYER THAT IS NOT LOOP-OWNED, AND THE LOCAL
// IDIOM IS A TRAP HERE. FenceSet is immutable, and FenceSetView is documented lock-free — but only
// because it runs on the processor's single-writer goroutine, the same goroutine that reads it.
// The geometry cache has no such owner: it is written by the fact consumer, by the reconcile sweep,
// by the startup reconcile, and by per-request preview goroutines, at least four goroutine families
// with no ordering between them. Copying the lock-free idiom across from its neighbours would be a
// straightforward data race on a map, and — because a fence set is built once per version and then
// read for minutes — one that would show up as a rare, unreproducible crash rather than as anything
// a test would catch. Every field below is guarded, and the guard is named in each method.
//
// WHY IT LIVES IN THIS PACKAGE rather than in a sibling under internal/. Three of the cache's
// obligations are statements about COMPILED GEOMETRY, i.e. about this package's own vocabulary: it
// admits only documents this package can compile, it bounds itself in the vertex unit
// Compiled.Vertices defines, and it must force the s2 index build (Compiled.Prebuild) before
// publishing a value. Housing it here also means its tests can register a geometry kind of their
// own in `containment` — the same seam the dispatch test already uses — so "the inserter really did
// pre-build the index before anyone could see the value" is a direct assertion about an observed
// call rather than an argument about timings. From a sibling package that requirement could only be
// tested indirectly, which for a requirement whose whole purpose is to remove a latent race is not
// good enough.
//
// This does NOT make the exported surface in compiled.go redundant. Its consumer is the code that
// assembles a FenceSet from cached geometry — the manifest consumer — which lives outside this
// package and holds only tokens, kinds and compiled values.

// DefaultMaxCachedVertices is the cache's default bound, in compiled vertices across every retained
// entry.
//
// 🔴 IT IS A COST PROXY, NOT A BYTE BUDGET, AND CLAIMING OTHERWISE WOULD BE THE USUAL LIE. The
// vertex array alone is 24 bytes per vertex (an s2.Point is three float64s), but a loop above s2's
// brute-force threshold also carries a shape index whose size depends on the geometry's shape, not
// only on its vertex count, so no single multiplier turns this number into megabytes. What the unit
// does buy is the property an entry count cannot: cost is monotone in it, and containment work is
// O(vertices) too, so one number bounds both the memory and the per-event work.
//
// 250,000 is sized from the authoring caps it has to sit above, and it is DERIVED FROM THEM rather
// than chosen beside them. Those caps are now per-tenant governance settings on the ADR-065 cascade,
// so the sentence this comment used to make — "this holds several tenants at that absolute ceiling
// simultaneously" — was a claim about two numbers that no longer moved together, in a file that
// could not see one of them. Writing it as an expression makes the relation hold by construction:
//
//   - governance.DefaultTenantGeometryVertices is one tenant's DEFAULT whole-fence-set budget, and
//     it is a FIFTH of this bound, so five tenants at their default budget are resident at once;
//   - governance.MaxTenantGeometryVertices is the largest budget any tier may grant, and it is a
//     HALF of this bound, so no single tenant — at any tier an operator can configure — may hold
//     more than half the cache and evict every other tenant's geometry on one refill.
//
// The two consts below assert exactly that, at COMPILE TIME rather than in a test: each is a
// uint conversion of a difference that must not be negative, so the two together say the bound
// equals both derivations, and any edit that breaks either fails the build in this file.
//
// The bound exists to make a pathological tenant cost bounded memory rather than to be tight —
// evicting a hot entry costs one refetch and one recompile, which is cheap; a process that grows
// without limit is not.
const DefaultMaxCachedVertices = governance.DefaultTenantGeometryVertices * defaultBudgetTenancyFactor

// defaultBudgetTenancyFactor is how many tenants at their DEFAULT fence-set budget the cache holds
// at once. It is the one number in this relation that is a judgement rather than a derivation, so
// it is named: five is enough that a small instance's whole tenant population is typically
// resident, and small enough that the bound stays a bound.
const defaultBudgetTenancyFactor = 5

// 🔴 THE TWO ASSERTIONS BELOW ARE NOT DEAD CODE. A const of type uint cannot hold a negative value,
// so each fails to compile when its difference goes negative, and the PAIR of them can only both
// compile when the two sides are EQUAL. That is the "no tenant holds more than half the cache"
// invariant, enforced by the compiler in the file that owns the number — which is what this
// codebase reaches for whenever a comment would otherwise assert an invariant nothing checks.
const _ = uint(DefaultMaxCachedVertices - governance.MaxTenantGeometryVertices*2)
const _ = uint(governance.MaxTenantGeometryVertices*2 - DefaultMaxCachedVertices)

// ErrGeometryHashMismatch is returned when a fetched geometry document does not hash to the content
// address it was fetched for. It is a REFUSAL, never a fallback: the document is discarded and
// nothing is stored under that address.
var ErrGeometryHashMismatch = errors.New("the fetched geometry document does not hash to its content address")

// GeometryFetch resolves the geometry document for one content address. It is supplied per call
// rather than held by the cache so that the cache stays a pure memoization layer with no opinion
// about transport, and so a preview can resolve through a different door than the live consumer
// without a second cache.
//
// It returns the document VERBATIM as the archive holds it. It must not canonicalise, re-encode or
// pretty-print: the address is the SHA-256 of the stored bytes, so any transformation on the way
// back — including a JSON round-trip that only changes separator spacing — turns a correct document
// into a hash mismatch.
type GeometryFetch func(ctx context.Context) ([]byte, error)

// GeometryCacheStats is one consistent snapshot of the cache's counters, for metrics and for tests.
// Entries and Vertices are gauges; the rest are monotonic counters since construction.
type GeometryCacheStats struct {
	// Entries is how many distinct (tenant, hash) documents are retained.
	Entries int
	// Vertices is the retained total in compiled vertices — the quantity the bound applies to.
	Vertices int
	// Hits is the number of Get calls served from a retained entry with no fetch.
	Hits uint64
	// Misses is the number of Get calls that found nothing retained and went on to join or start
	// a fill. It is not simply "calls minus hits" — a call that is refused before it ever looks
	// (no tenant, no address) is neither — and it is the barrier a concurrency test synchronizes
	// on, since it moves the instant a caller commits to the fill path.
	Misses uint64
	// Fills is the number of times a fetch function was actually invoked. With the single-flight
	// below, a stampede of N concurrent misses on one address raises this by exactly 1.
	Fills uint64
	// Shared is the number of Get calls that took part in a fill shared with at least one other
	// caller (the caller that ran the fetch included). It is what makes the single-flight
	// OBSERVABLE: a test asserting only that Fills is 1 cannot tell coalescing apart from callers
	// that arrived late and hit the cache, and would pass either way.
	Shared uint64
	// Evictions counts entries dropped to bring the retained total back inside the bound.
	Evictions uint64
	// HashMismatches counts documents refused because they did not hash to the address they were
	// fetched for. It is non-zero only when the archive, the fetch path, or the manifest is
	// disagreeing with itself, so it belongs on a dashboard, not in a log line.
	HashMismatches uint64
	// NotRetained counts verified, compiled documents that were served to their caller but too
	// large to keep (see Get).
	NotRetained uint64
	// Purged counts entries dropped by PurgeTenant.
	Purged uint64
}

// GeometryCache memoizes COMPILED geofence geometry by content address, so that the same geometry
// document arriving through many fence-set versions — and through many tenants' rebuilds — is
// fetched once, parsed once, compiled once and index-built once.
//
// It is safe for concurrent use by any number of goroutines. See the note at the top of this file
// for why that is stated rather than inherited from its neighbours.
type GeometryCache struct {
	maxVertices int

	// group collapses concurrent misses on one address into a single fetch. Without it the four
	// goroutine families that write this cache all stampede the same miss on a fence-set rebuild:
	// N goroutines issue N fetches of the same document, N compiles and N index builds, and then
	// N-1 of the results are thrown away. It is the same idiom core/svcclient and core/userclient
	// use for token mints, for the same reason.
	group singleflight.Group

	mu       sync.Mutex
	byKey    map[string]*list.Element
	lru      *list.List // front = most recently used
	vertices int
	// generation counts completed purges PER TENANT. A fetch may be in flight when a tenant is
	// purged, and without this its insert would land afterwards and RESURRECT geometry the purge
	// was asked to erase — an erasure that silently un-does itself minutes later, on a schedule
	// nothing observes. The generation captured at the start of a Get is compared at insert.
	generation map[string]uint64

	stats GeometryCacheStats
}

// cacheEntry is one retained document. tenant and hash are kept so eviction and purging can
// identify an entry from the LRU list without a reverse index.
type cacheEntry struct {
	tenant   string
	hash     string
	key      string
	compiled Compiled
	vertices int
}

// NewGeometryCache builds a cache bounded by maxVertices retained compiled vertices. A non-positive
// bound falls back to DefaultMaxCachedVertices rather than retaining nothing: a cache that retains
// zero entries is indistinguishable from a working one on every functional test — every answer is
// still correct — while quietly refetching and recompiling every geometry on every event, which is
// a misconfiguration that must not be expressible.
func NewGeometryCache(maxVertices int) *GeometryCache {
	if maxVertices <= 0 {
		maxVertices = DefaultMaxCachedVertices
	}
	return &GeometryCache{
		maxVertices: maxVertices,
		byKey:       map[string]*list.Element{},
		lru:         list.New(),
		generation:  map[string]uint64{},
	}
}

// Get returns the compiled geometry for one (tenant, content address), fetching and compiling it
// through fetch on a miss.
//
// 🔴 THE KEY IS (TENANT, HASH), NEVER THE HASH ALONE. A content address is a pure function of the
// bytes, so sharing one compiled polygon between two tenants is provably sound TODAY — and the
// argument has to stay sound through every future change to what a geometry document contains, to
// what a tenant may deduce from a timing difference, and to what the erasure obligation covers.
// Keying per tenant gives that argument up on purpose. The cost is a duplicate entry for two
// tenants that authored byte-identical geometry, which is rare (coordinates are drawn, not shared)
// and bounded by the same vertex budget as everything else. The benefit is that "could tenant A's
// compiled geometry serve tenant B?" is never a question anyone has to answer correctly again, and
// that PurgeTenant can be total.
//
// 🔴 ONLY VERIFIED SUCCESSES ARE RETAINED — nothing here ever negative-caches. A fetch error, a
// hash mismatch and a compile failure are all returned to the caller and stored nowhere, so the
// next Get retries. The neighbouring LoadingFenceSets does memoize its failures, and is right to:
// it is built per preview run and discarded with it, so a memoized failure lives for seconds. This
// cache lives as long as the process, so the same code here would turn one timed-out fetch into a
// permanent refusal to evaluate a fence — a fence that stops working until a restart, for a reason
// that has long since gone away.
//
// A document that verifies and compiles but is larger than the whole bound is SERVED and not
// retained. Inserting it would evict every other entry to make room for something that cannot fit
// anyway, so a single oversized fence would empty the cache on each of its own uses; refusing to
// retain it costs that one fence its caching and leaves every other fence alone.
func (c *GeometryCache) Get(ctx context.Context, tenant, hash string, fetch GeometryFetch) (Compiled, error) {
	if tenant == "" {
		return Compiled{}, errors.New("geometry cache: a tenant is required (an untenanted key would let one tenant's geometry serve another)")
	}
	// The address is normalized to lowercase because that is the form GeoFenceGeometryHash mints
	// and the form a manifest carries. Normalizing rather than trusting keeps two spellings of one
	// address from becoming two entries, and — more to the point — keeps the verification below
	// comparing like with like, so a manifest that shouted its hashes would not fail every fence.
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return Compiled{}, errors.New("geometry cache: a content address is required")
	}
	if fetch == nil {
		return Compiled{}, errors.New("geometry cache: a fetch function is required")
	}
	key := tenant + "\x00" + hash

	if got, ok := c.lookup(key); ok {
		return got, nil
	}

	c.count(func(s *GeometryCacheStats) { s.Misses++ })
	v, err, shared := c.group.Do(key, func() (any, error) {
		// A fill for this key may have completed between our lookup above and our arrival here,
		// in which case starting a second fetch would be pure waste.
		if got, ok := c.lookup(key); ok {
			return got, nil
		}
		gen := c.generationOf(tenant)
		c.count(func(s *GeometryCacheStats) { s.Fills++ })
		document, err := fetch(ctx)
		if err != nil {
			return nil, fmt.Errorf("geometry cache: fetching %s for tenant %s: %w", hash, tenant, err)
		}
		return c.admit(tenant, hash, key, gen, document)
	})
	if shared {
		c.count(func(s *GeometryCacheStats) { s.Shared++ })
	}
	if err != nil {
		return Compiled{}, err
	}
	return v.(Compiled), nil
}

// admit verifies, compiles, pre-builds and (budget permitting) retains one fetched document. It is
// the ONLY path into the cache's map, which is what makes the verification below structural.
func (c *GeometryCache) admit(tenant, hash, key string, gen uint64, document []byte) (Compiled, error) {
	// 🔴 THE ADDRESS IS RE-DERIVED HERE, BEFORE ANYTHING ELSE IS DONE WITH THE BYTES, AND A
	// MISMATCH IS A REFUSAL. Making this the insert path's own job rather than the caller's is the
	// whole design: a caller that must "remember to verify" verifies until the day a second caller
	// is written, and there is already a fetch client, a manifest consumer, a reconcile sweep and a
	// preview path that would each have to remember.
	//
	// The consequence of getting it wrong does not decay. A (tenant, hash) entry outlives every
	// fence-set version that names it, and a hit bypasses fetching entirely, so a poisoned entry is
	// never re-read and can never be repaired by the reconcile sweep — it answers containment
	// confidently, with somebody else's geometry, until the process restarts. Slice 1 of this arc
	// found a mutation that stored the FIRST document under EVERY address and survived the whole
	// suite because every test measured row COUNT; the same mutation here would be invisible to any
	// test that measures hit rate or entry count, and is killed by this line instead.
	//
	// It costs one SHA-256 over a document bounded at 32 KiB — microseconds, once per distinct
	// document per tenant, off the evaluation loop.
	sum := sha256.Sum256(document)
	if computed := hex.EncodeToString(sum[:]); computed != hash {
		c.count(func(s *GeometryCacheStats) { s.HashMismatches++ })
		return Compiled{}, fmt.Errorf("%w: tenant %s asked for %s and the fetched document hashes to %s",
			ErrGeometryHashMismatch, tenant, hash, computed)
	}

	compiled, err := CompileGeometry(document)
	if err != nil {
		return Compiled{}, fmt.Errorf("geometry cache: %s for tenant %s: %w", hash, tenant, err)
	}

	// Off-loop, before publication, and before the lock: the index build is the expensive part and
	// holding the cache's mutex across it would serialize every other tenant's lookups behind it.
	compiled.Prebuild()

	vertices := compiled.Vertices()

	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.generation[tenant] != gen:
		// The tenant was purged while this fetch was in flight. Serve the caller — it asked, and
		// the bytes came from its own fetch — but retain nothing, so the purge stays done.
		c.stats.Purged++
		return compiled, nil
	case vertices > c.maxVertices:
		c.stats.NotRetained++
		return compiled, nil
	}
	if existing, held := c.byKey[key]; held {
		// Only reachable when a purge or an eviction raced this fill; replacing keeps the accounting
		// exact rather than double-counting the vertices.
		c.removeElement(existing)
	}
	c.byKey[key] = c.lru.PushFront(&cacheEntry{
		tenant: tenant, hash: hash, key: key, compiled: compiled, vertices: vertices,
	})
	c.vertices += vertices
	c.evictLocked()
	return compiled, nil
}

// evictLocked drops least-recently-used entries until the retained total is back inside the bound.
// The entry just inserted sits at the FRONT, so it is the last candidate rather than the first —
// eviction must never begin by discarding the thing it was asked to make room for.
func (c *GeometryCache) evictLocked() {
	for c.vertices > c.maxVertices {
		oldest := c.lru.Back()
		if oldest == nil {
			return
		}
		c.removeElement(oldest)
		c.stats.Evictions++
	}
}

// removeElement unlinks one entry and un-charges its vertices. The caller holds mu.
func (c *GeometryCache) removeElement(e *list.Element) {
	entry := e.Value.(*cacheEntry)
	c.lru.Remove(e)
	delete(c.byKey, entry.key)
	c.vertices -= entry.vertices
}

// lookup returns a retained entry and marks it most-recently-used.
func (c *GeometryCache) lookup(key string) (Compiled, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byKey[key]
	if !ok {
		return Compiled{}, false
	}
	c.lru.MoveToFront(e)
	c.stats.Hits++
	return e.Value.(*cacheEntry).compiled, true
}

// generationOf reads a tenant's current purge generation.
func (c *GeometryCache) generationOf(tenant string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation[tenant]
}

// count applies one counter update under the lock. The counters share the entry map's mutex rather
// than taking atomics of their own so that Stats reports a snapshot in which the gauges and the
// counters were true at the same instant.
func (c *GeometryCache) count(f func(*GeometryCacheStats)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(&c.stats)
}

// PurgeTenant drops every entry belonging to one tenant and reports how many it removed.
//
// It is the erasure door, and it also invalidates any fetch already IN FLIGHT for that tenant (see
// GeometryCache.generation) — without that, a purge issued while a fence-set rebuild is running
// would be undone by the rebuild's own insert a moment later, and nothing would say so. Authored
// geometry is the tenant's own configuration, the coordinates of its sites, so it goes with the
// tenant; this is the in-memory copy, which nothing else would clear until a restart.
func (c *GeometryCache) PurgeTenant(tenant string) int {
	if tenant == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation[tenant]++
	removed := 0
	for e := c.lru.Front(); e != nil; {
		next := e.Next()
		if e.Value.(*cacheEntry).tenant == tenant {
			c.removeElement(e)
			removed++
		}
		e = next
	}
	c.stats.Purged += uint64(removed)
	return removed
}

// Entries is how many distinct (tenant, hash) documents are retained.
// Held reports which of the given addresses this tenant already holds, so a caller can size a
// BATCH fetch to only what is missing.
//
// 🔴 IT IS ADVISORY, AND CALLERS MUST NOT TREAT IT AS AUTHORITATIVE. It records no hit or miss
// and does not refresh recency, precisely because the answer is used to plan work rather than to
// serve one: counting it would inflate the hit rate with lookups nobody was answering, and
// refreshing recency would let a planning pass reorder the eviction queue.
//
// Being wrong is safe in both directions and neither is a correctness problem. An entry can be
// evicted between this and the Get that follows, which costs a fetch that was not planned for —
// Get handles it. An entry can arrive after this said it was missing, which costs a document
// fetched and then not needed — admit collapses it. What a caller must NOT do is skip Get for an
// address this reported as held; that would read the cache without the verification, the
// single-flight and the recency update that make Get the only safe door.
func (c *GeometryCache) Held(tenant string, hashes []string) map[string]bool {
	held := make(map[string]bool, len(hashes))
	if tenant == "" {
		return held
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, hash := range hashes {
		normalized := strings.ToLower(strings.TrimSpace(hash))
		if normalized == "" {
			continue
		}
		if _, ok := c.byKey[tenant+"\x00"+normalized]; ok {
			held[hash] = true
		}
	}
	return held
}

func (c *GeometryCache) Entries() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// Vertices is the retained total in compiled vertices — the quantity MaxVertices bounds.
func (c *GeometryCache) Vertices() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vertices
}

// MaxVertices is the bound the cache was built with.
func (c *GeometryCache) MaxVertices() int { return c.maxVertices }

// Stats is a consistent snapshot of every gauge and counter, for the metrics exporter.
func (c *GeometryCache) Stats() GeometryCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Entries = c.lru.Len()
	s.Vertices = c.vertices
	return s
}
