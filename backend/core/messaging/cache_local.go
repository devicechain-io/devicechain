// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"container/list"
	"sync"
	"time"
)

// DefaultLocalCacheTTL is how long a Cache keeps a value in process memory before it asks
// the bucket again. It is never longer than the bucket's own TTL: NewCache caps it there.
//
// 🔑 THE CLOCK STARTS WHEN THE VALUE IS STORED, NOT WHEN IT WAS LAST READ. A hit moves an
// entry to the front of the eviction order and leaves its expiry alone. If a hit extended
// it, a key read more often than once per TTL would never be read from the bucket again on
// a replica that did not make the change, and another replica's eviction would never reach
// it.
const DefaultLocalCacheTTL = 5 * time.Second

// defaultLocalMaxEntries and defaultLocalMaxBytes bound one Cache's in-process copy. With
// the five device-management caches that is at most 20 MiB of accounted bytes, against a
// 256 MiB area memory limit; the heap can hold up to about twice that before a collection.
const (
	defaultLocalMaxEntries = 4096
	defaultLocalMaxBytes   = 4 << 20
)

// localEntryOverhead is what one held entry costs beyond its key and value bytes: the list
// element, the entry struct and the map slot, rounded up. It is counted against the byte
// cap so that a cache full of tiny values is bounded by memory, not only by count.
const localEntryOverhead = 128

// CacheOption adjusts one Cache when it is built.
type CacheOption func(*cacheOptions)

type cacheOptions struct {
	localTTL   time.Duration // <= 0: no in-process tier
	maxEntries int
	maxBytes   int
}

func defaultCacheOptions() cacheOptions {
	return cacheOptions{
		localTTL:   DefaultLocalCacheTTL,
		maxEntries: defaultLocalMaxEntries,
		maxBytes:   defaultLocalMaxBytes,
	}
}

// WithLocalTTL sets how long this Cache keeps a value in process memory. It is still capped
// at the bucket TTL. A d of zero or less panics: turning the tier off is WithoutLocalCache,
// said by name, so a computed zero cannot switch it off by accident.
func WithLocalTTL(d time.Duration) CacheOption {
	if d <= 0 {
		panic("messaging: WithLocalTTL needs a positive duration; use WithoutLocalCache to turn the in-process tier off")
	}
	return func(o *cacheOptions) { o.localTTL = d }
}

// WithoutLocalCache turns the in-process tier off, so every Get asks the bucket. It is for
// a cache whose readers need a write made through another replica to be visible at once,
// which the in-process tier delays by up to its TTL.
func WithoutLocalCache() CacheOption {
	return func(o *cacheOptions) { o.localTTL = 0 }
}

// localCache is the in-process tier of a Cache: a least-recently-used list of ENCODED
// values, each with its own expiry.
//
// 🔑 IT HOLDS BYTES, NOT OBJECTS. Every hit is decoded into the caller's own destination,
// so no two callers are ever handed the same map or slice, and nothing a caller does to
// what it got back can change what the next caller reads. The bytes themselves are never
// written after they are stored.
//
// A nil *localCache is the tier turned off: every method is a no-op or a miss, which is
// what lets Cache call it without a branch.
type localCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int
	// gen is bumped by every write to this tier made on behalf of the bucket changing (a
	// Set, a Delete). A Get records it before asking the bucket and fills only if it has
	// not moved, so a read that was in flight across a change on this replica cannot put
	// the value from before the change back.
	//
	// It is one counter for the whole cache, not one per key, and that is accepted: a write
	// to any key (every cache-aside fill after a database read is a Set) drops the fills of
	// reads in flight for OTHER keys too. Those reads still return their value; only the
	// copy in memory is skipped, and the next Get of that key costs one bucket read. That is
	// hit rate lost during a cold-start burst of misses, never a wrong answer.
	gen   uint64
	order *list.List // front = most recently used; elements hold *localEntry
	byKey map[string]*list.Element
	obs   *cacheObserver // nil-safe
}

type localEntry struct {
	key     string
	data    []byte
	expires time.Time
	size    int
}

// newLocalCache returns the in-process tier, or nil when o turns it off.
func newLocalCache(o cacheOptions, obs *cacheObserver) *localCache {
	if o.localTTL <= 0 {
		return nil
	}
	return &localCache{
		ttl:        o.localTTL,
		maxEntries: o.maxEntries,
		maxBytes:   o.maxBytes,
		order:      list.New(),
		byKey:      map[string]*list.Element{},
		obs:        obs,
	}
}

// get returns the held bytes for key when they have not expired. An expired entry is
// removed on the lookup that finds it. The returned slice must not be written to.
func (l *localCache) get(key string, now time.Time) ([]byte, bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.byKey[key]
	if !ok {
		l.obs.localLookup(false)
		return nil, false
	}
	e := el.Value.(*localEntry)
	if !now.Before(e.expires) {
		l.remove(el, "expired")
		l.report()
		l.obs.localLookup(false)
		return nil, false
	}
	// Recency only. The expiry stays where it was set: see DefaultLocalCacheTTL.
	l.order.MoveToFront(el)
	l.obs.localLookup(true)
	return e.data, true
}

// generation is read by a Get before it asks the bucket, and handed back to fill.
func (l *localCache) generation() uint64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gen
}

// fill stores what a Get read from the bucket, unless a Set or Delete on this Cache ran
// while that read was in flight (gen moved), in which case the value may be the one the
// write replaced and is dropped. The next Get asks the bucket, which costs one read.
func (l *localCache) fill(key string, data []byte, now time.Time, gen uint64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if gen != l.gen {
		return
	}
	l.insert(key, data, now)
	l.report()
}

// put stores what a Set wrote to the bucket, and moves the generation on so that a fill
// from a read that started before the write cannot replace it with an older value.
func (l *localCache) put(key string, data []byte, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gen++
	l.insert(key, data, now)
	l.report()
}

// invalidate removes key, and moves the generation on so that a read in flight across the
// Delete does not refill it.
func (l *localCache) invalidate(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gen++
	if el, ok := l.byKey[key]; ok {
		l.remove(el, "deleted")
	}
	l.report()
}

// len reports the entries and accounted bytes held, expired entries not yet removed
// included.
func (l *localCache) len() (entries, bytes int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len(), l.bytes
}

// insert replaces any entry for key and evicts from the least recently used end until both
// caps hold. A value too large for the byte cap on its own is not held at all, and the
// entry it would have replaced is gone too, so an older value is never left in its place.
// Called with mu held.
func (l *localCache) insert(key string, data []byte, now time.Time) {
	if el, ok := l.byKey[key]; ok {
		l.remove(el, "")
	}
	size := len(key) + len(data) + localEntryOverhead
	if size > l.maxBytes {
		return
	}
	l.byKey[key] = l.order.PushFront(&localEntry{key: key, data: data, expires: now.Add(l.ttl), size: size})
	l.bytes += size
	for l.order.Len() > l.maxEntries || l.bytes > l.maxBytes {
		l.remove(l.order.Back(), "capacity")
	}
}

// remove drops one entry. reason is the eviction label, or "" for a replacement, which is
// not an eviction. Called with mu held.
func (l *localCache) remove(el *list.Element, reason string) {
	e := el.Value.(*localEntry)
	l.order.Remove(el)
	delete(l.byKey, e.key)
	l.bytes -= e.size
	if reason != "" {
		l.obs.localEvicted(reason)
	}
}

// report publishes the size gauges. Called with mu held.
func (l *localCache) report() {
	l.obs.localSize(l.order.Len(), l.bytes)
}
