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

// scriptedStore is a cache store whose every call is answered by a function the test
// supplies, and which counts the calls that reached it.
type scriptedStore struct {
	get    func(ctx context.Context, key string) (jetstream.KeyValueEntry, error)
	put    func(ctx context.Context, key string) error
	delete func(ctx context.Context, key string) error

	gets, puts, deletes atomic.Int64
}

func (s *scriptedStore) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	s.gets.Add(1)
	return s.get(ctx, key)
}

func (s *scriptedStore) Put(ctx context.Context, key string, _ []byte) (uint64, error) {
	s.puts.Add(1)
	return 1, s.put(ctx, key)
}

func (s *scriptedStore) Delete(ctx context.Context, key string, _ ...jetstream.KVDeleteOpt) error {
	s.deletes.Add(1)
	return s.delete(ctx, key)
}

type valueEntry []byte

func (e valueEntry) Bucket() string                  { return "b" }
func (e valueEntry) Key() string                     { return "k" }
func (e valueEntry) Value() []byte                   { return e }
func (e valueEntry) Revision() uint64                { return 1 }
func (e valueEntry) Created() time.Time              { return time.Time{} }
func (e valueEntry) Delta() uint64                   { return 0 }
func (e valueEntry) Operation() jetstream.KeyValueOp { return jetstream.KeyValuePut }

// silentGet waits for its context, the way a read routed to a silent replica does.
func silentGet(ctx context.Context, _ string) (jetstream.KeyValueEntry, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func answeringGet(context.Context, string) (jetstream.KeyValueEntry, error) {
	return valueEntry(`"v"`), nil
}

func okWrite(context.Context, string) error { return nil }

// fakeClock is the breaker's clock, moved by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// breakerCache is a Cache over store with a 20 ms budget and a clock the test moves.
func breakerCache(store cacheStore) (*Cache, *fakeClock) {
	c := NewCacheOver(store)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now
	c.timeout = 20 * time.Millisecond
	return c, clock
}

func getOnce(c *Cache) (bool, error) {
	var v string
	return c.Get(context.Background(), "key", &v)
}

// reachesStore reads the breaker's state from what a Get does: a Get that reaches the store
// means it is closed (or probing).
func reachesStore(t *testing.T, c *Cache, store *scriptedStore) bool {
	t.Helper()
	before := store.gets.Load()
	_, err := getOnce(c)
	reached := store.gets.Load() > before
	if !reached && !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("a Get that did not reach the store returned %v, want ErrCacheUnavailable", err)
	}
	return reached
}

func TestAReadThatIsNeverAnsweredGivesUpAtTheBudget(t *testing.T) {
	store := &scriptedStore{get: silentGet, put: okWrite, delete: okWrite}
	c, _ := breakerCache(store)

	start := time.Now()
	found, err := getOnce(c)
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) || found {
		t.Fatalf("Get against a silent store = (%v, %v), want (false, context.DeadlineExceeded)", found, err)
	}
	if took > c.timeout+50*time.Millisecond {
		t.Fatalf("Get against a silent store took %s, want about the %s budget", took, c.timeout)
	}
}

func TestAFailedReadBypassesTheCacheWithoutARoundTrip(t *testing.T) {
	store := &scriptedStore{get: silentGet, put: okWrite, delete: okWrite}
	c, _ := breakerCache(store)
	_, _ = getOnce(c)
	calls := store.gets.Load()

	start := time.Now()
	found, err := getOnce(c)
	took := time.Since(start)
	if !errors.Is(err, ErrCacheUnavailable) || found {
		t.Fatalf("Get inside the bypass = (%v, %v), want (false, ErrCacheUnavailable)", found, err)
	}
	if took > 5*time.Millisecond {
		t.Errorf("a bypassed Get took %s; it must not wait at all", took)
	}
	if store.gets.Load() != calls {
		t.Errorf("a bypassed Get reached the store (%d calls, want %d)", store.gets.Load(), calls)
	}
	if err := c.Set(context.Background(), "key", "v"); !errors.Is(err, ErrCacheUnavailable) {
		t.Errorf("Set inside the bypass = %v, want ErrCacheUnavailable", err)
	}
	if store.puts.Load() != 0 {
		t.Errorf("a bypassed Set reached the store")
	}
}

func TestAProbeThatSucceedsClosesTheBreaker(t *testing.T) {
	var answering atomic.Bool
	store := &scriptedStore{put: okWrite, delete: okWrite, get: func(ctx context.Context, k string) (jetstream.KeyValueEntry, error) {
		if answering.Load() {
			return answeringGet(ctx, k)
		}
		return silentGet(ctx, k)
	}}
	c, clock := breakerCache(store)
	_, _ = getOnce(c)
	if reachesStore(t, c, store) {
		t.Fatal("the breaker did not open on a timed-out read")
	}

	clock.advance(c.bypass)
	answering.Store(true)
	for i := 0; i < 3; i++ {
		before := store.gets.Load()
		found, err := getOnce(c)
		if err != nil || !found || store.gets.Load() != before+1 {
			t.Fatalf("read %d after the bypass = (%v, %v), reached store: %v; want the value from the store",
				i, found, err, store.gets.Load() != before)
		}
	}
}

func TestOnlyOneProbeRunsAndAFailedProbeReopens(t *testing.T) {
	release := make(chan struct{})
	var holding atomic.Bool
	store := &scriptedStore{put: okWrite, delete: okWrite, get: func(ctx context.Context, k string) (jetstream.KeyValueEntry, error) {
		if holding.Load() {
			<-release
			return nil, context.DeadlineExceeded
		}
		return silentGet(ctx, k)
	}}
	c, clock := breakerCache(store)
	_, _ = getOnce(c)
	clock.advance(c.bypass)

	holding.Store(true)
	probeDone := make(chan error)
	go func() {
		_, err := getOnce(c)
		probeDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for store.gets.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the probe never reached the store")
		}
		time.Sleep(time.Millisecond)
	}
	if reachesStore(t, c, store) {
		t.Fatal("a second caller reached the store while the probe was in flight")
	}
	close(release)
	if err := <-probeDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe returned %v, want its timeout", err)
	}
	holding.Store(false)
	if reachesStore(t, c, store) {
		t.Fatal("a failed probe did not reopen the breaker")
	}
}

// Several readers share a Cache. A read admitted before the breaker opened and answered
// after it must not close it: only the probe says the cache is answering again.
func TestASuccessAdmittedBeforeTheBreakerOpenedDoesNotCloseIt(t *testing.T) {
	release := make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	store := &scriptedStore{put: okWrite, delete: okWrite, get: func(ctx context.Context, k string) (jetstream.KeyValueEntry, error) {
		if first.CompareAndSwap(true, false) {
			// The read that lands on a healthy replica, and is slow to come back.
			<-release
			return answeringGet(ctx, k)
		}
		return silentGet(ctx, k)
	}}
	c, _ := breakerCache(store)

	inflight := make(chan error)
	go func() {
		_, err := getOnce(c)
		inflight <- err
	}()
	for store.gets.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	if _, err := getOnce(c); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second read = %v, want a timeout that opens the breaker", err)
	}
	close(release)
	if err := <-inflight; err != nil {
		t.Fatalf("the in-flight read = %v, want its answer", err)
	}
	if reachesStore(t, c, store) {
		t.Fatal("a read admitted before the breaker opened closed it by succeeding afterwards")
	}
}

func TestAnEvictionIsNeverSkippedAndGetsItsOwnBudget(t *testing.T) {
	var deadlineLeft time.Duration
	store := &scriptedStore{get: silentGet, put: okWrite, delete: func(ctx context.Context, _ string) error {
		if d, ok := ctx.Deadline(); ok {
			deadlineLeft = time.Until(d)
		}
		return ctx.Err()
	}}
	c, _ := breakerCache(store)
	_, _ = getOnce(c)
	if reachesStore(t, c, store) {
		t.Fatal("the breaker did not open")
	}

	if err := c.Delete(context.Background(), "key"); err != nil {
		t.Fatalf("Delete while bypassed = %v, want nil", err)
	}
	if store.deletes.Load() != 1 {
		t.Fatalf("Delete while bypassed reached the store %d times, want 1: a skipped eviction leaves a "+
			"stale entry that is served once the cache is read again", store.deletes.Load())
	}
	if deadlineLeft < cacheDeleteTimeout-time.Second {
		t.Errorf("the eviction was given %s, want the %s eviction budget, not the read budget",
			deadlineLeft, cacheDeleteTimeout)
	}
}

func TestAnEvictionOutlivesACallerThatGaveUp(t *testing.T) {
	var sawCancelled atomic.Bool
	store := &scriptedStore{get: answeringGet, put: okWrite, delete: func(ctx context.Context, _ string) error {
		if ctx.Err() != nil {
			sawCancelled.Store(true)
		}
		return ctx.Err()
	}}
	c, _ := breakerCache(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Delete(ctx, "key"); err != nil {
		t.Fatalf("Delete with a cancelled caller = %v, want nil", err)
	}
	if store.deletes.Load() != 1 || sawCancelled.Load() {
		t.Fatalf("the eviction reached the store %d times, cancelled=%v; want once, with a context its "+
			"caller cannot cancel", store.deletes.Load(), sawCancelled.Load())
	}
}

func TestAMissOrAnUndecodableValueDoesNotOpenTheBreaker(t *testing.T) {
	var answer atomic.Value
	store := &scriptedStore{put: okWrite, delete: okWrite, get: func(context.Context, string) (jetstream.KeyValueEntry, error) {
		if e, _ := answer.Load().(valueEntry); e != nil {
			return e, nil
		}
		return nil, jetstream.ErrKeyNotFound
	}}
	c, _ := breakerCache(store)

	if found, err := getOnce(c); found || err != nil {
		t.Fatalf("a miss = (%v, %v), want (false, nil)", found, err)
	}
	if !reachesStore(t, c, store) {
		t.Fatal("a miss opened the breaker; a miss is a healthy answer")
	}
	answer.Store(valueEntry(`{not json`))
	var v string
	if _, err := c.Get(context.Background(), "key", &v); err == nil {
		t.Fatal("an undecodable value returned no error")
	}
	if !reachesStore(t, c, store) {
		t.Fatal("a decode error opened the breaker; the bucket answered")
	}
}

func TestAnErrorTheBucketAnswersWithDoesNotOpenTheBreaker(t *testing.T) {
	full := &jetstream.APIError{Code: 503, ErrorCode: 10077, Description: "maximum bytes exceeded"}
	store := &scriptedStore{get: answeringGet, delete: okWrite, put: func(context.Context, string) error { return full }}
	c, _ := breakerCache(store)

	if err := c.Set(context.Background(), "key", "v"); !errors.Is(err, full) {
		t.Fatalf("Set into a full bucket = %v, want the bucket's refusal", err)
	}
	if !reachesStore(t, c, store) {
		t.Fatal("a full bucket refusing a write opened the breaker, bypassing every read of a bucket that is fine")
	}
}

func TestACallerThatGivesUpDoesNotOpenTheBreaker(t *testing.T) {
	store := &scriptedStore{get: silentGet, put: okWrite, delete: okWrite}
	c, _ := breakerCache(store)
	logs := captureLogs(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	var v string
	start := time.Now()
	_, err := c.Get(ctx, "key", &v)
	if took := time.Since(start); took >= c.timeout {
		t.Fatalf("Get with a 5 ms caller deadline took %s; the caller's sooner deadline must win", took)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get past the caller's deadline = %v, want context.DeadlineExceeded", err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := c.Get(cancelled, "key", &v); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get with a cancelled caller = %v, want context.Canceled", err)
	}

	store.get = answeringGet
	if !reachesStore(t, c, store) {
		t.Fatal("a caller giving up opened the breaker; one slow client would switch the cache off for everyone")
	}
	if n := len(logLines(t, logs.String(), stoppedAnswering)); n != 0 {
		t.Errorf("a caller giving up logged %d %q lines, want none", n, stoppedAnswering)
	}
}
