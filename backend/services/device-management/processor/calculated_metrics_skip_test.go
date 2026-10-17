// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// Two workers (two observers) skipping the same metric report it once between them, and
// every skip is counted, however many were suppressed.
func TestSkipSuppressionIsSharedAcrossObserversAndCountersStayExact(t *testing.T) {
	metrics := testCalcMetrics()
	ctx := core.WithTenant(context.Background(), "acme")
	const workers, each = 8, 500
	var claimed atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		obs := &calcObserver{metrics: metrics, ctx: ctx, version: "meter@1"}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				obs.Skipped("power_kw", calcfield.SkipMissingInput, nil)
				if obs.claim("power_kw", "missing_input") {
					claimed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, claimed.Load(), "Skipped already took the one claim")
	assert.Equal(t, float64(workers*each), testutil.ToFloat64(metrics.skipped[calcfield.SkipMissingInput]))
	assert.Equal(t, 1, metrics.warned.len(), "one outcome, one entry")
}

// An observer that outgrows its cache still never reports twice what the shared set has.
func TestSkipCacheIsBoundedAndResetNeverReReports(t *testing.T) {
	metrics := testCalcMetrics()
	obs := &calcObserver{metrics: metrics, ctx: core.WithTenant(context.Background(), "acme"), version: "v"}
	assert.True(t, obs.claim("first", "range"))
	obs.seen = nil // a reset, as the bound does
	assert.False(t, obs.claim("first", "range"), "the shared set still remembers")
	for i := 0; i < calcWarnCapacity+10; i++ {
		obs.claim(string(rune('a'+i%26))+string(rune(i)), "range")
		assert.LessOrEqual(t, len(obs.seen), calcWarnCapacity)
	}
}

// legacyClaim is the claim as it was before the per-observer cache: build the key string and
// take the shared mutex every time.
func legacyClaim(o *calcObserver, key, outcome string) bool {
	tenant, _ := core.TenantFromContext(o.ctx)
	return o.metrics.warned.putIfAbsent(tenant+"|"+o.version+"|"+key+"|"+outcome, struct{}{})
}

// BenchmarkSkippedRepeat is a pool of workers each skipping a formula on every reading, the
// shape of a fleet whose has()-guarded input is usually absent. "shared" is the old path,
// "cached" the new one; run interleaved with -count.
func BenchmarkSkippedRepeat(b *testing.B) {
	for _, mode := range []string{"shared", "cached"} {
		b.Run(mode, func(b *testing.B) {
			metrics := testCalcMetrics()
			ctx := core.WithTenant(context.Background(), "acme")
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				obs := &calcObserver{metrics: metrics, ctx: ctx, version: "meter-profile@17"}
				for pb.Next() {
					metrics.skipped[calcfield.SkipMissingInput].Inc()
					if mode == "shared" {
						legacyClaim(obs, "power_kw", "missing_input")
					} else {
						obs.claim("power_kw", "missing_input")
					}
				}
			})
		})
	}
}
