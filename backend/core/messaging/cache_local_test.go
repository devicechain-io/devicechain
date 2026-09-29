// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// countingStore is a map-backed cache store that counts the calls reaching it. A miss is
// the real bucket's jetstream.ErrKeyNotFound. It lives here rather than in msgtest because
// msgtest imports this package.
//
// Get hands back the stored slice itself, not a copy, the way a client library may hand
// back a buffer it owns; overwrite rewrites that slice in place, which is how a test shows
// that the in-process tier does not keep a reference to it.
type countingStore struct {
	mu     sync.Mutex
	values map[string][]byte
	putErr error

	// beforeGet and beforeDelete, when set, run at the start of the call. A test blocks
	// one on a channel to hold the call in flight.
	beforeGet    func()
	beforeDelete func()

	gets, puts, deletes atomic.Int64
}

func newCountingStore() *countingStore {
	return &countingStore{values: map[string][]byte{}}
}

func (s *countingStore) Get(_ context.Context, key string) (jetstream.KeyValueEntry, error) {
	s.gets.Add(1)
	if s.beforeGet != nil {
		s.beforeGet()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	if !ok {
		return nil, jetstream.ErrKeyNotFound
	}
	return valueEntry(v), nil
}

func (s *countingStore) Put(_ context.Context, key string, value []byte) (uint64, error) {
	s.puts.Add(1)
	if s.putErr != nil {
		return 0, s.putErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return 1, nil
}

func (s *countingStore) Delete(_ context.Context, key string, _ ...jetstream.KVDeleteOpt) error {
	s.deletes.Add(1)
	if s.beforeDelete != nil {
		s.beforeDelete()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// seed stores a JSON value directly, as another replica's write would.
func (s *countingStore) seed(key, json string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[kvKey(key)] = []byte(json)
}

// overwrite rewrites the stored bytes of key in place, keeping the slice.
func (s *countingStore) overwrite(key string, b byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.values[kvKey(key)]
	for i := range v {
		v[i] = b
	}
}

// localCacheOver is a Cache over store with the production defaults and a clock the test
// moves. It uses only NewCacheOver and the clock field, both of which predate the
// in-process tier, so the tests below compile against the Cache without it and fail there
// by value.
func localCacheOver(store cacheStore) (*Cache, *fakeClock) {
	c := NewCacheOver(store)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now
	return c, clock
}

func mustGet(t *testing.T, c *Cache, key string) (string, bool) {
	t.Helper()
	var v string
	found, err := c.Get(context.Background(), key, &v)
	if err != nil {
		t.Fatalf("Get(%q) = %v", key, err)
	}
	return v, found
}

func mustSet(t *testing.T, c *Cache, key, value string) {
	t.Helper()
	if err := c.Set(context.Background(), key, value); err != nil {
		t.Fatalf("Set(%q) = %v", key, err)
	}
}

// 🔑 THE POINT OF THE TIER, BY VALUE: a second Get of the same key within the TTL makes no
// call to the bucket. Before it, every Get was a round trip, a hit included.
func TestASecondGetWithinTheLocalTTLDoesNotAskTheBucket(t *testing.T) {
	store := newCountingStore()
	c, _ := localCacheOver(store)
	mustSet(t, c, "acme|dev", "v1")

	if v, found := mustGet(t, c, "acme|dev"); !found || v != "v1" {
		t.Fatalf("first Get = (%q, %v), want v1", v, found)
	}
	before := store.gets.Load()
	if v, found := mustGet(t, c, "acme|dev"); !found || v != "v1" {
		t.Fatalf("second Get = (%q, %v), want v1", v, found)
	}
	if got := store.gets.Load() - before; got != 0 {
		t.Errorf("the second Get within the TTL made %d bucket reads, want 0", got)
	}
}

// The same, for a value this replica never wrote: the first Get reads the bucket and keeps
// what it read; the second is answered from memory.
func TestAValueReadFromTheBucketIsKeptForTheNextGet(t *testing.T) {
	store := newCountingStore()
	store.seed("acme|dev", `"v1"`)
	c, _ := localCacheOver(store)

	mustGet(t, c, "acme|dev")
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load(); got != 1 {
		t.Errorf("two Gets of a key another replica wrote made %d bucket reads, want 1", got)
	}
}

// A hit in memory is served while the breaker is open: it never touched the bucket the
// breaker protects. A MISS in memory is still refused, as before.
func TestAnL1HitIsServedWhileTheBreakerIsOpen(t *testing.T) {
	var silent atomic.Bool
	store := &scriptedStore{put: okWrite, delete: okWrite, get: func(ctx context.Context, k string) (jetstream.KeyValueEntry, error) {
		if silent.Load() {
			return silentGet(ctx, k)
		}
		return answeringGet(ctx, k)
	}}
	// Not breakerCache, which turns the in-process tier off: the point here is the tier.
	c, _ := localCacheOver(store)
	c.timeout = 20 * time.Millisecond
	if err := c.Set(context.Background(), "held", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	silent.Store(true)
	var v string
	if _, err := c.Get(context.Background(), "other", &v); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get of a key not held, against a silent store = %v, want the budget running out", err)
	}

	found, err := c.Get(context.Background(), "held", &v)
	if err != nil || !found || v != "v" {
		t.Errorf("Get of a held key while the breaker is open = (%v, %v, %q), want the held value", found, err, v)
	}
	if _, err := c.Get(context.Background(), "other", &v); !errors.Is(err, ErrCacheUnavailable) {
		t.Errorf("Get of a key not held while the breaker is open = %v, want ErrCacheUnavailable", err)
	}
}

// After the TTL, the next Get asks the bucket, exactly once.
func TestAGetAfterTheLocalTTLAsksTheBucketOnce(t *testing.T) {
	store := newCountingStore()
	c, clock := localCacheOver(store)
	mustSet(t, c, "acme|dev", "v1")
	mustGet(t, c, "acme|dev")

	clock.advance(DefaultLocalCacheTTL + time.Nanosecond)
	before := store.gets.Load()
	mustGet(t, c, "acme|dev")
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("two Gets after the TTL made %d bucket reads, want 1 (the first asks, the second is refilled)", got)
	}
}

// 🔴 THE TTL COUNTS FROM WHEN THE VALUE WAS STORED, NOT FROM THE LAST HIT. A key read every
// second is still read from the bucket once its value is five seconds old. If a hit moved
// the expiry, a key read more often than the TTL would never be refetched on this replica,
// and another replica's eviction would never reach it.
func TestAHotKeyIsStillRefetchedEveryLocalTTL(t *testing.T) {
	store := newCountingStore()
	store.seed("acme|dev", `"v1"`)
	c, clock := localCacheOver(store)
	mustGet(t, c, "acme|dev")
	fill := store.gets.Load()

	for i := 0; i < 4; i++ {
		clock.advance(time.Second)
		mustGet(t, c, "acme|dev")
	}
	if got := store.gets.Load() - fill; got != 0 {
		t.Fatalf("hits inside the TTL made %d bucket reads, want 0", got)
	}
	clock.advance(time.Second + time.Nanosecond) // 5 s + 1 ns after the fill
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load() - fill; got != 1 {
		t.Errorf("a Get 5 s after the fill, with a hit every second since, made %d bucket reads, want 1", got)
	}
}

// Delete then Get asks the bucket, and gets the miss.
func TestDeleteThenGetAsksTheBucket(t *testing.T) {
	store := newCountingStore()
	c, _ := localCacheOver(store)
	mustSet(t, c, "acme|dev", "v1")
	mustGet(t, c, "acme|dev")

	if err := c.Delete(context.Background(), "acme|dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	before := store.gets.Load()
	if _, found := mustGet(t, c, "acme|dev"); found {
		t.Error("a Get after Delete found the value")
	}
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("the Get after Delete made %d bucket reads, want 1", got)
	}
}

// A miss is never kept: two Gets of an absent key both ask the bucket.
func TestAMissIsNeverKeptLocally(t *testing.T) {
	store := newCountingStore()
	c, _ := localCacheOver(store)
	mustGet(t, c, "acme|absent")
	mustGet(t, c, "acme|absent")
	if got := store.gets.Load(); got != 2 {
		t.Errorf("two Gets of an absent key made %d bucket reads, want 2", got)
	}
}

// A write the bucket refused is not kept in memory: memory holds only what the bucket
// would return.
func TestAWriteTheBucketRefusedIsNotKeptLocally(t *testing.T) {
	store := newCountingStore()
	store.putErr = errors.New("bucket full")
	c, _ := localCacheOver(store)
	if err := c.Set(context.Background(), "acme|dev", "v1"); err == nil {
		t.Fatal("Set against a refusing bucket returned nil")
	}
	if _, found := mustGet(t, c, "acme|dev"); found {
		t.Error("a value the bucket refused was served")
	}
	if got := store.gets.Load(); got != 1 {
		t.Errorf("the Get after a refused Set made %d bucket reads, want 1", got)
	}
}

// A Set whose Put failed drops what memory held for the key. A failed Put may still have
// landed (a timeout the bucket applied anyway), so the value held from before it is no
// longer known to be what the bucket would return, and the next Get asks the bucket.
func TestAFailedWriteDropsWhatMemoryHeldForTheKey(t *testing.T) {
	store := newCountingStore()
	c, _ := localCacheOver(store)
	mustSet(t, c, "acme|dev", "v1") // held in memory

	store.putErr = errors.New("timeout")
	if err := c.Set(context.Background(), "acme|dev", "v2"); err == nil {
		t.Fatal("Set against a failing bucket returned nil")
	}
	store.seed("acme|dev", `"v2"`) // the write landed after all

	before := store.gets.Load()
	if v, found := mustGet(t, c, "acme|dev"); !found || v != "v2" {
		t.Errorf("Get after a failed Set = (%q, %v), want (\"v2\", true) from the bucket; memory "+
			"answered with the value from before the write", v, found)
	}
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("the Get after a failed Set made %d bucket reads, want 1", got)
	}
}

// Memory is keyed on the caller's whole key, which carries the tenant: a key held for one
// tenant never answers another's.
func TestNoLocalHitCrossesTenants(t *testing.T) {
	store := newCountingStore()
	c, _ := localCacheOver(store)
	mustSet(t, c, "acme|dev", "acme's")
	mustGet(t, c, "acme|dev")

	before := store.gets.Load()
	if v, found := mustGet(t, c, "globex|dev"); found {
		t.Errorf("globex's lookup of dev found %q, which was stored for acme", v)
	}
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("globex's lookup made %d bucket reads, want 1", got)
	}
}

type mapValue struct {
	M map[string]int `json:"m"`
}

// Two callers never share an object, and nothing outside the Cache can change what it
// holds: one caller mutating what it got back does not reach the next, and the client
// library reusing the buffer it returned does not either.
func TestTwoCallersNeverShareAValue(t *testing.T) {
	store := newCountingStore()
	store.seed("acme|dev", `{"m":{"a":1}}`)
	c, _ := localCacheOver(store)

	var first mapValue
	if found, err := c.Get(context.Background(), "acme|dev", &first); err != nil || !found {
		t.Fatalf("first Get = (%v, %v)", found, err)
	}
	first.M["a"] = 99

	// The bucket's buffer is rewritten in place after the fill.
	store.overwrite("acme|dev", ' ')

	var second mapValue
	if found, err := c.Get(context.Background(), "acme|dev", &second); err != nil || !found {
		t.Fatalf("second Get = (%v, %v); memory held a reference to the bucket's buffer", found, err)
	}
	if second.M["a"] != 1 {
		t.Errorf("second caller read a=%d, want 1: it was handed what the first caller mutated", second.M["a"])
	}
	if got := store.gets.Load(); got != 1 {
		t.Errorf("the second Get made %d bucket reads in all, want 1 (it must be a memory hit)", got)
	}
}

// A Get that read the bucket before a Delete landed does not put what it read back into
// memory: the next Get asks the bucket.
func TestAFillRacingADeleteIsDropped(t *testing.T) {
	store := newCountingStore()
	store.seed("acme|dev", `"old"`)
	c, _ := localCacheOver(store)

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store.beforeGet = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	done := make(chan string)
	go func() {
		var v string
		_, _ = c.Get(context.Background(), "acme|dev", &v)
		done <- v
	}()
	<-entered
	// The read is in flight. The Delete runs to completion under it; the store's own map
	// lock keeps the in-flight read's value the old one.
	store.mu.Lock()
	old := append([]byte(nil), store.values[kvKey("acme|dev")]...)
	store.mu.Unlock()
	if err := c.Delete(context.Background(), "acme|dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	store.seed("acme|dev", string(old)) // the in-flight read still sees the pre-delete value
	close(release)
	if v := <-done; v != "old" {
		t.Fatalf("the in-flight Get returned %q, want old", v)
	}
	store.mu.Lock()
	delete(store.values, kvKey("acme|dev"))
	store.mu.Unlock()

	before := store.gets.Load()
	if v, found := mustGet(t, c, "acme|dev"); found {
		t.Errorf("the Get after the Delete found %q: the racing read refilled memory", v)
	}
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("the Get after the Delete made %d bucket reads, want 1", got)
	}
}

// The other interleaving: a Get misses memory and reads the bucket WHILE the Delete is
// still in flight at the bucket. The Delete must clear memory after the bucket, so it
// removes what that Get filled. Clearing memory first would leave the filled old value.
func TestADeleteClearsMemoryAfterTheBucket(t *testing.T) {
	store := newCountingStore()
	store.seed("acme|dev", `"old"`)
	c, _ := localCacheOver(store)

	entered, release := make(chan struct{}), make(chan struct{})
	store.beforeDelete = func() {
		close(entered)
		<-release
	}
	deleted := make(chan struct{})
	go func() {
		_ = c.Delete(context.Background(), "acme|dev")
		close(deleted)
	}()
	<-entered
	if v, found := mustGet(t, c, "acme|dev"); !found || v != "old" {
		t.Fatalf("the Get during the Delete = (%q, %v), want old (the bucket still held it)", v, found)
	}
	close(release)
	<-deleted

	before := store.gets.Load()
	if v, found := mustGet(t, c, "acme|dev"); found {
		t.Errorf("the Get after the Delete found %q in memory", v)
	}
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("the Get after the Delete made %d bucket reads, want 1", got)
	}
}

// A Get that read the bucket before a Set on this replica landed does not replace the new
// value in memory with the one it read.
func TestAFillRacingASetIsDropped(t *testing.T) {
	store := newCountingStore()
	store.seed("acme|dev", `"v1"`)
	c, _ := localCacheOver(store)

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store.beforeGet = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	store.mu.Lock()
	v1 := store.values[kvKey("acme|dev")]
	store.mu.Unlock()
	done := make(chan struct{})
	go func() {
		var v string
		_, _ = c.Get(context.Background(), "acme|dev", &v)
		close(done)
	}()
	<-entered
	mustSet(t, c, "acme|dev", "v2")
	// The in-flight read returns what it would have seen before the Set.
	store.mu.Lock()
	v2 := store.values[kvKey("acme|dev")]
	store.values[kvKey("acme|dev")] = v1
	store.mu.Unlock()
	close(release)
	<-done
	store.mu.Lock()
	store.values[kvKey("acme|dev")] = v2
	store.mu.Unlock()

	if v, _ := mustGet(t, c, "acme|dev"); v != "v2" {
		t.Errorf("after a Set of v2 raced by a read of v1, this replica reads %q, want v2", v)
	}
}

// Memory is bounded by entry count, evicting the least recently used.
func TestTheLocalCopyIsBoundedByEntries(t *testing.T) {
	store := newCountingStore()
	c := newCache("test", store, nil, 0)
	c.local.maxEntries = 2
	ctx := context.Background()
	for _, k := range []string{"a", "b"} {
		if err := c.Set(ctx, k, k); err != nil {
			t.Fatal(err)
		}
	}
	var v string
	_, _ = c.Get(ctx, "a", &v) // a is now the most recently used
	if err := c.Set(ctx, "c", "c"); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.local.len(); n != 2 {
		t.Fatalf("held %d entries, want 2", n)
	}
	before := store.gets.Load()
	_, _ = c.Get(ctx, "a", &v)
	_, _ = c.Get(ctx, "c", &v)
	if got := store.gets.Load() - before; got != 0 {
		t.Errorf("the two most recently used keys made %d bucket reads, want 0", got)
	}
	_, _ = c.Get(ctx, "b", &v)
	if got := store.gets.Load() - before; got != 1 {
		t.Errorf("the evicted key made %d bucket reads, want 1", got)
	}
}

// Memory is bounded by bytes too, keys and per-entry overhead included, and a value too
// large for the cap on its own is not held (nor is the older value it replaced).
func TestTheLocalCopyIsBoundedByBytes(t *testing.T) {
	store := newCountingStore()
	c := newCache("test", store, nil, 0)
	ctx := context.Background()
	// A Set holds json.Marshal's 4 bytes, whose allocation holds 8, and an entry is charged
	// the capacity it holds.
	one := len("k0") + 8 + localEntryOverhead
	c.local.maxBytes = 2 * one
	for _, k := range []string{"k0", "k1", "k2"} {
		if err := c.Set(ctx, k, "xx"); err != nil {
			t.Fatal(err)
		}
	}
	if n, b := c.local.len(); n != 2 || b != 2*one {
		t.Fatalf("held %d entries in %d bytes, want 2 in %d", n, b, 2*one)
	}

	big := make([]byte, 3*one)
	for i := range big {
		big[i] = 'x'
	}
	if err := c.Set(ctx, "k2", string(big)); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.local.len(); n != 1 {
		t.Errorf("held %d entries after an oversized value replaced one, want 1", n)
	}
	before := store.gets.Load()
	var v string
	_, _ = c.Get(ctx, "k2", &v)
	if got := store.gets.Load() - before; got != 1 || v != string(big) {
		t.Errorf("the oversized key made %d bucket reads and read %d bytes, want 1 read of the new value", got, len(v))
	}
}

// The in-process TTL never exceeds the bucket's: a value the bucket would have expired
// after 1 s is not served from memory for 5.
func TestLocalTTLIsCappedByTheBucketTTL(t *testing.T) {
	store := newCountingStore()
	c := newCache("test", store, nil, time.Second)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now
	mustSet(t, c, "acme|dev", "v1")

	clock.advance(time.Second - time.Nanosecond)
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load(); got != 0 {
		t.Fatalf("a Get just inside 1 s made %d bucket reads, want 0", got)
	}
	clock.advance(time.Nanosecond)
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load(); got != 1 {
		t.Errorf("a Get at the 1 s bucket TTL made %d bucket reads, want 1", got)
	}
}

// WithLocalTTL sets it per cache.
func TestWithLocalTTLSetsTheInProcessTTL(t *testing.T) {
	store := newCountingStore()
	c := NewCacheOver(store, WithLocalTTL(time.Minute))
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now
	mustSet(t, c, "acme|dev", "v1")
	clock.advance(30 * time.Second)
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load(); got != 0 {
		t.Errorf("a Get 30 s into a 1 min in-process TTL made %d bucket reads, want 0", got)
	}
}

// WithoutLocalCache turns it off: every Get asks the bucket.
func TestWithoutLocalCacheAsksTheBucketEveryTime(t *testing.T) {
	store := newCountingStore()
	c := NewCacheOver(store, WithoutLocalCache())
	mustSet(t, c, "acme|dev", "v1")
	mustGet(t, c, "acme|dev")
	mustGet(t, c, "acme|dev")
	if got := store.gets.Load(); got != 2 {
		t.Errorf("two Gets with the in-process tier off made %d bucket reads, want 2", got)
	}
}

func TestWithLocalTTLRefusesZero(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WithLocalTTL(0) did not panic; a computed zero must not switch the tier off silently")
		}
	}()
	WithLocalTTL(0)
}

// 🔑 THE DOCUMENTED BOUND, PINNED: another replica sees a Delete within the in-process TTL,
// not at once. Replica A deletes; replica B, which read the value just before, keeps
// serving it until its copy is DefaultLocalCacheTTL old, then reads the miss.
func TestAnotherReplicaSeesADeleteWithinTheLocalTTL(t *testing.T) {
	store := newCountingStore()
	a, _ := localCacheOver(store)
	b, clockB := localCacheOver(store)
	mustSet(t, a, "acme|dev", "v1")
	if v, _ := mustGet(t, b, "acme|dev"); v != "v1" {
		t.Fatalf("replica B read %q, want v1", v)
	}
	if err := a.Delete(context.Background(), "acme|dev"); err != nil {
		t.Fatal(err)
	}
	if _, found := mustGet(t, a, "acme|dev"); found {
		t.Error("replica A, which made the Delete, still found the value")
	}
	clockB.advance(DefaultLocalCacheTTL - time.Nanosecond)
	if v, found := mustGet(t, b, "acme|dev"); !found || v != "v1" {
		t.Errorf("replica B inside the TTL = (%q, %v), want the value it held (the documented bound)", v, found)
	}
	clockB.advance(time.Nanosecond)
	if _, found := mustGet(t, b, "acme|dev"); found {
		t.Error("replica B still found the value once its copy was the TTL old")
	}
}
