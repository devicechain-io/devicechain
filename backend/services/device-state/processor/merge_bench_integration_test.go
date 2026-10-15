// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// BenchmarkMergeProjectionBatch times ONE call of model.MergeProjectionBatch against a REAL
// PostgreSQL, in transactions per second terms: the merge itself, with no NATS, no decode and
// no worker pool in front of it, so a change in what the merge sends to the database shows up
// undiluted.
//
// Arms are fleet x batch size:
//
//   - spread/first: every event is a device never seen, the shape of onboarding a fleet;
//   - spread/known: every event is a device that already has a row, none repeating within a
//     batch, the steady state of a large fleet;
//   - loadtest/known: 200 devices sending in turn, so concurrent batches share devices.
//
// A batch is one tenant's measurement events, three readings each, one event per device.
//
// Reported beside ns/op (which is per BATCH): ns/event; stmts/batch, the statements gorm sent
// (the counter cannot see BEGIN and COMMIT, which are two more round trips each batch); and
// rtt-ns, the median `SELECT 1` on the pool, because a result measured over loopback says little
// about a network where each statement costs a round trip. Put latency in front of the server
// to measure that, e.g. `tc qdisc add dev eth0 root netem delay 250us` inside a container run
// with --cap-add NET_ADMIN.
//
// Run it as fence_cost_bench_integration_test.go describes, with -bench MergeProjectionBatch.
package processor

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

func BenchmarkMergeProjectionBatch(b *testing.B) {
	mgr, _ := newFenceBenchManager(b, "dsmergebench", true)
	rtt := medianRTT(b, mgr)
	counter := rdbtest.NewStatementCounter("")
	counter.Arm(false)
	api := model.NewApi(&rdb.RdbManager{Database: mgr.Database.Session(&gorm.Session{Logger: counter})})
	base := time.Now().UTC().Truncate(time.Hour)

	var invocation int
	readings := func(at time.Time) []model.LatestMeasurementInput {
		out := make([]model.LatestMeasurementInput, 0, 3)
		for i, name := range []string{"temperature", "humidity", "pressure"} {
			out = append(out, model.LatestMeasurementInput{Name: name, OccurredTime: at,
				Value: sql.NullFloat64{Float64: 20 + float64(i), Valid: true}})
		}
		return out
	}
	for _, fleet := range []struct {
		name    string
		devices int // 0: a device never seen before, every time
		known   bool
	}{{"spread/first", 0, false}, {"spread/known", 4096, true}, {"loadtest/known", 200, true}} {
		for _, size := range []int{1, 16, 32} {
			b.Run(fmt.Sprintf("fleet=%s/batch=%d", fleet.name, size), func(b *testing.B) {
				invocation++
				run := invocation
				device := func(n int) string {
					if fleet.devices == 0 {
						return fmt.Sprintf("mb-%d-%d", run, n)
					}
					return fmt.Sprintf("mb-%d-%04d", run, n%fleet.devices)
				}
				batchAt := func(i int, at time.Time) []model.ProjectionUpdate {
					updates := make([]model.ProjectionUpdate, size)
					for k := range updates {
						updates[k] = model.ProjectionUpdate{Tenant: "acme", DeviceToken: device(i*size + k),
							OccurredAt: at, Measurements: readings(at)}
					}
					return updates
				}
				if fleet.known {
					// Create every row outside the timer, a batch of at most 32 at a time.
					for first := 0; first < fleet.devices; first += 32 {
						var updates []model.ProjectionUpdate
						for n := first; n < first+32 && n < fleet.devices; n++ {
							updates = append(updates, model.ProjectionUpdate{Tenant: "acme", DeviceToken: device(n),
								OccurredAt: base, Measurements: readings(base)})
						}
						if err := api.MergeProjectionBatch(context.Background(), updates); err != nil {
							b.Fatalf("create the fleet: %v", err)
						}
					}
				}
				counter.Reset()
				counter.Arm(true)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					at := base.Add(time.Duration(run)*time.Hour + time.Duration(i+1)*time.Millisecond)
					if err := api.MergeProjectionBatch(context.Background(), batchAt(i, at)); err != nil {
						b.Fatalf("batch %d: %v", i, err)
					}
				}
				b.StopTimer()
				counter.Arm(false)
				all, _ := counter.Counts()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/event")
				b.ReportMetric(float64(all)/float64(b.N), "stmts/batch")
				b.ReportMetric(rtt, "rtt-ns")
			})
		}
	}
}
