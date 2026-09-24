// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// These tests pin the bounded pool AllowUntrusted keeps for tenant names the platform
// has not authenticated, and the counting of admissions metered at the platform default.

// sourced returns a resolver serving one ceiling for every tenant, with the Source the
// sourceOf func assigns each tenant.
func sourced(rps float64, burst int, sourceOf func(string) CeilingSource) TenantCeilingResolver {
	return func(tenant string) TenantCeiling {
		return TenantCeiling{RatePerSecond: rps, Burst: burst, Source: sourceOf(tenant)}
	}
}

func allPending(string) CeilingSource { return CeilingPending }

// drain calls admit until it has been refused 3 times in a row at a frozen instant, and
// returns how many it admitted.
func drain(admit func() bool) int {
	admitted, refused := 0, 0
	for refused < 3 && admitted < 100000 {
		if admit() {
			admitted++
			refused = 0
		} else {
			refused++
		}
	}
	return admitted
}

func TestUntrustedNamesGetOwnBucketsOnlyUpToThePool(t *testing.T) {
	const burst = 5
	now := time.Unix(1_000_000, 0)
	overflow := prometheus.NewCounter(prometheus.CounterOpts{Name: "overflow_probe_total"})
	l := NewTenantRateLimiter(sourced(1, burst, allPending), WithOverflowAdmissions(overflow))
	l.now = func() time.Time { return now }
	l.maxPooled = 4

	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("pooled-%d", i)
		if got := drain(func() bool { return l.AllowUntrusted(name) }); got != burst {
			t.Fatalf("%s admitted %d; each pooled name gets its own burst (%d)", name, got, burst)
		}
	}
	if got := testutil.ToFloat64(overflow); got != 0 {
		t.Fatalf("overflow counted %v admissions while the pool had room", got)
	}

	together := 0
	for i := 0; i < 50; i++ {
		if l.AllowUntrusted(fmt.Sprintf("invented-%d", i)) {
			together++
		}
	}
	if together != burst {
		t.Errorf("50 names past the pool admitted %d together; they share ONE burst (%d)", together, burst)
	}
	if got := testutil.ToFloat64(overflow); got != burst {
		t.Errorf("overflow counter = %v, want %d (one per admission the shared bucket served)", got, burst)
	}
	if c, p, o := l.BucketCounts(); c != 0 || p != 4 || !o {
		t.Errorf("BucketCounts = (%d, %d, %v), want (0, 4, true)", c, p, o)
	}
}

// Every entry point except AllowUntrusted is an authenticated admission, and never
// pools — whatever the ceiling's Source says. With the pool full and the overflow
// drained, 2000 new tenants arriving over authenticated origins each get a full burst.
func TestAuthenticatedAdmissionsNeverPool(t *testing.T) {
	const burst = 3
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(1, burst, func(tenant string) CeilingSource {
		if len(tenant)%2 == 0 {
			return CeilingUnreachable
		}
		return CeilingPending
	}))
	l.now = func() time.Time { return now }
	l.maxPooled = 2
	for i := 0; i < 10; i++ { // fill the pool and drain the overflow
		drain(func() bool { return l.AllowUntrusted(fmt.Sprintf("spray-%d", i)) })
	}
	if _, p, o := l.BucketCounts(); p != 2 || !o {
		t.Fatalf("setup: pool %d, overflow %v; want a full pool and a live overflow", p, o)
	}

	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("device-tenant-%d", i)
		when := now.Add(-time.Duration(i) * time.Millisecond)
		if got := drain(func() bool { return l.AllowAt(name, when) }); got != burst {
			t.Fatalf("authenticated tenant %s admitted %d; want its own full burst (%d)", name, got, burst)
		}
	}
	if c, _, _ := l.BucketCounts(); c != 2000 {
		t.Errorf("confirmed buckets = %d, want 2000", c)
	}
}

// A name the authority has confirmed gets a bucket of its own even from an untrusted
// origin, and even when the pool is full.
func TestResolvedUntrustedNameGetsItsOwnBucket(t *testing.T) {
	const burst = 4
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(1, burst, func(tenant string) CeilingSource {
		if tenant == "acme" {
			return CeilingResolved
		}
		return CeilingUnknownTenant
	}))
	l.now = func() time.Time { return now }
	l.maxPooled = 1
	drain(func() bool { return l.AllowUntrusted("invented-1") })

	if got := drain(func() bool { return l.AllowUntrusted("acme") }); got != burst {
		t.Errorf("resolved untrusted name admitted %d; want its own burst (%d)", got, burst)
	}
	if c, p, o := l.BucketCounts(); c != 1 || p != 1 || o {
		t.Errorf("BucketCounts = (%d, %d, %v), want (1, 1, false)", c, p, o)
	}
}

// A pooled bucket whose name is later confirmed is promoted IN PLACE: it keeps its
// level, so a larger confirmed burst adds headroom that accrues over time rather than
// tokens now.
func TestPromotionInPlaceMintsNothing(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	resolved := false
	l := NewTenantRateLimiter(func(string) TenantCeiling {
		if resolved {
			return TenantCeiling{RatePerSecond: 1, Burst: 10, Source: CeilingResolved}
		}
		return TenantCeiling{RatePerSecond: 1, Burst: 5, Source: CeilingPending}
	})
	l.now = func() time.Time { return now }

	if got := drain(func() bool { return l.AllowUntrusted("acme") }); got != 5 {
		t.Fatalf("pooled bucket admitted %d, want 5", got)
	}
	if c, p, _ := l.BucketCounts(); c != 0 || p != 1 {
		t.Fatalf("before promotion BucketCounts = (%d, %d), want (0, 1)", c, p)
	}

	resolved = true
	if l.Allow("acme") {
		t.Error("promotion to a larger burst minted a token at the same instant")
	}
	if c, p, _ := l.BucketCounts(); c != 1 || p != 0 {
		t.Errorf("after promotion BucketCounts = (%d, %d), want (1, 0)", c, p)
	}
}

// While the overflow is live, a newly confirmed untrusted name starts at the overflow's
// level, so rotating to a name the authority knows is not a way out of the shared
// allowance.
func TestAnUntrustedBucketBornDuringOverflowStartsAtItsLevel(t *testing.T) {
	const rps, burst = 10, 5
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(rps, burst, func(tenant string) CeilingSource {
		if tenant == "known" {
			return CeilingResolved
		}
		return CeilingPending
	}))
	l.now = func() time.Time { return now }
	l.maxPooled = 1
	drain(func() bool { return l.AllowUntrusted("invented-1") })
	if got := drain(func() bool { return l.AllowUntrusted("invented-2") }); got != burst {
		t.Fatalf("setup: overflow admitted %d, want %d", got, burst)
	}

	if l.AllowUntrusted("known") {
		t.Fatal("a name confirmed during overflow started full; it must start at the overflow's (empty) level")
	}
	now = now.Add(time.Second / rps)
	if got := drain(func() bool { return l.AllowUntrusted("known") }); got != 1 {
		t.Errorf("after 1/rate s the new bucket admitted %d, want exactly 1", got)
	}
}

// The seed is for untrusted creations only. An authenticated tenant born while the
// overflow is live starts full and drains an hour-old compliant backlog in full.
func TestABucketBornDuringOverflowDrainsAnHourOldBacklogInFull(t *testing.T) {
	const rps, burst = 10, 5
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(rps, burst, allPending))
	l.now = func() time.Time { return now }
	l.maxPooled = 0 // every untrusted name goes straight to the overflow
	drain(func() bool { return l.AllowUntrusted("invented") })
	if _, _, o := l.BucketCounts(); !o {
		t.Fatal("setup: the overflow must be live")
	}

	start := now.Add(-time.Hour)
	admitted := 0
	for i := 0; i < 3600*rps; i++ {
		if l.AllowAt("backlog", start.Add(time.Duration(i)*time.Second/rps)) {
			admitted++
		}
	}
	if admitted != 3600*rps {
		t.Errorf("an hour of compliant backlog admitted %d of %d", admitted, 3600*rps)
	}
}

// The overflow bucket is an ordinary bucket: every name it serves is charged on its one
// clock, so interleaved names together admit one burst plus the rate over the span.
func TestOverflowNeverRewinds(t *testing.T) {
	const rps, burst = 10, 5
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(rps, burst, allPending))
	l.now = func() time.Time { return now }
	l.maxPooled = 0

	admitted := 0
	for i := 0; i < 2000; i++ { // 2 s at 1000/s, alternating names
		now = time.Unix(1_000_000, 0).Add(time.Duration(i) * time.Millisecond)
		if l.AllowUntrusted(fmt.Sprintf("invented-%d", i%2)) {
			admitted++
		}
	}
	if bound := burst + rps*2 + 1; admitted > bound {
		t.Errorf("overflow admitted %d across two names; bound is %d", admitted, bound)
	}
}

// Evicting an idle pooled bucket returns its slot, and an idle overflow is dropped.
func TestSweepReturnsPoolSlots(t *testing.T) {
	const burst = 2
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(1, burst, allPending))
	l.now = func() time.Time { return now }
	l.maxPooled = 2
	for _, n := range []string{"a", "b", "c"} {
		drain(func() bool { return l.AllowUntrusted(n) })
	}
	if _, p, o := l.BucketCounts(); p != 2 || !o {
		t.Fatalf("setup: pool %d overflow %v, want 2 and true", p, o)
	}

	now = now.Add(l.idleTTL + l.sweepInterval + time.Second)
	for _, n := range []string{"d", "e"} {
		if got := drain(func() bool { return l.AllowUntrusted(n) }); got != burst {
			t.Errorf("%s admitted %d after the sweep; a returned slot gives it its own burst (%d)", n, got, burst)
		}
	}
	if c, p, o := l.BucketCounts(); c != 0 || p != 2 || o {
		t.Errorf("BucketCounts = (%d, %d, %v), want (0, 2, false)", c, p, o)
	}
}

func TestUnresolvedAdmissionsCountedByCause(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	counts := map[CeilingSource]int{}
	sources := map[string]CeilingSource{
		"pending": CeilingPending, "unreachable": CeilingUnreachable, "unknown": CeilingUnknownTenant,
		"resolved": CeilingResolved, "static": CeilingStatic,
	}
	l := NewTenantRateLimiter(func(tenant string) TenantCeiling {
		return TenantCeiling{RatePerSecond: 0.001, Burst: 2, Source: sources[tenant]}
	}, WithUnresolvedAdmissions(func(s CeilingSource) { counts[s]++ }))
	l.now = func() time.Time { return now }

	for tenant := range sources {
		for i := 0; i < 5; i++ { // 2 admitted, 3 denied
			l.Allow(tenant)
		}
	}
	want := map[CeilingSource]int{CeilingPending: 2, CeilingUnreachable: 2, CeilingUnknownTenant: 2}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("after Allow: counts %v, want %v (admitted only, unresolved sources only)", counts, want)
	}

	clear(counts)
	l.AllowN("fresh", 2) // an unlisted tenant reads as the zero Source: pending
	if counts[CeilingPending] != 1 || len(counts) != 1 {
		t.Errorf("AllowN of 2 must count once; got %v", counts)
	}

	clear(counts)
	l.AllowUntrusted("unreachable")
	l.AllowUntrusted("unknown")
	l.AllowUntrusted("resolved")
	if counts[CeilingUnknownTenant] != 0 || counts[CeilingUnreachable] != 0 {
		// both buckets were drained by the Allow calls above: denied, so not counted
		t.Errorf("denied untrusted admissions must not count; got %v", counts)
	}
	if !l.AllowUntrusted("fresh-untrusted") || counts[CeilingPending] != 1 {
		t.Errorf("an admitted untrusted call must count; got %v", counts)
	}

	clear(counts)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := l.Wait(ctx, "fresh-wait"); err != nil {
		t.Fatalf("Wait on a fresh bucket: %v", err)
	}
	if counts[CeilingPending] != 1 || len(counts) != 1 {
		t.Errorf("a successful Wait must count once; got %v", counts)
	}
	// The budget check compares against ctx's real deadline, so this half runs on the
	// real clock: drain a bucket, then a Wait whose token is ~1000 s away is shed.
	l.now = time.Now
	l.Allow("drained")
	l.Allow("drained")
	clear(counts)
	short, cancelShort := context.WithTimeout(context.Background(), time.Second)
	defer cancelShort()
	if err := l.Wait(short, "drained"); !errors.Is(err, ErrWaitBudget) {
		t.Fatalf("Wait past the budget = %v, want ErrWaitBudget", err)
	}
	if len(counts) != 0 {
		t.Errorf("a shed Wait must not count; got %v", counts)
	}
}

// A resolver that forgets to set Source yields CeilingPending: pooled when untrusted,
// and counted — bounded and visible, never paging.
func TestZeroValueCeilingIsPendingAndPooled(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var got []CeilingSource
	l := NewTenantRateLimiter(func(string) TenantCeiling {
		return TenantCeiling{RatePerSecond: 1, Burst: 1}
	}, WithUnresolvedAdmissions(func(s CeilingSource) { got = append(got, s) }))
	l.now = func() time.Time { return now }

	if !l.AllowUntrusted("acme") {
		t.Fatal("a fresh pooled bucket must admit")
	}
	if c, p, _ := l.BucketCounts(); c != 0 || p != 1 {
		t.Errorf("BucketCounts = (%d, %d), want (0, 1): an unsaid Source must pool", c, p)
	}
	if len(got) != 1 || got[0] != CeilingPending || got[0].String() != "pending" {
		t.Errorf("counted %v, want one pending", got)
	}
}

// BenchmarkSweep measures one full idle sweep over 10k confirmed and a full pool.
func BenchmarkSweep(b *testing.B) {
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(1, 1, allPending))
	l.now = func() time.Time { return now }
	for i := 0; i < 10000; i++ {
		l.Allow(fmt.Sprintf("confirmed-%d", i))
	}
	for i := 0; i < maxUntrustedBuckets; i++ {
		l.AllowUntrusted(fmt.Sprintf("pooled-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.mu.Lock()
		l.lastSweep = time.Time{}
		l.sweepLocked(now) // nothing is idle: a full scan that evicts nothing
		l.mu.Unlock()
	}
}
