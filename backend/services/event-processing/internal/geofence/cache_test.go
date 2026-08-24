// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geofence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

// ngonDocument is a stored POLYGON_2D envelope for a regular n-gon centred on (lonCentre, 0). It
// compiles to exactly n vertices, which is how a test mints documents of a chosen COST, and two
// different centres give two different documents and therefore two different content addresses.
func ngonDocument(lonCentre float64, n int) []byte {
	ring := make([][2]float64, 0, n+1)
	for i := 0; i < n; i++ {
		theta := 2 * math.Pi * float64(i) / float64(n)
		ring = append(ring, [2]float64{lonCentre + 0.01*math.Cos(theta), 0.01 * math.Sin(theta)})
	}
	return polygonDocument(append(ring, ring[0]))
}

// addr is the content address of a document, derived the way the archive derives it.
func addr(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

// serving is a fetch that always returns the same document, counting its calls.
func serving(document []byte, calls *int32) GeometryFetch {
	return func(context.Context) ([]byte, error) {
		atomic.AddInt32(calls, 1)
		return document, nil
	}
}

// refusing is a fetch that always fails. Used as a PROBE: a Get that returns a value through it
// was served from a retained entry, and a Get that returns its error was not.
func refusing(reason string) GeometryFetch {
	return func(context.Context) ([]byte, error) { return nil, errors.New(reason) }
}

// retained reports whether an address is held for a tenant, without disturbing anything but the
// entry's recency — it probes with a fetch that cannot succeed, so only a hit can answer.
func retained(t *testing.T, c *GeometryCache, tenant, hash string) bool {
	t.Helper()
	_, err := c.Get(context.Background(), tenant, hash, refusing("probe"))
	return err == nil
}

// answersInside runs the compiled geometry through the real containment path, which is the only
// evidence that a cached value is the geometry it claims to be rather than merely some geometry.
func answersInside(t *testing.T, c Compiled, p Position) bool {
	t.Helper()
	fs := &FenceSet{version: 1, byToken: map[string]*Fence{"f": NewCompiledFence("f", c)}}
	in, err := fs.Contains("f", p)
	if err != nil {
		t.Fatalf("Contains: %v", err)
	}
	return in
}

// TestTheKeyIsTenantScoped: two tenants that authored byte-identical geometry get two entries and
// two fetches.
//
// 🔴 THIS TEST EXISTS TO PIN A DELIBERATE INEFFICIENCY, so it is written to fail if someone
// "optimizes" it away. A content address is a pure function of the bytes, so sharing one compiled
// polygon across tenants is sound today — the point is that it has to STAY sound through every
// future change to what a geometry document holds, to what a tenant may infer from a timing
// difference, and to what erasure covers. Keying per tenant means that argument never has to be
// made again, and it is what lets PurgeTenant be total. A hash-only cache would show one entry and
// one fetch here.
func TestTheKeyIsTenantScoped(t *testing.T) {
	doc := ngonDocument(0, 5)
	hash := addr(doc)
	cache := NewGeometryCache(0)

	var calls int32
	for _, tenant := range []string{"acme", "acme", "globex"} {
		if _, err := cache.Get(context.Background(), tenant, hash, serving(doc, &calls)); err != nil {
			t.Fatalf("Get(%s): %v", tenant, err)
		}
	}
	if calls != 2 {
		t.Errorf("fetched %d times, want 2 (acme once, globex once; acme's second call is a hit)", calls)
	}
	if cache.Entries() != 2 {
		t.Errorf("Entries() = %d, want 2; one address held for two tenants is two entries", cache.Entries())
	}
	if cache.Vertices() != 10 {
		t.Errorf("Vertices() = %d, want 10 (two 5-vertex entries)", cache.Vertices())
	}
}

// TestGetRefusesAKeyItCannotScope: an untenanted or unaddressed key is refused rather than
// defaulted. An empty tenant would file geometry under a key every tenant shares, which is the
// cross-tenant sharing the key design exists to rule out — and it would arrive not as a decision
// but as a caller that forgot to thread its tenant through.
func TestGetRefusesAKeyItCannotScope(t *testing.T) {
	cache := NewGeometryCache(0)
	doc := ngonDocument(0, 4)
	var calls int32
	for _, tc := range []struct {
		name           string
		tenant, hash   string
		fetch          GeometryFetch
		wantFetchCalls int32
	}{
		{"no tenant", "", addr(doc), serving(doc, &calls), 0},
		{"no address", "acme", "   ", serving(doc, &calls), 0},
		{"no fetch", "acme", addr(doc), nil, 0},
	} {
		if _, err := cache.Get(context.Background(), tc.tenant, tc.hash, tc.fetch); err == nil {
			t.Errorf("%s: Get succeeded", tc.name)
		}
	}
	if calls != 0 {
		t.Errorf("a refused key still fetched %d times", calls)
	}
	if cache.Entries() != 0 {
		t.Errorf("a refused key stored %d entries", cache.Entries())
	}
}

// TestCachedValueCarriesTheKindAndTheGeometry: the value is the PAIR. A cache that kept only the
// compiled shape would hand back fences reporting an empty kind, which is what a fence whose
// envelope carried no kind at all reports — the two would be indistinguishable on exactly the
// surfaces that exist to tell an author what their fence is.
func TestCachedValueCarriesTheKindAndTheGeometry(t *testing.T) {
	doc := ngonDocument(20, 6)
	cache := NewGeometryCache(0)
	var calls int32
	got, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind() != KindPolygon2D {
		t.Errorf("Kind() = %q, want %q", got.Kind(), KindPolygon2D)
	}
	if got.Vertices() != 6 {
		t.Errorf("Vertices() = %d, want 6", got.Vertices())
	}
	if !answersInside(t, got, at(20, 0)) {
		t.Error("the cached geometry did not contain its own centre")
	}
	if answersInside(t, got, at(21, 0)) {
		t.Error("the cached geometry contained a point a degree away from it")
	}
	if f := NewCompiledFence("depot", got); f.Kind() != KindPolygon2D {
		t.Errorf("the kind did not survive from the cache onto a fence: %q", f.Kind())
	}
}

// TestConcurrentMissesOnOneAddressFillExactlyOnce is the single-flight requirement.
//
// 🔴 THE LOCAL LOCK-FREE IDIOM DOES NOT APPLY HERE AND NEITHER DOES ITS ABSENCE OF STAMPEDE
// CONTROL. FenceSetView is loop-owned, so it has exactly one writer and no miss can be concurrent
// with another. This cache is written by the fact consumer, the reconcile sweep, the startup
// reconcile and per-request preview goroutines, so a fence-set rebuild puts several of them on the
// same miss at the same instant — without coalescing that is N fetches, N verifications, N
// compiles and N index builds of one document, N-1 of them thrown away.
//
// 🔴 ASSERTING ONLY "Fills == 1" WOULD PASS WITHOUT COALESCING. A caller that arrives after the
// single fill has completed is a cache HIT and fills nothing, so a cache with no single-flight at
// all scores Fills == 1 whenever the callers happen not to overlap. Shared is what makes the test
// mean something: it counts callers that took part in a fill shared with somebody else, so
// requiring all of them removes the vacuous reading. The barrier below (every caller past the
// lookup, and the fill parked) is what makes the overlap real rather than hoped for.
func TestConcurrentMissesOnOneAddressFillExactlyOnce(t *testing.T) {
	const callers = 8
	doc := ngonDocument(0, 9)
	hash := addr(doc)
	cache := NewGeometryCache(0)

	var fills int32
	release := make(chan struct{})
	entered := make(chan struct{}, callers)
	started := make(chan struct{}, callers)
	values := make(chan Compiled, callers)
	failures := make(chan error, callers)

	for i := 0; i < callers; i++ {
		go func() {
			started <- struct{}{}
			got, err := cache.Get(context.Background(), "acme", hash, func(context.Context) ([]byte, error) {
				atomic.AddInt32(&fills, 1)
				entered <- struct{}{}
				<-release
				return doc, nil
			})
			if err != nil {
				failures <- err
				return
			}
			values <- got
		}()
	}
	for i := 0; i < callers; i++ {
		<-started
	}
	<-entered
	// Every caller must be past the lookup and committed to the fill path before the fill is
	// allowed to finish; Misses moves at exactly that point. The short settle afterwards covers
	// the few instructions between that counter and the coalescing itself — and if it is not
	// enough, the Shared assertion below FAILS rather than passing quietly.
	deadline := time.Now().Add(5 * time.Second)
	for cache.Stats().Misses < callers {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d callers reached the fill path", cache.Stats().Misses, callers)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)

	var first Compiled
	for i := 0; i < callers; i++ {
		select {
		case err := <-failures:
			t.Fatalf("caller failed: %v", err)
		case got := <-values:
			if i == 0 {
				first = got
			} else if got.geom != first.geom {
				t.Error("two callers were handed different compiled geometries for one address")
			}
		}
	}

	if fills != 1 {
		t.Errorf("the fetch ran %d times for one address; %d concurrent misses must collapse to 1", fills, callers)
	}
	stats := cache.Stats()
	if stats.Fills != 1 {
		t.Errorf("Stats().Fills = %d, want 1", stats.Fills)
	}
	if stats.Shared != callers {
		t.Errorf("Stats().Shared = %d, want %d; the callers did not coalesce onto one fill", stats.Shared, callers)
	}
	if cache.Entries() != 1 {
		t.Errorf("Entries() = %d, want 1", cache.Entries())
	}
}

// TestTheInserterPrebuildsBeforePublishing is the s2 index requirement, and it asserts BOTH halves
// of it: that the pre-build happened at all, and that it happened while the value was still
// private to the inserter.
//
// 🔴 THE ORDER IS THE POINT, NOT THE CALL. A pre-build performed after the entry is in the map is
// no pre-build: the value is reachable, so another goroutine can already be inside s2's lazy build
// — which is the whole situation being removed. The recorded entry count AT the moment of the
// pre-build is what tells the two apart, and nothing else does; a test that only counted calls
// would pass for the broken order.
//
// The reasons the lazy build must not be left in the hot path are in Compiled.Prebuild: an
// untagged dependency pin whose safety analysis re-opens on every bump, a build lock that is not
// double-checked so N waiters do N builds, and a single-writer loop that could otherwise block
// inside s2 behind a preview goroutine.
func TestTheInserterPrebuildsBeforePublishing(t *testing.T) {
	rec := registerCountedKind(t)
	cache := NewGeometryCache(0)
	doc := countedDocument("depot", 10)
	hash := addr(doc)

	entriesAtPrebuild := -1
	rec.onPrebuild = func() { entriesAtPrebuild = cache.Entries() }

	var calls int32
	if _, err := cache.Get(context.Background(), "acme", hash, serving(doc, &calls)); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.prebuilds != 1 {
		t.Fatalf("the inserter pre-built %d times, want 1", rec.prebuilds)
	}
	if entriesAtPrebuild != 0 {
		t.Errorf("the cache already held %d entries when the geometry was pre-built; the value was "+
			"published before its index was forced, which is exactly the window being closed", entriesAtPrebuild)
	}
	if cache.Entries() != 1 {
		t.Fatalf("Entries() = %d after the fill, want 1", cache.Entries())
	}

	// A hit serves the already-built value; nothing rebuilds.
	if _, err := cache.Get(context.Background(), "acme", hash, refusing("must not fetch")); err != nil {
		t.Fatalf("Get (hit): %v", err)
	}
	if rec.prebuilds != 1 {
		t.Errorf("a cache hit pre-built again (%d total); the work is supposed to be done once", rec.prebuilds)
	}
}

// TestNothingIsNegativeCached: every way a fill can fail leaves the cache exactly as it was, and
// the next Get tries again.
//
// 🔴 THE NEIGHBOURING MEMO DOES THE OPPOSITE AND IS RIGHT TO. LoadingFenceSets records its
// failures as nil so that a preview over ten thousand events issues one failed read rather than
// ten thousand — and it can, because it is built per preview run and thrown away with it, so a
// memoized failure lives for seconds. This cache lives as long as the process, so the same code
// here converts one timed-out fetch into a fence that stops answering until somebody restarts the
// service, for a reason that went away minutes later. Transposing the idiom is the mistake this
// test exists to catch.
func TestNothingIsNegativeCached(t *testing.T) {
	good := ngonDocument(30, 7)
	hash := addr(good)

	// Each case is a different way the fill can fail, all of them transient in principle.
	uncompilable := polygonDocument([][2]float64{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {0, 0}}) // a bow-tie
	for _, tc := range []struct {
		name  string
		hash  string
		fetch GeometryFetch
	}{
		{"the fetch failed", hash, refusing("the archive is unreachable")},
		{"the document did not hash to its address", hash, func(context.Context) ([]byte, error) { return ngonDocument(99, 4), nil }},
		{"the document did not compile", addr(uncompilable), func(context.Context) ([]byte, error) { return uncompilable, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewGeometryCache(0)
			var attempts int32
			counting := func(ctx context.Context) ([]byte, error) {
				atomic.AddInt32(&attempts, 1)
				return tc.fetch(ctx)
			}
			for i := 0; i < 3; i++ {
				if _, err := cache.Get(context.Background(), "acme", tc.hash, counting); err == nil {
					t.Fatalf("attempt %d succeeded", i)
				}
			}
			if attempts != 3 {
				t.Errorf("the failure was retried %d times in 3 calls; a memoized failure would show 1", attempts)
			}
			if cache.Entries() != 0 || cache.Vertices() != 0 {
				t.Errorf("a failed fill left %d entries / %d vertices behind", cache.Entries(), cache.Vertices())
			}

			// The transient condition clears: the very next call must succeed.
			var calls int32
			got, err := cache.Get(context.Background(), "acme", hash, serving(good, &calls))
			if err != nil {
				t.Fatalf("after the failure cleared, Get: %v", err)
			}
			if !answersInside(t, got, at(30, 0)) {
				t.Error("the recovered value is not the geometry that was asked for")
			}
		})
	}
}

// TestTheBoundIsTotalVerticesNotEntryCount.
//
// 🔴 A 3-VERTEX BOX AND A 511-VERTEX POLYGON ARE NOT ONE SLOT EACH. Authoring admits both, so entry
// cost varies by more than two orders of magnitude and an entry-count bound predicts nothing about
// what the cache actually holds. Both scenarios below are written so an entry-count bound would
// give a DIFFERENT, visibly wrong answer: the first keeps 2 of 3 equal entries where a bound of
// "three entries" would keep all three, and the second evicts one big entry to make room for four
// small ones where a bound of "five entries" would keep every one of them.
func TestTheBoundIsTotalVerticesNotEntryCount(t *testing.T) {
	t.Run("equal entries are counted in vertices", func(t *testing.T) {
		cache := NewGeometryCache(10)
		var calls int32
		for i := 0; i < 3; i++ {
			doc := ngonDocument(float64(i)*10, 4)
			if _, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls)); err != nil {
				t.Fatalf("Get(%d): %v", i, err)
			}
		}
		if cache.Entries() != 2 || cache.Vertices() != 8 {
			t.Errorf("holding %d entries / %d vertices, want 2 / 8 against a bound of 10",
				cache.Entries(), cache.Vertices())
		}
		if e := cache.Stats().Evictions; e != 1 {
			t.Errorf("Evictions = %d, want 1", e)
		}
	})

	t.Run("one large entry outweighs several small ones", func(t *testing.T) {
		cache := NewGeometryCache(50)
		var calls int32
		big := ngonDocument(0, 40)
		if _, err := cache.Get(context.Background(), "acme", addr(big), serving(big, &calls)); err != nil {
			t.Fatalf("Get(big): %v", err)
		}
		for i := 1; i <= 4; i++ {
			doc := ngonDocument(float64(i)*10, 4)
			if _, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls)); err != nil {
				t.Fatalf("Get(small %d): %v", i, err)
			}
		}
		if retained(t, cache, "acme", addr(big)) {
			t.Error("the 40-vertex entry survived four 4-vertex inserts against a 50-vertex bound")
		}
		if cache.Entries() != 4 || cache.Vertices() != 16 {
			t.Errorf("holding %d entries / %d vertices, want 4 / 16", cache.Entries(), cache.Vertices())
		}
	})
}

// TestEvictionIsLeastRecentlyUsed: recency is USE, not insertion, so a hit has to move an entry
// out of the firing line. Without that the cache evicts by age and reliably throws away the one
// fence every event in the tenant is testing against.
func TestEvictionIsLeastRecentlyUsed(t *testing.T) {
	cache := NewGeometryCache(12) // exactly three 4-vertex entries
	var calls int32
	docs := map[string][]byte{}
	for _, name := range []string{"a", "b", "c"} {
		doc := ngonDocument(float64(len(docs))*10, 4)
		docs[name] = doc
		if _, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls)); err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
	}
	// a is the oldest by insertion. Using it must make b the eviction candidate instead.
	if !retained(t, cache, "acme", addr(docs["a"])) {
		t.Fatal("a was not retained before the test even began")
	}

	d := ngonDocument(30, 4)
	if _, err := cache.Get(context.Background(), "acme", addr(d), serving(d, &calls)); err != nil {
		t.Fatalf("Get(d): %v", err)
	}
	if cache.Entries() != 3 {
		t.Fatalf("Entries() = %d, want 3", cache.Entries())
	}
	if retained(t, cache, "acme", addr(docs["b"])) {
		t.Error("b survived; eviction did not pick the least recently USED entry")
	}
	for _, name := range []string{"a", "c"} {
		if !retained(t, cache, "acme", addr(docs[name])) {
			t.Errorf("%s was evicted; only b should have been", name)
		}
	}
	if !retained(t, cache, "acme", addr(d)) {
		t.Error("d was evicted; eviction began with the entry it was making room for")
	}
}

// TestAnEntryLargerThanTheWholeBoundIsServedButNotRetained: it cannot fit, so retaining it would
// mean evicting everything else for something that still does not fit — one oversized fence would
// empty the cache on each of its own uses, and the entries it flushed would be refetched by
// whoever needed them next. Serving it without retaining costs that one fence its caching and
// leaves the others alone.
func TestAnEntryLargerThanTheWholeBoundIsServedButNotRetained(t *testing.T) {
	cache := NewGeometryCache(10)
	var calls int32
	small := ngonDocument(0, 4)
	if _, err := cache.Get(context.Background(), "acme", addr(small), serving(small, &calls)); err != nil {
		t.Fatalf("Get(small): %v", err)
	}

	big := ngonDocument(40, 40)
	got, err := cache.Get(context.Background(), "acme", addr(big), serving(big, &calls))
	if err != nil {
		t.Fatalf("an oversized document must still be SERVED: %v", err)
	}
	if !answersInside(t, got, at(40, 0)) {
		t.Error("the oversized document was served but is not the geometry that was asked for")
	}
	if cache.Entries() != 1 || cache.Vertices() != 4 {
		t.Errorf("holding %d entries / %d vertices, want 1 / 4 — the small entry must survive",
			cache.Entries(), cache.Vertices())
	}
	if !retained(t, cache, "acme", addr(small)) {
		t.Error("the small entry was flushed to make room for something that could never fit")
	}
	if n := cache.Stats().NotRetained; n != 1 {
		t.Errorf("NotRetained = %d, want 1", n)
	}
	if e := cache.Stats().Evictions; e != 0 {
		t.Errorf("Evictions = %d, want 0; nothing needed to be evicted", e)
	}
}

// TestPurgeTenantIsTotalAndScoped: erasure removes one tenant's geometry and only that tenant's.
// Authored geometry is the tenant's own configuration — the coordinates of its sites — so it goes
// with the tenant; this is the in-memory copy, which nothing else would clear until a restart.
func TestPurgeTenantIsTotalAndScoped(t *testing.T) {
	cache := NewGeometryCache(0)
	var calls int32
	acmeDocs := [][]byte{ngonDocument(0, 4), ngonDocument(10, 5)}
	for _, doc := range acmeDocs {
		if _, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls)); err != nil {
			t.Fatalf("Get(acme): %v", err)
		}
	}
	globex := ngonDocument(20, 6)
	if _, err := cache.Get(context.Background(), "globex", addr(globex), serving(globex, &calls)); err != nil {
		t.Fatalf("Get(globex): %v", err)
	}

	if removed := cache.PurgeTenant("acme"); removed != 2 {
		t.Errorf("PurgeTenant removed %d entries, want 2", removed)
	}
	if cache.Entries() != 1 || cache.Vertices() != 6 {
		t.Errorf("after the purge: %d entries / %d vertices, want 1 / 6 (globex's only)",
			cache.Entries(), cache.Vertices())
	}
	if !retained(t, cache, "globex", addr(globex)) {
		t.Error("purging acme also dropped globex's geometry")
	}
	for _, doc := range acmeDocs {
		if retained(t, cache, "acme", addr(doc)) {
			t.Error("an acme entry survived the purge")
		}
	}
	// Nothing is remembered about the purge except that it happened: a later fetch refills.
	before := cache.Stats().Fills
	if _, err := cache.Get(context.Background(), "acme", addr(acmeDocs[0]), serving(acmeDocs[0], &calls)); err != nil {
		t.Fatalf("Get after purge: %v", err)
	}
	if cache.Stats().Fills != before+1 {
		t.Error("a purged tenant's next Get did not refill")
	}
}

// TestPurgeDuringAnInFlightFetchIsNotUndone.
//
// 🔴 AN ERASURE THAT SILENTLY UN-DOES ITSELF IS WORSE THAN ONE THAT FAILS. A fetch may be in flight
// when a tenant is purged — a fence-set rebuild takes as long as its round trips — and its insert
// lands afterwards, putting the tenant's geometry back minutes after the purge reported success.
// Nothing observes that, because the purge already returned a plausible count. The caller that
// asked is still served (the bytes came from its own fetch, and it is mid-request), but the cache
// retains nothing.
func TestPurgeDuringAnInFlightFetchIsNotUndone(t *testing.T) {
	cache := NewGeometryCache(0)
	doc := ngonDocument(0, 6)
	hash := addr(doc)

	entered := make(chan struct{})
	release := make(chan struct{})
	var got Compiled
	var err error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, err = cache.Get(context.Background(), "acme", hash, func(context.Context) ([]byte, error) {
			close(entered)
			<-release
			return doc, nil
		})
	}()

	<-entered
	cache.PurgeTenant("acme")
	close(release)
	wg.Wait()

	if err != nil {
		t.Fatalf("the in-flight caller was refused rather than served: %v", err)
	}
	if !answersInside(t, got, at(0, 0)) {
		t.Error("the in-flight caller was served something other than what it fetched")
	}
	if cache.Entries() != 0 || cache.Vertices() != 0 {
		t.Errorf("the purge was undone by the in-flight fill: %d entries / %d vertices",
			cache.Entries(), cache.Vertices())
	}
	if retained(t, cache, "acme", hash) {
		t.Error("the purged geometry is being served again")
	}
}

// TestOnlyAVerifiedDocumentIsAdmitted is the content-address requirement, and the assertion that
// matters is the LAST one: what the cache serves for an address is the geometry of THAT document.
//
// 🔴 A POISONED ENTRY CANNOT BE REPAIRED BY ANYTHING DOWNSTREAM. A (tenant, hash) entry outlives
// every fence-set version that names it and a hit bypasses fetching entirely, so a wrong document
// filed under an address is never re-read: it answers containment confidently, with somebody
// else's shape, until the process restarts. The reconcile sweep cannot fix it because the sweep
// resolves through this cache.
//
// 🔴 COUNTING IS NOT ENOUGH TO CATCH THIS, WHICH IS WHY THE GEOMETRY IS INTERROGATED. Slice 1 of
// this arc found a mutation that stored the FIRST document under EVERY address; it survived an
// entire suite because every test measured row COUNT, and it would survive any test here that
// checked hit rates, entry counts or "Get returned no error". The fetch below deliberately serves
// the wrong document for the second address — the exact shape of that mutation — and the test
// insists on containment answers rather than counts.
func TestOnlyAVerifiedDocumentIsAdmitted(t *testing.T) {
	docA := ngonDocument(0, 5)
	docB := ngonDocument(60, 5)
	hashA, hashB := addr(docA), addr(docB)
	cache := NewGeometryCache(0)

	var callsA int32
	gotA, err := cache.Get(context.Background(), "acme", hashA, serving(docA, &callsA))
	if err != nil {
		t.Fatalf("Get(A): %v", err)
	}
	if !answersInside(t, gotA, at(0, 0)) || answersInside(t, gotA, at(60, 0)) {
		t.Fatal("A's address did not serve A's geometry")
	}

	// The mutation's shape: whatever address is asked for, hand back the document already held.
	var poisonCalls int32
	if _, err := cache.Get(context.Background(), "acme", hashB, serving(docA, &poisonCalls)); !errors.Is(err, ErrGeometryHashMismatch) {
		t.Fatalf("a document that does not hash to its address was accepted; err = %v", err)
	}
	if poisonCalls != 1 {
		t.Errorf("the poisoned fetch ran %d times, want 1", poisonCalls)
	}
	if cache.Entries() != 1 {
		t.Errorf("the refused document was stored anyway: %d entries", cache.Entries())
	}
	if m := cache.Stats().HashMismatches; m != 1 {
		t.Errorf("HashMismatches = %d, want 1", m)
	}

	// The address is still clean, so the correct document is still admissible under it.
	var callsB int32
	gotB, err := cache.Get(context.Background(), "acme", hashB, serving(docB, &callsB))
	if err != nil {
		t.Fatalf("Get(B) after the refusal: %v", err)
	}
	if !answersInside(t, gotB, at(60, 0)) {
		t.Error("B's address does not serve B's geometry")
	}
	if answersInside(t, gotB, at(0, 0)) {
		t.Error("B's address serves A's geometry; the first document was stored under every address")
	}
	if !answersInside(t, gotA, at(0, 0)) || answersInside(t, gotA, at(60, 0)) {
		t.Error("A's geometry changed underneath its holder")
	}
}

// TestTheAddressIsDerivedTheWayTheArchiveDerivesIt: the cache verifies against device-management's
// own hash, which is the authority. A cache computing a DIFFERENT function of the bytes would
// refuse every correct document — every fence in the instance would fail to resolve, and the
// symptom (fences that stop answering after a redeploy) points nowhere near the hash.
//
// The parity is asserted twice on purpose: once on the function, and once end-to-end by filing a
// document under the address the archive would have minted for it and requiring the cache to
// accept it. The second is what would still fail if the verification moved somewhere else.
func TestTheAddressIsDerivedTheWayTheArchiveDerivesIt(t *testing.T) {
	docs := [][]byte{
		ngonDocument(0, 3),
		ngonDocument(15, 40),
		polygonDocument(square(0, 0, 10, 10), square(4, 4, 6, 6)),
	}
	cache := NewGeometryCache(0)
	for i, doc := range docs {
		theirs := dmmodel.GeoFenceGeometryHash(doc)
		if ours := addr(doc); ours != theirs {
			t.Errorf("document %d: this test's address %s does not match device-management's %s", i, ours, theirs)
		}
		var calls int32
		if _, err := cache.Get(context.Background(), "acme", theirs, serving(doc, &calls)); err != nil {
			t.Errorf("document %d: a document filed under device-management's own address was refused: %v", i, err)
		}
	}
}

// TestTheAddressIsNormalizedNotTrusted: a manifest carries lowercase hex, but a cache that trusted
// the spelling would file the same document twice under two spellings AND — the part that is not
// cosmetic — compare a lowercase computed hash against an uppercase key and refuse every fence.
func TestTheAddressIsNormalizedNotTrusted(t *testing.T) {
	doc := ngonDocument(0, 5)
	lower := addr(doc)
	upper := fmt.Sprintf("%X", func() []byte { b, _ := hex.DecodeString(lower); return b }())

	cache := NewGeometryCache(0)
	var calls int32
	if _, err := cache.Get(context.Background(), "acme", upper, serving(doc, &calls)); err != nil {
		t.Fatalf("an uppercase address was refused: %v", err)
	}
	if _, err := cache.Get(context.Background(), "acme", lower, refusing("must not fetch")); err != nil {
		t.Errorf("the lowercase spelling of the same address missed: %v", err)
	}
	if cache.Entries() != 1 {
		t.Errorf("Entries() = %d, want 1; two spellings of one address became two entries", cache.Entries())
	}
}

// TestStatsTrackTheContents: the gauges are what an operator sees, so they have to survive every
// path that changes the contents rather than only the happy one. A vertex total that drifts —
// un-charged on eviction, double-charged on replacement — turns the bound into a slow leak or a
// cache that evicts itself down to nothing, and either looks like a memory problem somewhere else.
func TestStatsTrackTheContents(t *testing.T) {
	cache := NewGeometryCache(20)
	var calls int32
	docs := []([]byte){ngonDocument(0, 4), ngonDocument(10, 6), ngonDocument(20, 8)}
	for i, doc := range docs {
		if _, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls)); err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
	}
	if s := cache.Stats(); s.Entries != 3 || s.Vertices != 18 || s.Fills != 3 || s.Hits != 0 {
		t.Errorf("after three fills: %+v, want 3 entries / 18 vertices / 3 fills / 0 hits", s)
	}

	if _, err := cache.Get(context.Background(), "acme", addr(docs[0]), refusing("must not fetch")); err != nil {
		t.Fatalf("hit: %v", err)
	}
	if s := cache.Stats(); s.Hits != 1 || s.Fills != 3 || s.Entries != 3 || s.Vertices != 18 {
		t.Errorf("after a hit: %+v, want 1 hit and the contents unchanged", s)
	}

	// A fourth entry of 5 takes the total to 23 against a bound of 20. The 4-vertex entry was just
	// used, so the 6-vertex one is now least-recently-used and goes — which is also why the total
	// lands on 17 rather than on 19: what is un-charged is the cost of the entry that actually
	// left, not the cost of the oldest insert.
	fourth := ngonDocument(30, 5)
	if _, err := cache.Get(context.Background(), "acme", addr(fourth), serving(fourth, &calls)); err != nil {
		t.Fatalf("Get(fourth): %v", err)
	}
	s := cache.Stats()
	if s.Vertices > cache.MaxVertices() {
		t.Errorf("the retained total %d is over the bound of %d", s.Vertices, cache.MaxVertices())
	}
	if s.Vertices != 17 || s.Entries != 3 || s.Evictions != 1 {
		t.Errorf("after eviction: %+v, want 17 vertices / 3 entries / 1 eviction (the 6-vertex entry goes)", s)
	}
	if retained(t, cache, "acme", addr(docs[1])) {
		t.Error("the 6-vertex entry survived; the eviction un-charged a cost it did not remove")
	}

	if removed := cache.PurgeTenant("acme"); removed != 3 {
		t.Fatalf("PurgeTenant removed %d, want 3", removed)
	}
	if s := cache.Stats(); s.Entries != 0 || s.Vertices != 0 {
		t.Errorf("after a full purge: %+v, want 0 entries / 0 vertices", s)
	}
}

// TestABoundOfZeroFallsBackRatherThanRetainingNothing: a cache that retains nothing is correct on
// every functional test — every answer it gives is right — while refetching and recompiling every
// geometry on every use. It is a misconfiguration with no symptom except cost, so it must not be
// expressible.
func TestABoundOfZeroFallsBackRatherThanRetainingNothing(t *testing.T) {
	for _, bound := range []int{0, -1} {
		cache := NewGeometryCache(bound)
		if cache.MaxVertices() != DefaultMaxCachedVertices {
			t.Errorf("NewGeometryCache(%d).MaxVertices() = %d, want the default %d",
				bound, cache.MaxVertices(), DefaultMaxCachedVertices)
		}
		doc := ngonDocument(0, 4)
		var calls int32
		if _, err := cache.Get(context.Background(), "acme", addr(doc), serving(doc, &calls)); err != nil {
			t.Fatalf("Get: %v", err)
		}
		if cache.Entries() != 1 {
			t.Errorf("NewGeometryCache(%d) retained nothing", bound)
		}
	}
}
