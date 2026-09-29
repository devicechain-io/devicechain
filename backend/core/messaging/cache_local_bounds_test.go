// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"bytes"
	"container/list"
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// goSizeClass rounds a small allocation up to the size class the Go allocator serves it
// from (runtime/sizeclasses.go, unchanged from Go 1.20 through 1.26 for the sizes here).
func goSizeClass(n uintptr) uintptr {
	for _, c := range []uintptr{8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208, 224, 240, 256} {
		if n <= c {
			return c
		}
	}
	panic(fmt.Sprintf("goSizeClass: %d is past the table", n))
}

// 🔑 THE BYTE CAP BOUNDS THE HEAP ONLY IF AN ENTRY IS CHARGED WHAT IT COSTS. Beyond its key
// and value, each held entry allocates a list element and an entry struct, and takes a
// slot in the key map. A Go map is at its emptiest (7/16 full) just after it grows, so the
// slot's worst case is its size over 7/16. This is the arithmetic the constant's comment
// gives, done with the running Go's own struct sizes, so it moves if they do.
//
// With the 128 this replaced it fails by value: 48 + 80 + 58 = 186 > 128.
func TestLocalEntryOverheadCoversTheHeap(t *testing.T) {
	element := goSizeClass(unsafe.Sizeof(list.Element{}))
	entry := goSizeClass(unsafe.Sizeof(localEntry{}))
	var key string
	var value *list.Element
	slot := unsafe.Sizeof(key) + unsafe.Sizeof(value) + 1 // + the slot's control byte
	worstSlot := (slot*16 + 6) / 7                        // slot / (7/16), rounded up
	need := element + entry + worstSlot
	t.Logf("list element %d B + entry %d B + map slot at its emptiest %d B = %d B; localEntryOverhead = %d",
		element, entry, worstSlot, need, localEntryOverhead)
	if uintptr(localEntryOverhead) < need {
		t.Errorf("localEntryOverhead = %d, below the %d B an entry costs on the heap beyond its key and "+
			"value: the byte cap undercounts memory by the difference on every entry", localEntryOverhead, need)
	}
}

// An entry is charged its value's capacity, which is what its allocation holds, not its
// length. A 4-byte value read from the bucket is a clone whose capacity is 8.
func TestAnEntryIsChargedItsValuesCapacity(t *testing.T) {
	o := defaultCacheOptions()
	l := newLocalCache(o, nil)
	data := make([]byte, 4, 64)
	l.put("acme|k", data, time.Unix(1_700_000_000, 0))
	if _, got := l.len(); got != len("acme|k")+64+localEntryOverhead {
		t.Errorf("accounted bytes = %d, want %d: key + capacity 64 + overhead", got, len("acme|k")+64+localEntryOverhead)
	}
}

// 🔑 AN ENTRY NOTHING CAN READ ANY MORE IS DROPPED WHEN A NEWER ONE IS STORED. A key read
// less often than the TTL has expired by the time it is read again; without the trim a
// fleet reporting that slowly kept every device's dead entry until the bound pushed it out,
// so memory followed the fleet rather than the devices reporting within the TTL.
//
// It fails on the tree before the trim by value: two entries held and no expired eviction.
func TestExpiredEntriesAreTrimmedAsNewOnesAreStored(t *testing.T) {
	m, _ := cacheMetricsFor(t)
	store := newCountingStore()
	c := newCache("relationships-by-source", store, m, 0)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now

	mustSet(t, c, "acme|a", "va")
	clock.advance(DefaultLocalCacheTTL + time.Second)
	mustSet(t, c, "acme|b", "vb")

	if n, _ := c.local.len(); n != 1 {
		t.Errorf("entries held = %d, want 1: the expired acme|a is still held after a newer store", n)
	}
	if got := testutil.ToFloat64(m.cacheLocalEvictions.WithLabelValues("relationships-by-source", "expired")); got != 1 {
		t.Errorf("expired evictions = %v, want 1", got)
	}
	// The one left is the live one, read without a bucket round trip.
	before := store.gets.Load()
	if v, found := mustGet(t, c, "acme|b"); !found || v != "vb" {
		t.Fatalf("Get(acme|b) = (%q, %v), want vb", v, found)
	}
	if got := store.gets.Load() - before; got != 0 {
		t.Errorf("the live entry made %d bucket reads, want 0", got)
	}
}

// The counterweight: the trim never touches an entry still inside its TTL, the oldest
// included. Trimming by the wrong comparison would pass the test above and fail this one.
func TestTheTrimLeavesLiveEntriesAlone(t *testing.T) {
	m, _ := cacheMetricsFor(t)
	store := newCountingStore()
	c := newCache("relationships-by-source", store, m, 0)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now

	mustSet(t, c, "acme|a", "va")
	clock.advance(DefaultLocalCacheTTL - time.Nanosecond)
	mustSet(t, c, "acme|b", "vb")

	if n, _ := c.local.len(); n != 2 {
		t.Errorf("entries held = %d, want 2: a live entry was trimmed", n)
	}
	if got := testutil.ToFloat64(m.cacheLocalEvictions.WithLabelValues("relationships-by-source", "expired")); got != 0 {
		t.Errorf("expired evictions = %v, want 0", got)
	}
}

// The trim works from the least recently USED end, and stops at the first live entry: an
// expired entry read more recently than a live one stays until a lookup finds it. Pinned
// so that the doc comment's caveat stays true, and so that nobody "fixes" it into a scan
// of the whole list on every store.
func TestTheTrimStopsAtTheFirstLiveEntry(t *testing.T) {
	store := newCountingStore()
	c, clock := localCacheOver(store)

	mustSet(t, c, "acme|old", "v")
	clock.advance(time.Second)
	mustSet(t, c, "acme|live", "v")
	// Read old so it is more recently used than live, then let old expire but not live.
	mustGet(t, c, "acme|old")
	clock.advance(DefaultLocalCacheTTL - time.Second)
	mustSet(t, c, "acme|new", "v")

	if n, _ := c.local.len(); n != 3 {
		t.Errorf("entries held = %d, want 3: the trim went past the live least recently used entry", n)
	}
}

// WithLocalBounds sets both bounds, and the two gauges report what the cache was built
// with. Entries and bytes are different numbers so a swap between them shows.
func TestWithLocalBoundsSetsBothBoundsAndReportsThem(t *testing.T) {
	m, _ := cacheMetricsFor(t)
	c := newCache("relationships-by-source", newCountingStore(), m, 0, WithLocalBounds(3, 1<<20))
	if c.local.maxEntries != 3 || c.local.maxBytes != 1<<20 {
		t.Fatalf("bounds = (%d entries, %d bytes), want (3, %d)", c.local.maxEntries, c.local.maxBytes, 1<<20)
	}
	if got := testutil.ToFloat64(m.cacheLocalMaxEntries.WithLabelValues("relationships-by-source")); got != 3 {
		t.Errorf("kv_cache_local_max_entries = %v, want 3", got)
	}
	if got := testutil.ToFloat64(m.cacheLocalMaxBytes.WithLabelValues("relationships-by-source")); got != 1<<20 {
		t.Errorf("kv_cache_local_max_bytes = %v, want %d", got, 1<<20)
	}
	for i := 0; i < 4; i++ {
		mustSet(t, c, fmt.Sprintf("acme|%d", i), "v")
	}
	if n, _ := c.local.len(); n != 3 {
		t.Errorf("entries held after 4 stores = %d, want 3", n)
	}

	// A cache with no option reports the default bounds.
	d := newCache("scoped-groups-exist", newCountingStore(), m, 0)
	if d.local.maxEntries != defaultLocalMaxEntries || d.local.maxBytes != defaultLocalMaxBytes {
		t.Fatalf("default bounds = (%d, %d), want (%d, %d)", d.local.maxEntries, d.local.maxBytes,
			defaultLocalMaxEntries, defaultLocalMaxBytes)
	}
	if got := testutil.ToFloat64(m.cacheLocalMaxEntries.WithLabelValues("scoped-groups-exist")); got != defaultLocalMaxEntries {
		t.Errorf("default kv_cache_local_max_entries = %v, want %d", got, defaultLocalMaxEntries)
	}
	if got := testutil.ToFloat64(m.cacheLocalMaxBytes.WithLabelValues("scoped-groups-exist")); got != defaultLocalMaxBytes {
		t.Errorf("default kv_cache_local_max_bytes = %v, want %d", got, defaultLocalMaxBytes)
	}
}

func TestWithLocalBoundsRefusesANonPositiveBound(t *testing.T) {
	for _, tc := range []struct{ entries, bytes int }{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithLocalBounds(%d, %d) did not panic", tc.entries, tc.bytes)
				}
			}()
			WithLocalBounds(tc.entries, tc.bytes)
		}()
	}
}

// GetFromMemory answers a held value without asking the bucket, and counts it as the hit
// it is. A key it does not hold is left to Get, which counts the one miss: a lookup made
// through GetFromMemory then Get counts once, as Get alone does.
func TestGetFromMemoryCountsEachLookupOnce(t *testing.T) {
	m, _ := cacheMetricsFor(t)
	store := newCountingStore()
	c := newCache("relationships-by-source", store, m, 0)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now
	lookups := func(result string) float64 {
		return testutil.ToFloat64(m.cacheLocalLookups.WithLabelValues("relationships-by-source", result))
	}

	mustSet(t, c, "acme|held", "v")
	var v string
	if found, err := c.GetFromMemory("acme|held", &v); err != nil || !found || v != "v" {
		t.Fatalf("GetFromMemory(held) = (%v, %v, %q), want the held v", found, err, v)
	}
	if found, err := c.GetFromMemory("acme|absent", &v); err != nil || found {
		t.Fatalf("GetFromMemory(absent) = (%v, %v), want not found", found, err)
	}
	if got := store.gets.Load(); got != 0 {
		t.Errorf("GetFromMemory asked the bucket %d times, want 0", got)
	}
	if hits, misses := lookups("hit"), lookups("miss"); hits != 1 || misses != 0 {
		t.Errorf("after one held and one absent GetFromMemory: hits %v, misses %v; want 1, 0", hits, misses)
	}
	// The absent key's Get is its one counted miss.
	mustGet(t, c, "acme|absent")
	if got := lookups("miss"); got != 1 {
		t.Errorf("misses after the Get that followed = %v, want 1", got)
	}

	// An expired entry is not answered, and is left for the Get that follows to count and
	// remove, exactly as a Get alone would.
	clock.advance(DefaultLocalCacheTTL)
	if found, _ := c.GetFromMemory("acme|held", &v); found {
		t.Fatal("GetFromMemory answered an expired entry")
	}
	if got := testutil.ToFloat64(m.cacheLocalEvictions.WithLabelValues("relationships-by-source", "expired")); got != 0 {
		t.Errorf("expired evictions after GetFromMemory = %v, want 0: the Get that follows removes it", got)
	}
	mustGet(t, c, "acme|held")
	if got := lookups("miss"); got != 2 {
		t.Errorf("misses after the expired key's Get = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.cacheLocalEvictions.WithLabelValues("relationships-by-source", "expired")); got != 1 {
		t.Errorf("expired evictions after the Get = %v, want 1", got)
	}

	// With the in-process tier off there is no memory to answer from.
	off := newCache("scoped-groups-exist", newCountingStore(), m, 0, WithoutLocalCache())
	mustSet(t, off, "acme|k", "v")
	if found, err := off.GetFromMemory("acme|k", &v); err != nil || found {
		t.Errorf("GetFromMemory with the tier off = (%v, %v), want not found", found, err)
	}
}

// BenchmarkLocalEntryHeapCost measures what an entry really costs on the heap, beyond its
// key, for the figures in localEntryOverhead's comment. It is a measurement, not a gate:
// the heap delta depends on how full the map happens to be at the N chosen, which is why
// TestLocalEntryOverheadCoversTheHeap asserts the worst case from struct sizes instead.
//
//	go test -run '^$' -bench LocalEntryHeapCost -benchtime 1x ./
func BenchmarkLocalEntryHeapCost(b *testing.B) {
	for _, vlen := range []int{72, 720} {
		for _, n := range []int{50_000, 100_000, 131_072} {
			b.Run(fmt.Sprintf("value=%d/entries=%d", vlen, n), func(b *testing.B) {
				keys := make([]string, n)
				for i := range keys {
					keys[i] = fmt.Sprintf("acme|%011d", i)
				}
				src := bytes.Repeat([]byte("x"), vlen)
				o := defaultCacheOptions()
				o.maxEntries, o.maxBytes = 1<<30, 1<<40
				var before, after runtime.MemStats
				runtime.GC()
				runtime.GC()
				runtime.ReadMemStats(&before)
				l := newLocalCache(o, nil)
				now := time.Unix(1_700_000_000, 0)
				for i := range keys {
					l.put(keys[i], bytes.Clone(src), now)
				}
				runtime.GC()
				runtime.GC()
				runtime.ReadMemStats(&after)
				_, accounted := l.len()
				real := float64(after.HeapAlloc-before.HeapAlloc) / float64(n)
				b.ReportMetric(real, "heapB/entry")
				b.ReportMetric(float64(accounted-n*len(keys[0]))/float64(n), "accountedB/entry")
				runtime.KeepAlive(l)
				runtime.KeepAlive(keys)
			})
		}
	}
}
