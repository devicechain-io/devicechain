// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func privateRefreshResolver(t *testing.T, fetch func(context.Context, string) (int, error)) *tenantResolver[int] {
	t.Helper()
	r := newTenantResolver(fetch, 0, "test")
	r.group = newRefreshGroup()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.group.close(ctx); err != nil {
			t.Errorf("cleanup could not join refreshes: %v", err)
		}
	})
	return r
}

func waitRefreshStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start within one second")
	}
}

func assertRefreshIdle(t *testing.T, r *tenantResolver[int]) {
	t.Helper()
	r.mu.Lock()
	inflight, slots := len(r.inflight), len(r.sem)
	r.mu.Unlock()
	if inflight != 0 || slots != 0 {
		t.Fatalf("refresh cleanup incomplete: inflight=%d, semaphore slots=%d", inflight, slots)
	}
}

func TestCloseCancelsAnInFlightRefresh(t *testing.T) {
	started := make(chan struct{})
	sawError := make(chan error, 1)
	r := privateRefreshResolver(t, func(ctx context.Context, tenant string) (int, error) {
		close(started)
		<-ctx.Done()
		sawError <- ctx.Err()
		return 0, ctx.Err()
	})
	r.resolve("t1")
	waitRefreshStarted(t, started)
	// The bound is generous on purpose: what proves cancellation is the fetch seeing
	// context.Canceled below, not how fast a loaded runner schedules the join.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.group.close(ctx); err != nil {
		t.Fatalf("close failed to join canceled refresh: %v", err)
	}
	select {
	case err := <-sawError:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetch saw %v, want context.Canceled", err)
		}
	default:
		t.Fatal("close returned before fetch reported cancellation")
	}
	assertRefreshIdle(t, r)
	r.mu.Lock()
	_, cached := r.cache["t1"]
	r.mu.Unlock()
	if cached {
		t.Fatal("shutdown cancellation recorded a negative cache entry for t1")
	}
}

func TestCloseWaitsForARunningRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	r := privateRefreshResolver(t, func(context.Context, string) (int, error) {
		close(started)
		<-release
		return 7, nil
	})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	r.resolve("t1")
	waitRefreshStarted(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- r.group.close(ctx) }()
	// Cancellation proves close has begun before checking that it still waits.
	select {
	case <-r.group.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel the group's context")
	}
	select {
	case err := <-closed:
		t.Fatalf("close returned while a refresh was still running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close failed after refresh was released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not return after refresh was released")
	}
	assertRefreshIdle(t, r)
	r.mu.Lock()
	entry, cached := r.cache["t1"]
	r.mu.Unlock()
	if !cached || !entry.have || entry.val != 7 {
		t.Fatalf("close returned before cache write completed: cached=%v, entry=%+v", cached, entry)
	}
}

func TestCloseIsBoundedByItsContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	r := privateRefreshResolver(t, func(context.Context, string) (int, error) {
		close(started)
		<-release
		return 7, nil
	})
	// Registered after the resolver cleanup so release happens before its join.
	t.Cleanup(func() { close(release) })
	r.resolve("t1")
	waitRefreshStarted(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := r.group.close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close returned %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("bounded close took %v, want it to return at its 100ms deadline", elapsed)
	}
}

func TestNoRefreshStartsAfterClose(t *testing.T) {
	var calls atomic.Int64
	r := privateRefreshResolver(t, func(context.Context, string) (int, error) {
		calls.Add(1)
		return 7, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.group.close(ctx); err != nil {
		t.Fatalf("close of idle group failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if got := r.resolve(fmt.Sprintf("resolve-%d", i)); got != 0 {
			t.Fatalf("resolve after close returned %d, want default 0", got)
		}
		r.resolveOK(fmt.Sprintf("ok-%d", i))
		r.resolveFresh(fmt.Sprintf("fresh-%d", i))
		r.existence(fmt.Sprintf("existence-%d", i))
		// awaitExistence also answers Unknown when its ctx expires, so the wait gets a
		// long deadline and the check is that it returned well before it.
		awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		got := r.awaitExistence(awaitCtx, fmt.Sprintf("await-%d", i))
		awaitCancel()
		if got != ExistenceUnknown {
			t.Fatalf("awaitExistence after close returned %v, want ExistenceUnknown", got)
		}
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Fatalf("awaitExistence waited %v with no refresh running", elapsed)
		}
	}
	assertRefreshIdle(t, r)
	if got := calls.Load(); got != 0 {
		t.Fatalf("fetch called %d times after group closed, want 0", got)
	}
}

func TestStopRefreshesClosesTheProcessGroup(t *testing.T) {
	previous := processRefreshes
	processRefreshes = newRefreshGroup()
	group := processRefreshes
	t.Cleanup(func() {
		defer func() { processRefreshes = previous }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := group.close(ctx); err != nil {
			t.Errorf("cleanup could not join process refreshes: %v", err)
		}
	})
	var calls atomic.Int64
	started := make(chan struct{}, 2)
	sawError := make(chan error, 2)
	r := newTenantResolver(func(ctx context.Context, tenant string) (int, error) {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		sawError <- ctx.Err()
		return 0, ctx.Err()
	}, 0, "test")
	r.resolve("t1")
	waitRefreshStarted(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := StopRefreshes(ctx); err != nil {
		t.Fatalf("StopRefreshes failed: %v", err)
	}
	select {
	case err := <-sawError:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("process refresh saw %v, want context.Canceled", err)
		}
	default:
		t.Fatal("StopRefreshes returned before fetch reported cancellation")
	}
	r.resolve("t2")
	assertRefreshIdle(t, r)
	if got := calls.Load(); got != 1 {
		t.Fatalf("process resolver fetched %d times, want only the pre-stop fetch", got)
	}
}

func TestCloseWhileResolvingConcurrently(t *testing.T) {
	var running atomic.Int64
	started := make(chan struct{}, 1)
	r := privateRefreshResolver(t, func(ctx context.Context, tenant string) (int, error) {
		running.Add(1)
		defer running.Add(-1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Millisecond):
			return 7, nil
		}
	})
	r.Configure(ResolverOptions{MaxConcurrent: 8, RefreshesPerSecond: 1e6, RefreshBurst: 1e6})
	stop := make(chan struct{})
	var workers sync.WaitGroup
	var stopOnce sync.Once
	stopWorkers := func() {
		stopOnce.Do(func() { close(stop) })
		workers.Wait()
	}
	t.Cleanup(stopWorkers)
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					r.resolve(fmt.Sprintf("worker-%d-tenant-%d", worker, i))
				}
			}
		}(worker)
	}
	waitRefreshStarted(t, started)
	// Bound the concurrent workload before closing while workers keep resolving.
	<-time.After(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.group.close(ctx)
	active := running.Load()
	stopWorkers()
	if err != nil {
		t.Fatalf("close failed during concurrent resolving: %v", err)
	}
	if active != 0 {
		t.Fatalf("close returned while %d refreshes were still running", active)
	}
	assertRefreshIdle(t, r)
}
