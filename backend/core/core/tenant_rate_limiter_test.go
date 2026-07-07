// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A tenant may burst up to its burst size, then is denied once the bucket is
// drained (the sustained rate is low enough that no token refills within the
// test's frozen clock).
func TestTenantRateLimiter_BurstThenDeny(t *testing.T) {
	l := NewTenantRateLimiter(1, 3)
	// Freeze the clock so no tokens refill mid-test.
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("acme"), "1st within burst")
	assert.True(t, l.Allow("acme"), "2nd within burst")
	assert.True(t, l.Allow("acme"), "3rd within burst")
	assert.False(t, l.Allow("acme"), "4th exceeds burst")
}

// Each tenant has an independent bucket: one tenant draining its allowance does
// not deny another.
func TestTenantRateLimiter_PerTenantIsolation(t *testing.T) {
	l := NewTenantRateLimiter(1, 2)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("acme"))
	assert.True(t, l.Allow("acme"))
	assert.False(t, l.Allow("acme"), "acme is now drained")

	// beta is untouched by acme's flood.
	assert.True(t, l.Allow("beta"))
	assert.True(t, l.Allow("beta"))
	assert.False(t, l.Allow("beta"), "beta drains independently")
}

// A refill of tokens over elapsed time lets a previously-drained tenant proceed
// again — the bucket is not a fixed once-per-process budget.
func TestTenantRateLimiter_RefillsOverTime(t *testing.T) {
	l := NewTenantRateLimiter(10, 1) // 10/s => one token every 100ms
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("acme"))
	assert.False(t, l.Allow("acme"), "burst of 1 is drained")

	now = now.Add(150 * time.Millisecond) // > one refill interval
	assert.True(t, l.Allow("acme"), "a token has refilled")
}

// A bucket untouched for longer than the idle TTL is evicted, so the map does
// not grow with every tenant ever seen; an evicted tenant transparently gets a
// fresh full bucket on its next request.
func TestTenantRateLimiter_IdleEviction(t *testing.T) {
	l := NewTenantRateLimiter(1, 2)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("acme"))
	assert.Len(t, l.buckets, 1)

	// Advance past both the sweep interval and the idle TTL, then touch a
	// different tenant to trigger the sweep. acme should be evicted.
	now = now.Add(l.idleTTL + l.sweepInterval + time.Second)
	assert.True(t, l.Allow("beta"))

	_, acmePresent := l.buckets["acme"]
	assert.False(t, acmePresent, "idle acme bucket should be evicted")
	_, betaPresent := l.buckets["beta"]
	assert.True(t, betaPresent, "active beta bucket should remain")
}

// Allow is safe under concurrent callers (run with -race).
func TestTenantRateLimiter_ConcurrentAllow(t *testing.T) {
	l := NewTenantRateLimiter(1000, 1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.Allow("acme")
				l.Allow("beta")
			}
		}()
	}
	wg.Wait()
}
