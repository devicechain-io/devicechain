// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The paced benchmark: BenchmarkPersistBatch's saturation shapes answer "how fast can the
// writers go", which is not the question that decides whether batching helps at the rates a
// pod actually sees. This answers the other one. A feeder hands messages to the REAL
// EventPersistenceProcessor's channel at a fixed, even rate, one device per event across a
// fleet of 3,000, and the run reports how full the batches came out and what it cost.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -run '^$' \
//	  -bench 'PersistBatch/paced' -benchtime=30000x -count=1 -p 1 ./processor/
//
// DC_BENCH_PACED selects the arms, as a comma-separated list of
// rate:commitMs:lingerMs:writers (default below). commitMs is latency injected inside the
// transaction before its COMMIT, standing in for the synchronous-standby wait of a
// replicated event store; the run reports the commit duration it actually measured, which
// is what the reproduction model uses, not the number asked for.
package processor

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	emconfig "github.com/devicechain-io/dc-event-management/config"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// pacedFleet is how many devices the feeder cycles through: 3,000 devices at 1 event a
// second each is the 3,000 events a second the r25 rounds drove.
const pacedFleet = 3000

// slowCommitApi injects latency inside every transaction, after its statements and before
// the COMMIT, so the connection and the transaction stay open for it the way they do while
// a synchronous standby is awaited.
type slowCommitApi struct {
	emmodel.EventManagementApi
	d time.Duration
}

func (a slowCommitApi) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return a.EventManagementApi.PersistInTx(ctx, func(tx *gorm.DB) error {
		if err := fn(tx); err != nil {
			return err
		}
		time.Sleep(a.d)
		return nil
	})
}

// pacedAck records when its message was acknowledged.
type pacedAck struct {
	at  []atomic.Int64
	idx int
	n   *atomic.Int64
}

func (a pacedAck) Ack() error {
	a.at[a.idx].Store(time.Now().UnixNano())
	a.n.Add(1)
	return nil
}

type pacedArm struct {
	rate, commitMs, lingerMs, writers int
}

func pacedArms(tb testing.TB) []pacedArm {
	spec := os.Getenv("DC_BENCH_PACED")
	if spec == "" {
		spec = "100:0:10:10,1500:0:10:10,3000:0:10:10,1500:20:10:10"
	}
	var arms []pacedArm
	for _, s := range strings.Split(spec, ",") {
		f := strings.Split(strings.TrimSpace(s), ":")
		if len(f) != 4 {
			tb.Fatalf("DC_BENCH_PACED entry %q is not rate:commitMs:lingerMs:writers", s)
		}
		var v [4]int
		for i := range f {
			n, err := strconv.Atoi(f[i])
			if err != nil {
				tb.Fatalf("DC_BENCH_PACED entry %q: %v", s, err)
			}
			v[i] = n
		}
		arms = append(arms, pacedArm{v[0], v[1], v[2], v[3]})
	}
	return arms
}

// histogramOf sums the histogram metrics of reg whose name ends with suffix.
func histogramOf(reg *prometheus.Registry, suffix string) (sum float64, count uint64) {
	families, _ := reg.Gather()
	for _, f := range families {
		if !strings.HasSuffix(f.GetName(), suffix) {
			continue
		}
		for _, m := range f.GetMetric() {
			if h := m.GetHistogram(); h != nil {
				sum += h.GetSampleSum()
				count += h.GetSampleCount()
			}
		}
	}
	return
}

// pgStatTotalMillis is the server's total statement execution time so far, or ok=false when
// pg_stat_statements is not installed on it.
func pgStatTotalMillis(mgr *rdb.RdbManager) (ms float64, ok bool) {
	sqldb, err := mgr.Database.DB()
	if err != nil {
		return 0, false
	}
	if err := sqldb.QueryRow(`SELECT coalesce(sum(total_exec_time),0) FROM pg_stat_statements`).Scan(&ms); err != nil {
		return 0, false
	}
	return ms, true
}

func percentileMs(sorted []int64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return float64(sorted[i]) / 1e6
}

// benchPersistPaced runs BenchmarkPersistBatch's paced arms.
func benchPersistPaced(b *testing.B, mgr *rdb.RdbManager) {
	// Lifecycle chatter at info would interleave with the result line.
	level := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(level) })
	base := time.Now().UTC().Truncate(time.Hour)
	var invocation int
	for _, arm := range pacedArms(b) {
		arm := arm
		name := fmt.Sprintf("paced/rate=%d/commit=%dms/linger=%dms/writers=%d",
			arm.rate, arm.commitMs, arm.lingerMs, arm.writers)
		b.Run(name, func(b *testing.B) { benchPacedArm(b, mgr, arm, &invocation, base) })
	}
}

func benchPacedArm(b *testing.B, mgr *rdb.RdbManager, arm pacedArm, invocation *int, base time.Time) {
	*invocation++
	inv := *invocation
	truncateEvents(b, mgr)
	n := b.N
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "bench", FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(reg)
	var api emmodel.EventManagementApi = emmodel.NewApi(mgr)
	if arm.commitMs > 0 {
		api = slowCommitApi{EventManagementApi: api, d: time.Duration(arm.commitMs) * time.Millisecond}
	}
	linger := arm.lingerMs
	proc := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), api, NewPersistMetrics(ms),
		WithPersistence(emconfig.PersistenceConfiguration{Writers: arm.writers, LingerMillis: &linger}))
	if err := proc.Initialize(context.Background()); err != nil {
		b.Fatalf("initialize: %v", err)
	}
	acksAt := make([]atomic.Int64, n+1)
	var acked atomic.Int64
	build := func(i int) messaging.Message {
		ev := batchEvent(i, false, base.Add(time.Duration(inv)*time.Minute))
		ev.SourceDeviceToken = fmt.Sprintf("dev-%d", i%pacedFleet)
		ev.Anchors = nil
		ev.OccurredTime = ev.OccurredTime.Add(time.Duration(i) * time.Microsecond)
		bytes, err := resolvedevent.MarshalResolvedEvent(&ev)
		if err != nil {
			b.Fatalf("marshal a resolved event: %v", err)
		}
		return messaging.NewConsumedMessage("instance1."+fenceCostTenant+".resolved-events", bytes, 1, nil,
			pacedAck{at: acksAt, idx: i, n: &acked})
	}
	// Warm-up: the chunk exists and the driver's statement cache is primed (slot n is
	// its, outside the run's).
	proc.handOff(context.Background(), build(n))
	for acked.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	acked.Store(0)

	msgs := make([]messaging.Message, n)
	for i := range msgs {
		msgs[i] = build(i)
	}
	handoff := make([]int64, n)
	dbMs0, dbOK := pgStatTotalMillis(mgr)
	sum0, cnt0 := histogramOf(reg, "persist_batch_size")
	csum0, ccnt0 := histogramOf(reg, "persist_commit_duration_seconds")
	cpu0, cpuOK := processCPU()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	b.ResetTimer()
	start := time.Now()
	interval := time.Second / time.Duration(arm.rate)
	for i := 0; i < n; i++ {
		// An even schedule: each event has a due time, and a feeder that fell behind catches
		// up at once rather than stretching the rate.
		if d := time.Until(start.Add(time.Duration(i) * interval)); d > 0 {
			time.Sleep(d)
		}
		handoff[i] = time.Now().UnixNano()
		proc.handOff(context.Background(), msgs[i])
	}
	deadline := time.Now().Add(5 * time.Minute)
	for acked.Load() < int64(n) {
		if time.Now().After(deadline) {
			b.Fatalf("%d of %d events acknowledged after 5 minutes", acked.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)
	cpu1, cpuOK1 := processCPU()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	dbMs1, dbOK1 := pgStatTotalMillis(mgr)
	sum1, cnt1 := histogramOf(reg, "persist_batch_size")
	csum1, ccnt1 := histogramOf(reg, "persist_commit_duration_seconds")
	b.StopTimer()
	if err := proc.ExecuteStop(context.Background()); err != nil {
		b.Fatalf("stop: %v", err)
	}

	commits := float64(cnt1 - cnt0)
	b.ReportMetric((sum1-sum0)/commits, "events/commit")
	b.ReportMetric(commits/elapsed.Seconds(), "commits/s")
	b.ReportMetric(1000*(csum1-csum0)/float64(ccnt1-ccnt0), "commit-ms")
	b.ReportMetric(float64(n)/elapsed.Seconds(), "events/s")
	if cpuOK && cpuOK1 {
		b.ReportMetric(float64((cpu1-cpu0).Microseconds())/float64(n), "cpu-us/ev")
	}
	if dbOK && dbOK1 {
		b.ReportMetric((dbMs1-dbMs0)*1000/float64(n), "db-us/ev")
	}
	b.ReportMetric(float64(m1.Mallocs-m0.Mallocs)/float64(n), "allocs/ev")
	lat := make([]int64, n)
	for i := range lat {
		lat[i] = acksAt[i].Load() - handoff[i]
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	b.ReportMetric(percentileMs(lat, 0.50), "ack-p50-ms")
	b.ReportMetric(percentileMs(lat, 0.99), "ack-p99-ms")

	// Admission cost per event (tenant from the subject + decode), measured untimed on a
	// worker of its own over fresh messages.
	const admits = 20000
	w := NewEventPersistenceWorker(1, api, nil, func(error, messaging.Message) {}, nil, nil)
	am := make([]messaging.Message, admits)
	for i := range am {
		am[i] = build(i % n)
	}
	t0 := time.Now()
	for _, m := range am {
		w.admit(context.Background(), m, func(string) {})
	}
	b.ReportMetric(float64(time.Since(t0).Microseconds())/admits, "admit-us/ev")
}
