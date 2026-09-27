// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// BenchmarkProjectionWriters measures what projection.writers and projection.maxBatch buy on
// a REAL server: events merged per second with 1, 5 and 10 writers, one event per
// transaction (maxBatch 1) or batched (maxBatch 32), over three fleets:
//
//   - spread: every event its own device, so no two merges wait on one row lock;
//   - hot: 8 devices, so they do;
//   - loadtest: 200 devices sending in turn, the shape dc-loadtest drives (rate/4 devices,
//     each every 250 ms, at 800 events a second), where concurrent batches share devices and
//     a batch that meets a row another writer holds waits for that writer's commit.
//
// Run it as fence_cost_bench_integration_test.go describes, with -bench ProjectionWriters.
// The numbers that matter are with a synchronous standby, where every commit waits for it.
package processor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type countingAck struct{ n *atomic.Int64 }

func (c countingAck) Ack() error {
	c.n.Add(1)
	return nil
}

func BenchmarkProjectionWriters(b *testing.B) {
	mgr, _ := newFenceBenchManager(b, "dswritersbench", true)
	base := time.Now().UTC().Truncate(time.Hour)
	var invocation int
	for _, fleet := range []struct {
		name    string
		devices int
	}{{"spread", 0}, {"hot", 8}, {"loadtest", 200}} {
		for _, maxBatch := range []int{1, 32} {
			for _, writers := range []int{1, 5, 10} {
				b.Run(fmt.Sprintf("fleet=%s/maxBatch=%d/writers=%d", fleet.name, maxBatch, writers), func(b *testing.B) {
					invocation++
					ms := &core.Microservice{InstanceId: "bench", FunctionalArea: "device-state"}
					ms.UseMetricsRegistry(prometheus.NewRegistry())
					metrics := NewStateMetrics(ms)
					sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), newBenchProcessor(mgr).Api,
						metrics, ms.NewPeriodicTaskMetrics("writers_bench"),
						WithProjection(config.ProjectionConfiguration{Writers: writers, MaxBatch: maxBatch}))
					if err := sp.Initialize(context.Background()); err != nil {
						b.Fatalf("initialize: %v", err)
					}
					var acked atomic.Int64
					device := func(i int) string {
						if fleet.devices == 0 {
							return fmt.Sprintf("wb-%d-%d", invocation, i)
						}
						return fmt.Sprintf("wb-%d-fleet-%d", invocation, i%fleet.devices)
					}
					// Warm-up outside the timer, one event at a time. It creates every fixed
					// fleet's rows first: on the per-message path two FIRST events for one
					// device race on its unique index and one is left for redelivery, which
					// this benchmark would wait on forever.
					warmups := []string{fmt.Sprintf("wb-%d-warm", invocation)}
					for i := 0; i < fleet.devices; i++ {
						warmups = append(warmups, device(i))
					}
					for i, dev := range warmups {
						sp.handOff(context.Background(), benchMeasurement(b, "acme", dev, base, countingAck{&acked}))
						for acked.Load() < int64(i+1) {
							time.Sleep(time.Millisecond)
						}
					}
					acked.Store(0)
					b.ResetTimer()
					start := time.Now()
					for i := 0; i < b.N; i++ {
						at := base.Add(time.Duration(invocation)*time.Minute + time.Duration(i)*time.Millisecond)
						sp.handOff(context.Background(), benchMeasurement(b, "acme", device(i), at, countingAck{&acked}))
					}
					deadline := time.Now().Add(5 * time.Minute)
					for acked.Load() < int64(b.N) {
						if time.Now().After(deadline) {
							b.Fatalf("%d of %d events merged after 5 minutes", acked.Load(), b.N)
						}
						time.Sleep(time.Millisecond)
					}
					elapsed := time.Since(start)
					b.StopTimer()
					if err := sp.ExecuteStop(context.Background()); err != nil {
						b.Fatalf("stop: %v", err)
					}
					b.ReportMetric(float64(b.N)/elapsed.Seconds(), "events/s")
					// Batch transactions that did not commit (a lock wait that ended in a
					// deadlock, a first sight lost to another writer), per 1000 events.
					b.ReportMetric(1000*testutil.ToFloat64(metrics.fallbacks)/float64(b.N), "fallbacks/1000ev")
				})
			}
		}
	}
}
