// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantflight

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShareAndRelease(t *testing.T) {
	for workers, share := range map[int]int{1: 1, 2: 1, 3: 1, 5: 2, 32: 16} {
		l := New(workers)
		for i := 0; i < share; i++ {
			require.True(t, l.TryAcquire("a"))
		}
		require.False(t, l.TryAcquire("a"), "workers=%d", workers)
		require.True(t, l.TryAcquire("b"), "another tenant must have an independent share")
		for i := 0; i < share; i++ {
			l.Release("a")
		}
		l.Release("b")
		require.Empty(t, l.active, "idle tenants must not accumulate")
		require.True(t, l.TryAcquire("a"), "completed work must return capacity")
	}
}

func TestConcurrentAdmissionIsBounded(t *testing.T) {
	l := New(32)
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
	require.Empty(t, l.active)
}
