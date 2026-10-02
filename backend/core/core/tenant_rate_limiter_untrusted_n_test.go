// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// AllowUntrustedN charges the WHOLE batch from the one bucket AllowUntrusted would use: a
// 256-unit charge empties a 256 burst, so the next single unit is refused at the same
// instant, and only one pooled bucket exists for the name.
func TestAllowUntrustedNChargesTheWholeBatch(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewTenantRateLimiter(sourced(1, 256, allPending))
	l.now = func() time.Time { return now }

	if !l.AllowUntrustedN("acme", 256) {
		t.Fatal("a 256 charge against an idle 256 burst was refused")
	}
	if l.AllowUntrustedN("acme", 1) {
		t.Fatal("a 256 charge must spend 256 tokens; one more unit at the same instant was admitted")
	}
	if l.AllowUntrusted("acme") {
		t.Fatal("AllowUntrusted must draw from the same bucket AllowUntrustedN charged")
	}
	if c, p, _ := l.BucketCounts(); c != 0 || p != 1 {
		t.Errorf("BucketCounts = (%d confirmed, %d pooled), want (0, 1)", c, p)
	}
	// Above the burst is always refused (a token bucket never admits more than its burst).
	other := NewTenantRateLimiter(sourced(1, 256, allPending))
	other.now = func() time.Time { return now }
	if other.AllowUntrustedN("acme", 257) {
		t.Error("a charge above the burst was admitted")
	}
}

// A non-positive charge admits without creating a bucket, so an empty batch cannot spend a
// slot of the bounded pool.
func TestAllowUntrustedNOfZeroCreatesNoBucket(t *testing.T) {
	l := NewTenantRateLimiter(sourced(1, 5, allPending))
	for _, n := range []int{0, -1} {
		if !l.AllowUntrustedN("acme", n) {
			t.Fatalf("AllowUntrustedN(%d) refused; a non-positive charge admits", n)
		}
	}
	if c, p, o := l.BucketCounts(); c != 0 || p != 0 || o {
		t.Errorf("BucketCounts = (%d, %d, %v), want (0, 0, false)", c, p, o)
	}
}

// The overflow counter counts admitted CALLS: one 3-unit admission served by the shared
// overflow bucket is one, not three.
func TestAllowUntrustedNCountsOverflowCallsNotTokens(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	overflow := prometheus.NewCounter(prometheus.CounterOpts{Name: "overflow_n_probe_total"})
	l := NewTenantRateLimiter(sourced(1, 10, allPending), WithOverflowAdmissions(overflow))
	l.now = func() time.Time { return now }
	l.maxPooled = 0

	if !l.AllowUntrustedN("invented", 3) {
		t.Fatal("the overflow bucket refused a charge within its burst")
	}
	if got := testutil.ToFloat64(overflow); got != 1 {
		t.Errorf("overflow counter = %v, want 1 (one admitted call)", got)
	}
}
