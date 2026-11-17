// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantflight

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitRenewsDeliveryUntilCapacityReturns(t *testing.T) {
	l := New(2)
	require.True(t, l.TryAcquire("a"))
	require.True(t, l.TryAcquire("b"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	beats := 0
	require.NoError(t, l.Wait(ctx, "a", func() error {
		beats++
		if beats == 2 {
			l.Release("b")
		}
		return nil
	}))
	require.Equal(t, 2, beats, "a wait must periodically renew AckWait")
	require.Equal(t, 2, l.active["a"])
	l.Release("a")
	l.Release("a")
	require.Zero(t, l.total)
}

func TestWaitStopsOnShutdown(t *testing.T) {
	l := New(2)
	require.True(t, l.TryAcquire("a"))
	require.True(t, l.TryAcquire("b"))
	ctx, cancel := context.WithCancel(context.Background())
	require.ErrorIs(t, l.Wait(ctx, "a", func() error {
		cancel()
		return nil
	}), context.Canceled)
	require.Equal(t, 1, l.active["a"], "shutdown must not acquire a slot")
}

func TestWaitWakesOnRelease(t *testing.T) {
	l := New(2)
	require.True(t, l.TryAcquire("a"))
	require.True(t, l.TryAcquire("b"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- l.Wait(ctx, "a", func() error {
			select {
			case started <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	<-started
	l.Release("b")
	require.NoError(t, <-done, "release must wake a waiter before the next heartbeat")
}

func TestShareAndRelease(t *testing.T) {
	for workers, share := range map[int]int{1: 1, 2: 1, 3: 1, 5: 2, 32: 16} {
		l := New(workers)
		for i := 0; i < share; i++ {
			require.True(t, l.TryAcquire("a"))
		}
		require.True(t, l.TryAcquire("b"), "another tenant must have an independent share")
		require.False(t, l.TryAcquire("a"), "workers=%d", workers)
		for i := 0; i < share; i++ {
			l.Release("a")
		}
		l.Release("b")
		require.Empty(t, l.active, "idle tenants must not accumulate")
		require.Zero(t, l.total)
		for i := 0; i < workers; i++ {
			require.True(t, l.TryAcquire("a"), "single tenant must use the entire pool")
		}
		for i := 0; i < workers; i++ {
			l.Release("a")
		}
		require.True(t, l.TryAcquire("a"), "completed work must return capacity")
	}
}

func TestConcurrentAdmissionIsBounded(t *testing.T) {
	l := New(32)
	require.True(t, l.TryAcquire("b"))
	var wg sync.WaitGroup
	results := make(chan bool, 64)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- l.TryAcquire("a")
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for ok := range results {
		if ok {
			admitted++
		}
	}
	require.Equal(t, 16, admitted)
	for i := 0; i < admitted; i++ {
		l.Release("a")
	}
	l.Release("b")
	require.Empty(t, l.active)
}
