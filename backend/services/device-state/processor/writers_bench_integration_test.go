// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// BenchmarkProjectionWriters measures what projection.writers buys on a REAL server: events
// merged per second with 1, 5 and 10 writers, over a spread fleet (every event its own
// device, so no two merges wait on one row lock) and a hot one (8 devices, so they do).
//
// Run it as fence_cost_bench_integration_test.go describes, with -bench ProjectionWriters.
package processor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
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
	}{{"spread", 0}, {"hot", 8}} {
		for _, writers := range []int{1, 5, 10} {
			b.Run(fmt.Sprintf("fleet=%s/writers=%d", fleet.name, writers), func(b *testing.B) {
				invocation++
				ms := &core.Microservice{InstanceId: "bench", FunctionalArea: "device-state"}
				ms.UseMetricsRegistry(prometheus.NewRegistry())
				sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), newBenchProcessor(mgr).Api,
					NewStateMetrics(ms), ms.NewPeriodicTaskMetrics("writers_bench"), WithWriters(writers))
				if err := sp.Initialize(context.Background()); err != nil {
					b.Fatalf("initialize: %v", err)
				}
				var acked atomic.Int64
				device := func(i int) string {
					if fleet.devices == 0 {
						return fmt.Sprintf("wb-%d-%d", invocation, i)
					}
					return fmt.Sprintf("wb-%d-hot-%d", invocation, i%fleet.devices)
				}
				// Warm-up outside the timer, one event at a time. It creates every hot device's
				// row first: two FIRST events for one device race on its unique index and one
				// is left for redelivery, which this benchmark would wait on forever.
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
			})
		}
	}
}
