// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventlimit"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// flatResolve is a fail-safe-shaped resolver: every tenant gets the same POSITIVE ceiling,
// mirroring the platform-default closure the limiter uses when no authority is configured. A
// near-zero rate keeps tokens from refilling within a sub-millisecond test, so admission is
// governed by the burst alone.
func flatResolve(burst int) core.TenantCeilingResolver {
	return core.StaticCeiling(1e-9, burst)
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
	l := NewIngestLimiter(flatResolve(2), IngestLimiterMetrics{MessagesShed: shed}, nil)

	assert.True(t, l.AllowMessage("acme"), "1st within burst 2")
	assert.True(t, l.AllowMessage("acme"), "2nd within burst 2")
	assert.False(t, l.AllowMessage("acme"), "3rd shed")
	assert.False(t, l.AllowMessage("acme"), "4th shed")
	assert.Equal(t, float64(2), counterValue(shed), "two messages shed")

	// A different tenant has its own bucket — one tenant's flood never starves another.
	assert.True(t, l.AllowMessage("beta"), "beta's own burst is intact")
}

// STAGE 2 charges the decoded sample COUNT against the tenant's ingest ceiling itself (a
// burst of 300 is 300 readings, not 25 times that), sheds the batch that overflows, and
// counts the shed VOLUME (n), not shed events.
func TestIngestLimiter_SampleStageChargesCountAndCounts(t *testing.T) {
	shed := counter()
	l := NewIngestLimiter(flatResolve(300), IngestLimiterMetrics{SamplesShed: shed}, nil)

	assert.True(t, l.AllowSamples("acme", 160), "160 of 300 reading tokens")
	assert.True(t, l.AllowSamples("acme", 140), "next 140 drains the bucket")
	assert.False(t, l.AllowSamples("acme", 30), "30 more overflows — shed")
	assert.Equal(t, float64(30), counterValue(shed), "shed counts SAMPLES (30), not one event")

	// A non-positive batch is not a rate event: admitted, charged nothing.
	assert.True(t, l.AllowSamples("acme", 0))
	assert.True(t, l.AllowSamples("acme", -5))
	assert.Equal(t, float64(30), counterValue(shed), "no-op batches charged nothing")
}

// The sample stage's RATE is the ingest rate, not a multiple of it. At 10 readings/s, 200 ms
// after the bucket is drained it holds about 2 tokens, so a 50-reading charge is refused; a
// limiter that scaled the rate 25 times would hold about 50 and admit it.
func TestIngestLimiter_SampleRateIsTheIngestRate(t *testing.T) {
	l := NewIngestLimiter(core.StaticCeiling(10, 20), IngestLimiterMetrics{}, nil)
	assert.Equal(t, 256, l.AdmitSamples("acme", 256), "the floored burst admits one full event")
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, l.AdmitSamples("acme", 50), "200 ms at 10 readings/s refills about 2, not 50")
}

// The sample burst is FLOORED at the per-event limit, so a single charge up to the limit
// always fits — the AllowN(n>burst) forever-shed edge is closed — and a charge above it is
// still refused.
func TestIngestLimiter_SampleBurstFlooredAtCap(t *testing.T) {
	floored := NewIngestLimiter(flatResolve(1), IngestLimiterMetrics{}, nil)
	assert.True(t, floored.AllowSamples("acme", eventlimit.MaxReadingsPerEvent), "one full event fits a burst-1 tier")
	assert.False(t, NewIngestLimiter(flatResolve(1), IngestLimiterMetrics{}, nil).
		AllowSamples("acme", eventlimit.MaxReadingsPerEvent+1), "the floor is one event, not more")
}

// A max-burst override still admits a real batch: the ceiling passes through unscaled, so
// there is no multiplication to overflow into a negative bucket.
func TestIngestLimiter_MaxBurstOverrideAdmits(t *testing.T) {
	l := NewIngestLimiter(flatResolve(math.MaxInt), IngestLimiterMetrics{}, nil)
	assert.True(t, l.AllowSamples("acme", 1000), "a max-int burst admits")
}

// The derived sample ceiling is re-read from the shared resolver on every admission, so a
// per-tenant override change takes effect (within the resolver's TTL) — it is NOT snapshotted at
// construction. Hoisting the resolve out of the closure would freeze the ceiling and pass every
// other test; this one reddens on that mistake.
func TestIngestLimiter_SampleCeilingTracksResolver(t *testing.T) {
	curBurst := 1
	resolve := func(string) core.TenantCeiling { return core.TenantCeiling{RatePerSecond: 1e-9, Burst: curBurst} }
	l := NewIngestLimiter(resolve, IngestLimiterMetrics{}, nil)

	// At burst 1 (floored to one event) the sample bucket can never fit a 300-sample batch.
	assert.False(t, l.AllowSamples("acme", 300), "burst 1: a 300-sample batch is shed")

	// Raise the override. A fresh tenant's sample bucket must reflect the NEW ceiling — proof the
	// closure re-reads the resolver rather than a construction-time snapshot.
	curBurst = 1000
	assert.True(t, l.AllowSamples("beta", 300), "burst 1000 after override: a 300-sample batch admits")
}

// A zero burst from an adopter's flat closure (governance never returns one) must not yield a
// zero-burst sample bucket, which would admit NOTHING: a positive rate floors it to one event.
func TestIngestLimiter_ZeroBurstStillAdmits(t *testing.T) {
	l := NewIngestLimiter(core.StaticCeiling(1e-9, 0), IngestLimiterMetrics{}, nil)
	assert.True(t, l.AllowSamples("acme", 1), "a zero-burst ceiling must still admit a single sample")
}

// A nil-metrics limiter is fully usable (tests / inert deployments) — no panic on shed.
func TestIngestLimiter_NilMetricsSafe(t *testing.T) {
	l := NewIngestLimiter(flatResolve(1), IngestLimiterMetrics{}, nil)
	assert.True(t, l.AllowMessage("acme"))
	assert.False(t, l.AllowMessage("acme"), "shed with nil MessagesShed does not panic")
	assert.False(t, l.AllowSamples("acme", 1_000_000), "shed with nil SamplesShed does not panic")
}

// 🔴 A MESSAGE LARGER THAN THE SAMPLE BURST IS CHARGED EVENT BY EVENT. At the smallest ceiling
// (burst 1, so the sample burst is the 256 floor) one charge of 600 could never fit and would
// be refused for ever; charged per event, the first 256 are admitted and the other 344 are
// counted as shed. The admitted count is a whole number of events, so the prefix the caller
// ingests splits into exactly the events that were charged.
func TestIngestLimiter_AdmitSamplesChargesPerEvent(t *testing.T) {
	shed := counter()
	l := NewIngestLimiter(flatResolve(1), IngestLimiterMetrics{SamplesShed: shed}, nil)
	assert.Equal(t, 256, l.AdmitSamples("acme", 600), "the budget admits one event's worth")
	assert.Equal(t, float64(344), counterValue(shed), "every sample not admitted is counted, once")
	assert.Equal(t, 0, l.AdmitSamples("acme", 10), "the bucket is now empty")
	assert.Equal(t, float64(354), counterValue(shed))

	roomy := NewIngestLimiter(flatResolve(1000), IngestLimiterMetrics{}, nil)
	assert.Equal(t, 600, roomy.AdmitSamples("acme", 600), "a budget with room admits the whole message")
	assert.Equal(t, 0, roomy.AdmitSamples("acme", 0), "nothing to charge admits nothing")
}
