// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// flatResolve is a fail-safe-shaped resolver: every tenant gets the same POSITIVE ceiling,
// mirroring the platform-default closure the limiter uses when no authority is configured. A
// near-zero rate keeps tokens from refilling within a sub-millisecond test, so admission is
// governed by the burst alone.
func flatResolve(burst int) func(string) (float64, int) {
	return func(string) (float64, int) { return 1e-9, burst }
}

func counter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: "x", Help: "x"})
}

func counterValue(c prometheus.Counter) float64 {
	return testutil.ToFloat64(c)
}

// STAGE 1 sheds a message flood and counts each shed once, per tenant independently.
func TestIngestLimiter_MessageStageShedsAndCounts(t *testing.T) {
	shed := counter()
	l := NewIngestLimiter(flatResolve(2), DefaultSamplesPerMessage, 256,
		IngestLimiterMetrics{MessagesShed: shed})

	assert.True(t, l.AllowMessage("acme"), "1st within burst 2")
	assert.True(t, l.AllowMessage("acme"), "2nd within burst 2")
	assert.False(t, l.AllowMessage("acme"), "3rd shed")
	assert.False(t, l.AllowMessage("acme"), "4th shed")
	assert.Equal(t, float64(2), counterValue(shed), "two messages shed")

	// A different tenant has its own bucket — one tenant's flood never starves another.
	assert.True(t, l.AllowMessage("beta"), "beta's own burst is intact")
}

// STAGE 2 charges the decoded sample COUNT, sheds the batch that overflows the sample bucket,
// and counts the shed VOLUME (n), not shed events.
func TestIngestLimiter_SampleStageChargesCountAndCounts(t *testing.T) {
	shed := counter()
	// Sample burst = satMul(burst 4, factor 25) = 100 (>= floor 50), so ~100 sample tokens.
	l := NewIngestLimiter(flatResolve(4), 25, 50, IngestLimiterMetrics{SamplesShed: shed})

	assert.True(t, l.AllowSamples("acme", 60), "60 of ~100 sample tokens")
	assert.True(t, l.AllowSamples("acme", 40), "next 40 drains the bucket")
	assert.False(t, l.AllowSamples("acme", 30), "30 more overflows — shed")
	assert.Equal(t, float64(30), counterValue(shed), "shed counts SAMPLES (30), not one event")

	// A non-positive batch is not a rate event: admitted, charged nothing.
	assert.True(t, l.AllowSamples("acme", 0))
	assert.True(t, l.AllowSamples("acme", -5))
	assert.Equal(t, float64(30), counterValue(shed), "no-op batches charged nothing")
}

// The sample burst is FLOORED at the per-Notify cap, so a single compliant batch up to the cap
// always fits — the AllowN(n>burst) forever-shed edge is closed. A batch the floored limiter
// admits is shed by an unfloored (floor 0) one built from the same tiny ceiling.
func TestIngestLimiter_SampleBurstFlooredAtCap(t *testing.T) {
	// Tiny message burst 1, factor 1 → satMul = 1. Without a floor the sample burst is 1, so a
	// 200-sample batch could never be admitted. Floored at 256 it fits.
	floored := NewIngestLimiter(flatResolve(1), 1, 256, IngestLimiterMetrics{})
	unfloored := NewIngestLimiter(flatResolve(1), 1, 0, IngestLimiterMetrics{})

	assert.True(t, floored.AllowSamples("acme", 200), "200 <= floored burst 256 admits")
	assert.False(t, unfloored.AllowSamples("acme", 200), "200 > unfloored burst 1 is shed forever")
}

// satMulInt saturates instead of overflowing to a negative burst — the guard that keeps a
// huge per-tenant override from silently black-holing a tenant's samples.
func TestSatMulInt_Saturates(t *testing.T) {
	assert.Equal(t, 100, satMulInt(4, 25))
	assert.Equal(t, math.MaxInt, satMulInt(math.MaxInt, 25), "no overflow to negative")
	assert.Equal(t, math.MaxInt, satMulInt(math.MaxInt/10, 25), "saturates below MaxInt too")
	assert.Equal(t, 0, satMulInt(0, 25))
	assert.Equal(t, 0, satMulInt(-3, 25))
	assert.Equal(t, 0, satMulInt(4, 0))

	// End to end: a max-burst override must still admit a real batch (not a negative bucket).
	l := NewIngestLimiter(flatResolve(math.MaxInt), 25, 256, IngestLimiterMetrics{})
	assert.True(t, l.AllowSamples("acme", 1000), "a saturated sample burst still admits")
}

// The derived sample ceiling is re-read from the shared resolver on every admission, so a
// per-tenant override change takes effect (within the resolver's TTL) — it is NOT snapshotted at
// construction. Hoisting the resolve out of the closure would freeze the ceiling and pass every
// other test; this one reddens on that mistake.
func TestIngestLimiter_SampleCeilingTracksResolver(t *testing.T) {
	curBurst := 1
	resolve := func(string) (float64, int) { return 1e-9, curBurst }
	l := NewIngestLimiter(resolve, 1, 1, IngestLimiterMetrics{}) // factor 1, floor 1

	// At burst 1 the sample bucket can never fit a 5-sample batch.
	assert.False(t, l.AllowSamples("acme", 5), "burst 1: a 5-sample batch is shed")

	// Raise the override. A fresh tenant's sample bucket must reflect the NEW ceiling — proof the
	// closure re-reads the resolver rather than a construction-time snapshot.
	curBurst = 100
	assert.True(t, l.AllowSamples("beta", 5), "burst 100 after override: a 5-sample batch admits")
}

// A non-positive sampleBurstFloor (a shared-mechanism footgun the LwM2M path avoids) must not
// yield a zero-burst sample bucket, which would admit NOTHING. The constructor floors it at 1.
func TestIngestLimiter_ZeroFloorStillAdmitsOne(t *testing.T) {
	// resolve burst 0 (governance never returns this, but an adopter's flat closure might) with a
	// non-positive floor: satMul(0,25)=0, floor 0 → the guard raises the sample burst to 1.
	l := NewIngestLimiter(func(string) (float64, int) { return 1e-9, 0 }, 25, 0, IngestLimiterMetrics{})
	assert.True(t, l.AllowSamples("acme", 1), "a zero-derived sample burst must still admit a single sample")
}

// A nil-metrics limiter is fully usable (tests / inert deployments) — no panic on shed.
func TestIngestLimiter_NilMetricsSafe(t *testing.T) {
	l := NewIngestLimiter(flatResolve(1), DefaultSamplesPerMessage, 256, IngestLimiterMetrics{})
	assert.True(t, l.AllowMessage("acme"))
	assert.False(t, l.AllowMessage("acme"), "shed with nil MessagesShed does not panic")
	assert.False(t, l.AllowSamples("acme", 1_000_000), "shed with nil SamplesShed does not panic")
}
