// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// constLimit is a resolver that returns the same ceiling for every tenant.
func constLimit(rps float64, burst int) func(string) (float64, int) {
	return func(string) (float64, int) { return rps, burst }
}

// A tenant may burst up to its burst size, then is denied once the bucket is
// drained (the sustained rate is low enough that no token refills within the
// test's frozen clock).
func TestTenantRateLimiter_BurstThenDeny(t *testing.T) {
	l := NewTenantRateLimiter(constLimit(1, 3))
	// Freeze the clock so no tokens refill mid-test.
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("acme"), "1st within burst")
	assert.True(t, l.Allow("acme"), "2nd within burst")
	assert.True(t, l.Allow("acme"), "3rd within burst")
	assert.False(t, l.Allow("acme"), "4th exceeds burst")
}

// Wait admits the burst immediately, then — with the bucket drained and the next
// token far in the future — sheds fast when the required delay exceeds the wait
// budget (ctx deadline), consuming nothing. Uses the real clock (not the frozen
// l.now) because Wait blocks against the limiter's own wall clock.
func TestTenantRateLimiter_WaitAdmitsThenSheds(t *testing.T) {
	l := NewTenantRateLimiter(constLimit(0.001, 1)) // 1 burst token; next token ~1000s away

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	assert.NoError(t, l.Wait(ctx, "acme"), "burst token admitted immediately")
	cancel()

	start := time.Now()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	err := l.Wait(ctx, "acme")
	cancel()
	assert.Error(t, err, "over-budget wait sheds")
	assert.Less(t, time.Since(start), time.Second, "shed fast rather than blocking to the far-off token")
}

// Wait blocks for a token that frees WITHIN the budget, then admits — the smoothing
// behavior that lets a brief burst just over the rate pace out instead of shedding.
func TestTenantRateLimiter_WaitBlocksThenAdmits(t *testing.T) {
	l := NewTenantRateLimiter(constLimit(100, 1)) // one token every 10ms

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	assert.NoError(t, l.Wait(ctx, "acme"), "burst token admitted immediately")
	cancel()

	start := time.Now()
	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	err := l.Wait(ctx, "acme")
	cancel()
	assert.NoError(t, err, "a token refilled within the budget")
	assert.GreaterOrEqual(t, time.Since(start), 5*time.Millisecond, "waited for the refilled token")
}

// Each tenant has an independent bucket: one tenant draining its allowance does
// not deny another.
func TestTenantRateLimiter_PerTenantIsolation(t *testing.T) {
	l := NewTenantRateLimiter(constLimit(1, 2))
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

// The resolver's per-tenant ceiling is honored: different tenants get different
// burst allowances from the same limiter.
func TestTenantRateLimiter_PerTenantOverride(t *testing.T) {
	l := NewTenantRateLimiter(func(tenant string) (float64, int) {
		if tenant == "vip" {
			return 1, 5 // higher burst for the vip tenant
		}
		return 1, 1 // platform default for everyone else
	})
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	// default tenant: burst of 1
	assert.True(t, l.Allow("acme"))
	assert.False(t, l.Allow("acme"))

	// vip tenant: burst of 5
	for i := 0; i < 5; i++ {
		assert.True(t, l.Allow("vip"), "vip within its raised burst")
	}
	assert.False(t, l.Allow("vip"), "vip drained at its raised burst")
}

// When the resolver's returned ceiling changes for an already-created bucket, the
// bucket is retuned in place: a raised burst grants headroom the tenant then
// accumulates into over time (token-bucket semantics — a raise adds capacity, not
// instant tokens), rather than requiring the limiter to be recreated.
func TestTenantRateLimiter_RetuneOnChange(t *testing.T) {
	burst := 1
	l := NewTenantRateLimiter(func(string) (float64, int) { return 1, burst }) // 1 token/sec
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("acme"), "1st within burst of 1")
	assert.False(t, l.Allow("acme"), "drained at burst of 1")

	// Raise the override; the next admission retunes the existing bucket to burst 3.
	burst = 3
	l.Allow("acme") // triggers the in-place SetBurst; result is immaterial here

	// With the raised ceiling in place, the bucket now accumulates up to 3 tokens.
	now = now.Add(10 * time.Second)
	assert.True(t, l.Allow("acme"))
	assert.True(t, l.Allow("acme"))
	assert.True(t, l.Allow("acme"), "3 tokens available under the raised burst")
	assert.False(t, l.Allow("acme"), "drained at the raised burst of 3")
}

// A refill of tokens over elapsed time lets a previously-drained tenant proceed
// again — the bucket is not a fixed once-per-process budget.
func TestTenantRateLimiter_RefillsOverTime(t *testing.T) {
	l := NewTenantRateLimiter(constLimit(10, 1)) // 10/s => one token every 100ms
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
	l := NewTenantRateLimiter(constLimit(1, 2))
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
	l := NewTenantRateLimiter(constLimit(1000, 1000))
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

// AllowAt meters a message against the time the tenant SENT it, so a consumer
// replaying a durable backlog admits what the tenant actually sent rather than
// what its own drain rate looks like (ADR-030 I4).
func TestAllowAtMetersOnSendTimeNotArrivalTime(t *testing.T) {
	l := NewTenantRateLimiter(func(string) (float64, int) { return 100, 10 })

	// An hour-old backlog the tenant sent at exactly its ceiling, arriving at once.
	base := time.Now().Add(-time.Hour)
	admitted := 0
	for i := 0; i < 500; i++ {
		if l.AllowAt("acme", base.Add(time.Duration(i)*10*time.Millisecond)) {
			admitted++
		}
	}
	if admitted != 500 {
		t.Errorf("a compliant backlog must be admitted in full, got %d/500", admitted)
	}
}

// The counterweight: metering on send time must not become a way to exceed the
// ceiling. A tenant who genuinely sent above it is shed by the same amount the
// live path would shed, so being replayed buys nothing.
func TestAllowAtStillShedsTrafficSentAboveTheCeiling(t *testing.T) {
	l := NewTenantRateLimiter(func(string) (float64, int) { return 100, 10 })

	base := time.Now().Add(-time.Hour)
	admitted := 0
	for i := 0; i < 500; i++ { // 1000/s against a 100/s ceiling
		if l.AllowAt("acme", base.Add(time.Duration(i)*time.Millisecond)) {
			admitted++
		}
	}
	if admitted > 70 {
		t.Errorf("over-ceiling traffic must still be shed, admitted %d/500", admitted)
	}
}

// A zero send time means "now", so a caller with no send time to offer degrades
// to the live behaviour rather than to unmetered.
func TestAllowAtTreatsAZeroSendTimeAsNow(t *testing.T) {
	l := NewTenantRateLimiter(func(string) (float64, int) { return 100, 10 })

	admitted := 0
	for i := 0; i < 500; i++ {
		if l.AllowAt("acme", time.Time{}) {
			admitted++
		}
	}
	if admitted > 20 {
		t.Errorf("a zero send time must meter at now, not bypass the ceiling; admitted %d/500", admitted)
	}
}

// A send time in the FUTURE is clamped to now, so a broker with a fast clock
// cannot mint tokens by claiming its messages were sent later than they were.
func TestAllowAtClampsAFutureSendTimeToNow(t *testing.T) {
	l := NewTenantRateLimiter(func(string) (float64, int) { return 100, 10 })

	future := time.Now().Add(24 * time.Hour)
	admitted := 0
	for i := 0; i < 500; i++ {
		if l.AllowAt("acme", future) {
			admitted++
		}
	}
	if admitted > 20 {
		t.Errorf("a future send time must be clamped to now; admitted %d/500", admitted)
	}
}
