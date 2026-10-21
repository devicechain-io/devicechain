// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// A contention measurement for the projection's batch merge against a REAL PostgreSQL: how does
// MergeProjectionBatch behave when one device takes a large share of the events, and what would
// routing every device to one writer (keyed routing) change? It exists to decide a build/drop
// question with numbers (issue #1399, experiment 1); it measures, it asserts nothing about speed.
//
// It is a benchmark so that hack/integration-tests.sh lists it as a measurement rig rather than
// running it (that script fails any tagged Test that skips), and it also needs DC_CONTENTION=1, so
// a stray -bench . does not start a multi-minute run. One measurement per invocation, so -benchtime=1x:
//
//	DC_CONTENTION=1 DC_IT_PGPORT=$PORT go test -tags integration -run '^$' \
//	  -bench BenchmarkProjectionContention -benchtime=1x -count=1 -v -p 1 ./model/
//
// Knobs (defaults in brackets): DC_CONTENTION_EVENTS [30000], _WRITERS [10], _BATCH [32],
// _FLEET [10000], _REPS [3]. It prints one row per shape x mode x rep and PASSES whatever the
// numbers are: read events/s shared against keyed for the same shape and rep, with lock% (samples
// with a session in a Lock wait) and p99ms (batch latency) as the why. The deadlocks and refused
// columns are reported, not asserted, so a passing run is no evidence that either was 0.
//
// The two modes drive the SAME writers (DefaultProjectionWriters, each merging up to
// DefaultProjectionMaxBatch events) through the real MergeProjectionBatch:
//
//   - shared: one stream, each writer takes the next events waiting for it, which is what the
//     processor does today (two writers can hold events for one device at once);
//   - keyed: an event goes to the writer its device token hashes to, so a device is never held by
//     two writers at once. This is the routing the issue asks about, simulated at the dispatcher
//     with no change to the merge.
//
// Lock waits are SAMPLED from pg_stat_activity (sessions on this database in a Lock wait) every
// few milliseconds on a connection of their own, and deadlocks are the pg_stat_database delta.
package model

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type contentionShape struct {
	name string
	// hotShare is the fraction of events that belong to ONE hot device; the rest are uniform
	// over fleet devices.
	hotShare float64
}

type contentionResult struct {
	events       int
	elapsed      time.Duration
	batches      int
	refused      int
	deadlocks    int64
	samples      int
	lockWaitAvg  float64 // mean sessions in a Lock wait per sample
	lockWaitMax  int
	lockWaitFrac float64 // fraction of samples with at least one session in a Lock wait
	waitEvents   map[string]int
	p50, p99     time.Duration // batch latency
}

func BenchmarkProjectionContention(t *testing.B) {
	if os.Getenv("DC_CONTENTION") != "1" {
		t.Skip("set DC_CONTENTION=1 to run the contention measurement")
	}
	events := atoiEnv("DC_CONTENTION_EVENTS", 30000)
	writers := atoiEnv("DC_CONTENTION_WRITERS", 10)
	maxBatch := atoiEnv("DC_CONTENTION_BATCH", 32)
	fleet := atoiEnv("DC_CONTENTION_FLEET", 10000)
	reps := atoiEnv("DC_CONTENTION_REPS", 3)

	api := newPostgresProjectionApi(t, "contention")
	probe := newPostgresReplica(t, api)

	shapes := []contentionShape{{"broad", 0}, {"hot0.1", 0.001}, {"hot1", 0.01}, {"hot10", 0.10}, {"hot50", 0.50}, {"hot100", 1.0}}
	fmt.Printf("\ncontention: events=%d writers=%d maxBatch=%d fleet=%d reps=%d\n", events, writers, maxBatch, fleet, reps)
	fmt.Printf("%-8s %-7s %4s %9s %8s %7s %9s %8s %8s %8s %6s  %s\n", "shape", "mode", "rep", "events/s", "batches",
		"refused", "deadlocks", "lockAvg", "lockMax", "lock%", "p99ms", "wait events")
	for _, sh := range shapes {
		for rep := 1; rep <= reps; rep++ {
			for _, keyed := range []bool{false, true} { // interleaved so drift hits both
				truncateProjection(t, api)
				r := runContention(t, api, probe, sh, keyed, events, writers, maxBatch, fleet, int64(rep))
				mode := "shared"
				if keyed {
					mode = "keyed"
				}
				fmt.Printf("%-8s %-7s %4d %9.0f %8d %7d %9d %8.2f %8d %7.1f%% %6.1f  %v\n", sh.name, mode, rep,
					float64(r.events)/r.elapsed.Seconds(), r.batches, r.refused, r.deadlocks, r.lockWaitAvg, r.lockWaitMax,
					100*r.lockWaitFrac, float64(r.p99.Microseconds())/1000, r.waitEvents)
			}
		}
	}
}

func atoiEnv(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

// contentionEvents builds the event stream for a shape: hotShare of the events go to "hot-0", the
// rest to a uniformly drawn fleet device. Seeded so every mode sees the same stream.
func contentionEvents(sh contentionShape, n, fleet int, seed int64) []ProjectionUpdate {
	rnd := rand.New(rand.NewSource(seed))
	base := time.Now().UTC().Truncate(time.Microsecond)
	out := make([]ProjectionUpdate, n)
	for i := range out {
		dev := "hot-0"
		if rnd.Float64() >= sh.hotShare {
			dev = fmt.Sprintf("dev-%05d", rnd.Intn(fleet))
		}
		at := base.Add(time.Duration(i) * time.Millisecond)
		out[i] = ProjectionUpdate{
			Tenant: "acme", DeviceToken: dev, OccurredAt: at,
			Measurements: []LatestMeasurementInput{
				{Name: "temperature", Value: sql.NullFloat64{Float64: 21.5, Valid: true}, OccurredTime: at},
				{Name: "humidity", Value: sql.NullFloat64{Float64: 40.25, Valid: true}, OccurredTime: at},
				{Name: "pressure", Value: sql.NullFloat64{Float64: 1013.5, Valid: true}, OccurredTime: at},
			},
		}
	}
	return out
}

func runContention(t testing.TB, api, probe *Api, sh contentionShape, keyed bool, n, writers, maxBatch, fleet int,
	seed int64) contentionResult {
	t.Helper()
	ctx := context.Background()
	events := contentionEvents(sh, n, fleet, seed)

	// One queue per writer in keyed mode, one shared queue otherwise. The queues are buffered to
	// hold the whole stream so the producer never gates the writers: this measures the merge, not
	// the reader.
	queues := make([]chan ProjectionUpdate, 1)
	if keyed {
		queues = make([]chan ProjectionUpdate, writers)
	}
	for i := range queues {
		queues[i] = make(chan ProjectionUpdate, n)
	}
	for _, u := range events {
		q := queues[0]
		if keyed {
			h := fnv.New32a()
			_, _ = h.Write([]byte(u.Tenant + "/" + u.DeviceToken))
			q = queues[int(h.Sum32())%writers]
		}
		q <- u
	}
	for _, q := range queues {
		close(q)
	}

	deadBefore := deadlockCount(t, probe)
	stop := make(chan struct{})
	var sampWG sync.WaitGroup
	var mu sync.Mutex
	res := contentionResult{waitEvents: map[string]int{}}
	var lockSum, lockedSamples int
	sampWG.Add(1)
	go func() {
		defer sampWG.Done()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			rows, err := probe.RDB.Database.WithContext(ctx).Raw(
				`SELECT wait_event_type, wait_event, count(*) FROM pg_stat_activity
				 WHERE datname = current_database() AND pid <> pg_backend_pid() AND state = 'active'
				   AND wait_event IS NOT NULL GROUP BY 1, 2`).Rows()
			if err != nil {
				continue
			}
			locks := 0
			mu.Lock()
			res.samples++
			for rows.Next() {
				var typ, ev string
				var c int
				if rows.Scan(&typ, &ev, &c) == nil {
					if typ == "Lock" {
						locks += c
					}
					if typ == "Lock" || typ == "LWLock" {
						res.waitEvents[typ+":"+ev] += c
					}
				}
			}
			rows.Close()
			lockSum += locks
			if locks > 0 {
				lockedSamples++
			}
			if locks > res.lockWaitMax {
				res.lockWaitMax = locks
			}
			mu.Unlock()
		}
	}()

	var batches, refused atomic.Int64
	var latMu sync.Mutex
	var lats []time.Duration
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		q := queues[0]
		if keyed {
			q = queues[w]
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				first, ok := <-q
				if !ok {
					return
				}
				batch := []ProjectionUpdate{first}
			fill:
				for len(batch) < maxBatch { // take what is already waiting, never linger
					select {
					case u, ok := <-q:
						if !ok {
							break fill
						}
						batch = append(batch, u)
					default:
						break fill
					}
				}
				t0 := time.Now()
				err := api.MergeProjectionBatch(ctx, batch)
				d := time.Since(t0)
				batches.Add(1)
				if err != nil {
					refused.Add(1)
				}
				latMu.Lock()
				lats = append(lats, d)
				latMu.Unlock()
			}
		}()
	}
	wg.Wait()
	res.elapsed = time.Since(start)
	close(stop)
	sampWG.Wait()

	res.events = n
	res.batches = int(batches.Load())
	res.refused = int(refused.Load())
	res.deadlocks = deadlockCount(t, probe) - deadBefore
	if res.samples > 0 {
		res.lockWaitAvg = float64(lockSum) / float64(res.samples)
		res.lockWaitFrac = float64(lockedSamples) / float64(res.samples)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	if len(lats) > 0 {
		res.p50, res.p99 = lats[len(lats)/2], lats[len(lats)*99/100]
	}
	return res
}

func deadlockCount(t testing.TB, probe *Api) int64 {
	t.Helper()
	var d int64
	if err := probe.RDB.Database.Raw(`SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).
		Scan(&d).Error; err != nil {
		t.Fatalf("read deadlocks: %v", err)
	}
	return d
}
