// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIngress stands in for the device-plane HTTP ingress. EmitMeasurements
// needs nothing but the endpoint and the HTTP client — the credential travels
// in the payload, not in a session header — so a Runtime for these tests can be
// built without authenticating anything.
func fakeIngress(t *testing.T, deviceCount int, handler http.HandlerFunc) *Runtime {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	devices := make([]DeviceInstance, deviceCount)
	for i := range devices {
		devices[i] = DeviceInstance{Token: renderPattern("dev-{n:05d}", i+1), CredentialId: "cred"}
	}
	return &Runtime{
		Endpoints:  Endpoints{Ingress: srv.URL},
		InstanceId: "dc",
		Tenant:     "acme",
		HTTPClient: srv.Client(),
		Devices:    devices,
	}
}

func accepted(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) }

func constantMetrics(int, DeviceInstance) map[string]float64 {
	return map[string]float64{"speed_kph": 1}
}

// Every device must be emitted for, exactly once per tick.
//
// The achieved rate is derived from this count, so a worker pool that dropped
// or double-served an index would corrupt every number a measurement publishes
// while looking perfectly healthy.
func TestEmitAllCoversEveryDeviceExactlyOnce(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	rt := fakeIngress(t, 50, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Device string `json:"device"`
		}
		decodeJSON(t, r, &body)
		mu.Lock()
		seen[body.Device]++
		mu.Unlock()
		accepted(w, r)
	})

	if err := EmitAll(context.Background(), rt, 8, constantMetrics); err != nil {
		t.Fatalf("EmitAll: %v", err)
	}

	if len(seen) != 50 {
		t.Fatalf("%d distinct devices emitted, want 50", len(seen))
	}
	for token, n := range seen {
		if n != 1 {
			t.Errorf("device %s emitted %d times, want 1", token, n)
		}
	}
	if got := rt.Stats.Emitted.Load(); got != 50 {
		t.Errorf("Stats.Emitted = %d, want 50", got)
	}
	if got := rt.Stats.Failed.Load(); got != 0 {
		t.Errorf("Stats.Failed = %d, want 0", got)
	}
}

// Emits must genuinely overlap.
//
// This is the claim the whole load generator rests on: a serial emit caps the
// achieved rate at 1/latency no matter what interval was configured, so the sim
// would report a target it never reached. The barrier makes the assertion
// deterministic rather than timing-based — with real concurrency every handler
// is in flight at once and the barrier closes; serially the first handler waits
// out its timeout and the observed peak stays at 1.
func TestEmitAllActuallyEmitsConcurrently(t *testing.T) {
	const workers = 4

	var inFlight, peak atomic.Int64
	barrier := make(chan struct{})
	var once sync.Once

	rt := fakeIngress(t, workers, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		if n == workers {
			once.Do(func() { close(barrier) })
		}
		select {
		case <-barrier:
		case <-time.After(2 * time.Second):
		}
		inFlight.Add(-1)
		accepted(w, r)
	})

	if err := EmitAll(context.Background(), rt, workers, constantMetrics); err != nil {
		t.Fatalf("EmitAll: %v", err)
	}
	if got := peak.Load(); got != workers {
		t.Fatalf("peak concurrent emits = %d, want %d: emits are serialized, so the "+
			"achieved rate is bounded by emit latency rather than by the configured "+
			"interval — every rate this sim reports would be a target it never hit", got, workers)
	}
}

// Concurrency must be BOUNDED, not merely present.
func TestEmitAllRespectsItsWorkerBound(t *testing.T) {
	var inFlight, peak atomic.Int64
	rt := fakeIngress(t, 40, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		accepted(w, r)
	})

	if err := EmitAll(context.Background(), rt, 3, constantMetrics); err != nil {
		t.Fatalf("EmitAll: %v", err)
	}
	if got := peak.Load(); got > 3 {
		t.Errorf("peak concurrent emits = %d, above the bound of 3", got)
	}
}

// One failing device must not cancel the rest of the tick.
//
// Halting a tick on the first error under-applies load exactly when the
// platform is most stressed, biasing a footprint measurement toward looking
// cheaper than it is. Every device is attempted; the failures are counted.
func TestEmitAllKeepsGoingPastAFailure(t *testing.T) {
	var n atomic.Int64
	rt := fakeIngress(t, 20, func(w http.ResponseWriter, r *http.Request) {
		// Fail every third request.
		if n.Add(1)%3 == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		accepted(w, r)
	})

	err := EmitAll(context.Background(), rt, 4, constantMetrics)
	if err == nil {
		t.Fatal("a tick with failed emits reported success")
	}

	emitted, failed := rt.Stats.Emitted.Load(), rt.Stats.Failed.Load()
	if emitted+failed != 20 {
		// Fires in both directions, so the message names both: under 20 means
		// devices went unattempted (less load applied than reported); over 20
		// means an emit was counted twice (more load reported than applied).
		t.Errorf("emitted(%d) + failed(%d) = %d, want 20: the accounting does not "+
			"match the device set — under-counting means devices were never "+
			"attempted, over-counting means an emit was tallied twice",
			emitted, failed, emitted+failed)
	}
	if failed == 0 {
		t.Error("no failures were counted despite the ingress rejecting requests")
	}
}

// Per-device metric values must reach the device they were computed for.
//
// buildingpulse gives each device a distinct phase offset by index; a worker
// pool that paired index i's metrics with device j would emit a plausible-
// looking stream that no longer matches the topology it claims to describe.
func TestEmitAllPairsEachDeviceWithItsOwnMetrics(t *testing.T) {
	var mu sync.Mutex
	got := map[string]float64{}
	rt := fakeIngress(t, 30, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Device  string `json:"device"`
			Payload struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		decodeJSON(t, r, &body)
		mu.Lock()
		got[body.Device] = parseFloat(t, body.Payload.Entries[0].Measurements["idx"])
		mu.Unlock()
		accepted(w, r)
	})

	// Each device's value is its own index, so any mispairing is visible.
	err := EmitAll(context.Background(), rt, 8, func(i int, _ DeviceInstance) map[string]float64 {
		return map[string]float64{"idx": float64(i)}
	})
	if err != nil {
		t.Fatalf("EmitAll: %v", err)
	}

	for i, d := range rt.Devices {
		if got[d.Token] != float64(i) {
			t.Errorf("device %s (index %d) received metrics for index %v",
				d.Token, i, got[d.Token])
		}
	}
}

func TestEmitAllWithNoDevicesIsANoOp(t *testing.T) {
	rt := fakeIngress(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an empty device set still POSTed to the ingress")
	})
	if err := EmitAll(context.Background(), rt, 4, constantMetrics); err != nil {
		t.Fatalf("EmitAll on an empty device set: %v", err)
	}
}

// The achieved rate must be derived from what was emitted, not from the config.
func TestSnapshotReportsTheAchievedRate(t *testing.T) {
	var stats Stats
	start := time.Now()
	stats.Reset(start)
	stats.Emitted.Store(300)
	stats.Failed.Store(12)

	snap := stats.Snapshot(start.Add(10 * time.Second))
	if snap.Rate != 30 {
		t.Errorf("achieved rate = %v, want 30 (300 emits / 10s)", snap.Rate)
	}
	// Failures must NOT count toward the achieved rate: load the platform
	// rejected is load it never had to carry, and folding it in would inflate
	// exactly the number a measurement is built on.
	if snap.Emitted != 300 || snap.Failed != 12 {
		t.Errorf("snapshot = %+v, want 300 emitted / 12 failed", snap)
	}
}

// Reset must zero the counters, so a rate describes the current run.
func TestResetStartsANewRun(t *testing.T) {
	var stats Stats
	stats.Reset(time.Now())
	stats.Emitted.Store(999)
	stats.Overruns.Store(5)

	later := time.Now().Add(time.Minute)
	stats.Reset(later)
	if snap := stats.Snapshot(later.Add(time.Second)); snap.Emitted != 0 || snap.Overruns != 0 {
		t.Errorf("counters survived a reset: %+v", snap)
	}
}

func decodeJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("decode request body: %v", err)
	}
}

func parseFloat(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Errorf("parse %q: %v", s, err)
	}
	return f
}
