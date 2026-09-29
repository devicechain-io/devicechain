// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// cacheMetricsFor builds the stream metrics a NatsManager builds, into a registry of its own.
func cacheMetricsFor(t *testing.T) (*streamMetrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(reg)
	return newStreamMetrics(ms), reg
}

// The in-process tier's series exist from the moment the cache is built, at 0, and then
// count hits, misses, evictions by reason and the size held, by value.
func TestLocalLookupMetrics(t *testing.T) {
	m, reg := cacheMetricsFor(t)
	store := newCountingStore()
	c := newCache("device-by-token", store, m, 0)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clock.now

	lookups := func(result string) float64 {
		return testutil.ToFloat64(m.cacheLocalLookups.WithLabelValues("device-by-token", result))
	}
	evictions := func(reason string) float64 {
		return testutil.ToFloat64(m.cacheLocalEvictions.WithLabelValues("device-by-token", reason))
	}

	// Created at 0 when the cache was built: 2 lookup results, 3 eviction reasons, 2 gauges.
	for _, name := range []string{"kv_cache_local_lookups_total", "kv_cache_local_evictions_total",
		"kv_cache_local_entries", "kv_cache_local_bytes"} {
		full := "devicechain_devicemanagement_" + name
		n, err := testutil.GatherAndCount(reg, full)
		if err != nil {
			t.Fatalf("gather %s: %v", full, err)
		}
		want := map[string]int{"kv_cache_local_lookups_total": 2, "kv_cache_local_evictions_total": 3,
			"kv_cache_local_entries": 1, "kv_cache_local_bytes": 1}[name]
		if n != want {
			t.Errorf("%s has %d series at construction, want %d", full, n, want)
		}
	}

	ctx := context.Background()
	mustSet(t, c, "acme|a", "va") // held
	mustGet(t, c, "acme|a")       // hit
	mustGet(t, c, "acme|a")       // hit
	mustGet(t, c, "acme|absent")  // miss (and the bucket's miss is not kept)
	if err := c.Delete(ctx, "acme|a"); err != nil {
		t.Fatal(err)
	} // deleted
	mustSet(t, c, "acme|b", "vb")
	clock.advance(DefaultLocalCacheTTL)
	mustGet(t, c, "acme|b") // expired: a miss, then refilled from the bucket

	if got := lookups("hit"); got != 2 {
		t.Errorf("hits = %v, want 2", got)
	}
	if got := lookups("miss"); got != 2 {
		t.Errorf("misses = %v, want 2", got)
	}
	if got := evictions("deleted"); got != 1 {
		t.Errorf("deleted evictions = %v, want 1", got)
	}
	if got := evictions("expired"); got != 1 {
		t.Errorf("expired evictions = %v, want 1", got)
	}
	if got := evictions("capacity"); got != 0 {
		t.Errorf("capacity evictions = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.cacheLocalEntries.WithLabelValues("device-by-token")); got != 1 {
		t.Errorf("entries = %v, want 1 (acme|b, refilled)", got)
	}
	wantBytes := float64(len("acme|b") + len(`"vb"`) + localEntryOverhead)
	if got := testutil.ToFloat64(m.cacheLocalBytes.WithLabelValues("device-by-token")); got != wantBytes {
		t.Errorf("bytes = %v, want %v", got, wantBytes)
	}

	c.local.maxEntries = 1
	mustSet(t, c, "acme|c", "vc")
	if got := evictions("capacity"); got != 1 {
		t.Errorf("capacity evictions after a Set past the cap = %v, want 1", got)
	}
}

// A cache with the in-process tier off exports none of its series: an absent series means
// the tier is off. Its bucket series are still there.
func TestACacheWithoutTheLocalTierExportsNoLocalSeries(t *testing.T) {
	m, reg := cacheMetricsFor(t)
	c := newCache("memberships-by-entity", newCountingStore(), m, 0, WithoutLocalCache())
	mustSet(t, c, "acme|a", "va")
	mustGet(t, c, "acme|a")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sawBucket bool
	for _, f := range families {
		if strings.Contains(f.GetName(), "kv_cache_local_") && len(f.GetMetric()) > 0 {
			t.Errorf("%s has %d series for a cache with the in-process tier off", f.GetName(), len(f.GetMetric()))
		}
		if f.GetName() == "devicechain_devicemanagement_kv_cache_request_duration_seconds" {
			sawBucket = true
		}
	}
	if !sawBucket {
		t.Error("the bucket's own latency series is missing; the check above is reading an empty registry")
	}
}
