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

// defaultLocalMaxEntries and defaultLocalMaxBytes bound one Cache's in-process copy unless
// it is built WithLocalBounds. They suit a cache keyed by something there are few of (a
// device type, a tenant). A cache keyed per device holds one entry for every device that
// reported within its TTL, so it passes WithLocalBounds sized for the fleet, and whoever
// builds it states what that costs against the service's memory limit, remembering that
// the heap can grow to about twice what is live before a collection.
const (
	defaultLocalMaxEntries = 4096
	defaultLocalMaxBytes   = 4 << 20
)

// localEntryOverhead is what one held entry costs on the heap beyond its key bytes and its
// value's CAPACITY: the list element (48 B), the entry struct (72 B, allocated as 80) and
// its slot in the key map, which a Go map spreads over more memory the emptier it is. A
// map slot here is 25 B (a 16-B string header, an 8-B pointer and a control byte), and a
// map just past growth is 7/16 full, so the worst case is 25 / (7/16), 58 B rounded up.
// That makes 186 B, rounded up to 192. Measured on Go 1.26, linux/amd64, over 50,000 to 131,072
// entries with 16-B keys, everything but the key came to between 163 and 181 B more than
// the value's capacity, depending on how full the map was. The value 128 this replaced
// undercounted an entry by up to 53 B of that. TestLocalEntryOverheadCoversTheHeap holds the
// arithmetic above against the running Go's own struct sizes.
//
// It is counted against the byte cap so that a cache full of tiny values is bounded by
// memory, not only by count, and so that the cap bounds the heap it says it does.
const localEntryOverhead = 192

// CacheOption adjusts one Cache when it is built.
type CacheOption func(*cacheOptions)

type cacheOptions struct {
	localTTL   time.Duration // <= 0: no in-process tier
	maxEntries int
	maxBytes   int
	// decodedFactor and decodedBase give the bytes charged against maxBytes for a decoded
	// value kept by GetCloned: factor times the encoded length, plus base. See
	// WithDecodedCharge.
	decodedFactor, decodedBase int
}

func defaultCacheOptions() cacheOptions {
	return cacheOptions{
		localTTL:   DefaultLocalCacheTTL,
		maxEntries: defaultLocalMaxEntries,
		maxBytes:   defaultLocalMaxBytes,

		decodedFactor: 1,
	}
}

// WithDecodedCharge sets what a decoded value kept for GetCloned costs against this Cache's
// byte bound: factor times the length of its encoded form, plus base. The default is 1 and
// 0, which is right only for a value whose decoded form is about as large as its encoding.
//
// A decoded struct is usually larger than its JSON (headers for every string and slice, a
// pointer for every optional field, size-class rounding), and the heap cannot be measured
// per value on the hot path, so the owner of a cache states the ratio for the types it
// keeps, measured. The caller that sets it should hold the numbers with a test that
// measures the heap of representative values, as device-management does. Both must be
// non-negative and factor positive: it panics otherwise, as WithLocalTTL does.
func WithDecodedCharge(factor, base int) CacheOption {
	if factor <= 0 || base < 0 {
		panic("messaging: WithDecodedCharge needs a positive factor and a non-negative base")
	}
	return func(o *cacheOptions) {
		o.decodedFactor = factor
		o.decodedBase = base
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

// WithLocalBounds sets how many entries, and how many accounted bytes (see
// localEntryOverhead), this Cache keeps in process memory, in place of the 4,096 entries
// and 4 MiB a cache keeps otherwise. Both must be positive: it panics otherwise, as
// WithLocalTTL does, so a computed zero cannot shrink a cache to nothing by accident.
func WithLocalBounds(maxEntries, maxBytes int) CacheOption {
	if maxEntries <= 0 || maxBytes <= 0 {
		panic("messaging: WithLocalBounds needs a positive entry count and byte count")
	}
	return func(o *cacheOptions) {
		o.maxEntries = maxEntries
		o.maxBytes = maxBytes
	}
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
// 🔑 IT HOLDS BYTES, AND NEVER HANDS OUT AN OBJECT. Get decodes every hit into the caller's
// own destination, so no two callers are ever handed the same map or slice, and nothing a
// caller does to what it got back can change what the next caller reads. The bytes
// themselves are never written after they are stored. An entry may also carry the decoded
// form of its bytes for GetCloned, which exists to skip that decode; it keeps the same
// guarantee by returning a deep clone from the caller's own clone function on every hit,
// and never the held value.
//
// A nil *localCache is the tier turned off: every method is a no-op or a miss, which is
// what lets Cache call it without a branch.
type localCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int

	decodedFactor, decodedBase int
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
	// val is the decoded form of data, attached by the first typed read (GetCloned) that
	// decodes it, and nil until then. It is read and written only under localCache.mu, and
	// the pointer is never handed out: every typed read returns a clone of what it points
	// at, so nothing a caller does can reach it. A replaced or invalidated entry takes its
	// val with it, so the decoded form can never outlive the bytes it was decoded from.
	val *decodedValue
}

// localHit is what a lookup in the in-process tier found. entry identifies the entry the
// hit came from, so a decoded value is attached to that entry and to no other that has
// since replaced it under the same key.
type localHit struct {
	data  []byte
	val   *decodedValue
	entry *localEntry
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

		decodedFactor: o.decodedFactor,
		decodedBase:   o.decodedBase,
		order:         list.New(),
		byKey:         map[string]*list.Element{},
		obs:           obs,
	}
}

// get returns the held bytes for key when they have not expired. An expired entry is
// removed on the lookup that finds it. The returned slice must not be written to.
func (l *localCache) get(key string, now time.Time) ([]byte, bool) {
	h, ok := l.getHit(key, now)
	return h.data, ok
}

// getHit is get returning the whole hit.
func (l *localCache) getHit(key string, now time.Time) (localHit, bool) {
	if l == nil {
		return localHit{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.byKey[key]
	if !ok {
		l.obs.localLookup(false)
		return localHit{}, false
	}
	e := el.Value.(*localEntry)
	if !now.Before(e.expires) {
		l.remove(el, "expired")
		l.report()
		l.obs.localLookup(false)
		return localHit{}, false
	}
	// Recency only. The expiry stays where it was set: see DefaultLocalCacheTTL.
	l.order.MoveToFront(el)
	l.obs.localLookup(true)
	return localHit{data: e.data, val: e.val, entry: e}, true
}

// peek is get for a caller that will call get next when this finds nothing: a live entry
// is a hit, counted and moved to the front as get does, but a missing or expired entry is
// left to that get, which counts the miss and removes what expired. So a lookup that
// peeks first is still counted once.
func (l *localCache) peek(key string, now time.Time) ([]byte, bool) {
	h, ok := l.peekHit(key, now)
	return h.data, ok
}

// peekHit is peek returning the whole hit.
func (l *localCache) peekHit(key string, now time.Time) (localHit, bool) {
	if l == nil {
		return localHit{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.byKey[key]
	if !ok {
		return localHit{}, false
	}
	e := el.Value.(*localEntry)
	if !now.Before(e.expires) {
		return localHit{}, false
	}
	l.order.MoveToFront(el)
	l.obs.localLookup(true)
	return localHit{data: e.data, val: e.val, entry: e}, true
}

// attach records val, the decoded form of e's bytes, on e, unless e is no longer the entry
// held for key or already has one. It charges the entry the decoded value's cost
// (WithDecodedCharge) against the byte cap, evicting from the least recently used end to
// make room, and does not attach at all when the entry could not fit with it.
//
// 🔴 THE IDENTITY CHECK IS THE STALE-READ GUARD. A reader that hit an entry, then lost the
// race to a Set that replaced it, holds the OLD bytes' decoded form; attaching that to
// whatever entry the key now names would serve the old value until the TTL. The entry is
// compared by address, not looked up by key alone. An entry that already has a value keeps
// it: both are decodes of the same immutable bytes, so replacing it would only charge the
// entry twice.
func (l *localCache) attach(key string, e *localEntry, val any) {
	if l == nil || e == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.byKey[key]
	if !ok || el.Value.(*localEntry) != e || e.val != nil {
		return
	}
	extra := len(e.data)*l.decodedFactor + l.decodedBase
	if e.size+extra > l.maxBytes {
		return
	}
	e.val = &decodedValue{v: val}
	e.size += extra
	l.bytes += extra
	// Make room from the least recently used end, never evicting the entry just attached
	// to: if it is itself the least recently used, the one before it goes. The loop ends
	// because the entry fits alone (checked above), so while the cap is exceeded another
	// entry exists.
	for l.bytes > l.maxBytes {
		victim := l.order.Back()
		if victim == el {
			victim = victim.Prev()
		}
		l.remove(victim, "capacity")
	}
	l.report()
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
//
// It returns the entry it stored, or nil when it stored nothing (the tier is off, the read
// was in flight across a write, or the value is too large to hold).
func (l *localCache) fill(key string, data []byte, now time.Time, gen uint64) *localEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if gen != l.gen {
		return nil
	}
	l.insert(key, data, now)
	l.report()
	if el, ok := l.byKey[key]; ok {
		return el.Value.(*localEntry)
	}
	return nil
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

// len reports the entries and accounted bytes held, expired entries not yet removed (by the
// lookup that finds them, or a newer store behind them) included.
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
//
// An entry is charged its value's CAPACITY, not its length: the allocation behind a slice
// is rounded up to a size class, and that rounding is heap the cap has to see.
//
// Then entries past their expiry at the least recently used end are dropped. A key read
// less often than the TTL is never answered from memory, because it has expired by the
// time it is read again; without this, a fleet reporting less often than that would fill
// the cache with entries nothing can use, and hold memory for every device in it rather
// than for those that reported within the TTL. The back is only the least recently USED,
// not the oldest stored, so this stops at a live entry even when an expired one sits in
// front of it; that one goes when a lookup finds it, as before. The loop always stops,
// with no check for an empty list: the entry just stored is in it, with an expiry still
// ahead (a value too big to store returned above, before anything was removed here).
func (l *localCache) insert(key string, data []byte, now time.Time) {
	if el, ok := l.byKey[key]; ok {
		l.remove(el, "")
	}
	size := len(key) + cap(data) + localEntryOverhead
	if size > l.maxBytes {
		return
	}
	l.byKey[key] = l.order.PushFront(&localEntry{key: key, data: data, expires: now.Add(l.ttl), size: size})
	l.bytes += size
	for l.order.Len() > l.maxEntries || l.bytes > l.maxBytes {
		l.remove(l.order.Back(), "capacity")
	}
	for back := l.order.Back(); !now.Before(back.Value.(*localEntry).expires); back = l.order.Back() {
		l.remove(back, "expired")
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

// decodedValue boxes an entry's decoded form behind one pointer. The entry is allocated in
// the 80-byte size class (see localEntryOverhead), and an `any` field would take it to 96.
type decodedValue struct{ v any }
