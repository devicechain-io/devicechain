// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The paced mode of BenchmarkProjectionWriters. The saturation shapes ask how fast the
// writers can go; this asks what they cost at the rates a pod actually sees. A feeder hands
// messages to the REAL StateProcessor's channel at a fixed, even rate, one device per event
// across a fleet of 3,000, and the run reports how full the batches came out and what it
// cost.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -run '^$' \
//	  -bench 'ProjectionWriters/paced' -benchtime=60000x -count=1 -p 1 ./processor/
//
// DC_BENCH_PACED selects the arms, as a comma-separated list of
// rate:commitMs:lingerMs:writers:mix (default below), mix being
//
//   - plain: every event a measurement;
//   - change1: one event in a hundred is a presence claim (the state-change mix);
//   - storm10: one in ten is, while the inactivity sweep runs alongside (the
//     reconnect-storm mix). The sweep runs every 2 s against a clock set so that a device
//     silent for 900 ms (of its 1 s cycle) counts as past the 600 s timeout: about a
//     tenth of the fleet is flipped inactive per pass, in contention with the batches.
//
// commitMs is latency injected after each batch merge returns, standing in for the
// synchronous-standby wait of a replicated relational store. It delays the writer and the
// acknowledgements exactly as a slow COMMIT does, but holds no row locks while it does, so
// it understates contention on a hot fleet; this fleet is 3,000 devices at one event a
// second each, where that is small. The run reports the merge duration it actually
// measured, which is what the reproduction model uses, not the number asked for.
package processor

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
)

// pacedFleet is how many devices the feeder cycles through: 3,000 devices at 1 event a
// second each is the 3,000 events a second the r25 rounds drove.
const pacedFleet = 3000

// pacedProjection is the projection configuration a paced arm runs: the shipped defaults
// but for what the arm sets.
func pacedProjection(writers, lingerMs int) config.ProjectionConfiguration {
	return config.ProjectionConfiguration{Writers: writers, LingerMillis: lingerMs}
}

// slowMergeApi times every batch merge and injects latency after it.
type slowMergeApi struct {
	model.DeviceStateApi
	d      atomic.Int64 // injected latency, ns
	nanos  atomic.Int64 // total time of merges that committed, injected latency included
	merges atomic.Int64
}

func (a *slowMergeApi) MergeProjectionBatch(ctx context.Context, updates []model.ProjectionUpdate) error {
	t0 := time.Now()
	err := a.DeviceStateApi.MergeProjectionBatch(ctx, updates)
	if err == nil {
		if d := a.d.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		a.nanos.Add(int64(time.Since(t0)))
		a.merges.Add(1)
	}
	return err
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
	mix                               string
}

func pacedArms(tb testing.TB) []pacedArm {
	spec := os.Getenv("DC_BENCH_PACED")
	if spec == "" {
		spec = "100:0:10:10:plain,1500:0:10:10:plain,3000:0:10:10:plain,3000:2:10:10:plain,3000:2:10:10:change1,1500:2:10:10:storm10"
	}
	var arms []pacedArm
	for _, s := range strings.Split(spec, ",") {
		f := strings.Split(strings.TrimSpace(s), ":")
		if len(f) != 5 {
			tb.Fatalf("DC_BENCH_PACED entry %q is not rate:commitMs:lingerMs:writers:mix", s)
		}
		var v [4]int
		for i := 0; i < 4; i++ {
			n, err := strconv.Atoi(f[i])
			if err != nil {
				tb.Fatalf("DC_BENCH_PACED entry %q: %v", s, err)
			}
			v[i] = n
		}
		if f[4] != "plain" && f[4] != "change1" && f[4] != "storm10" {
			tb.Fatalf("DC_BENCH_PACED entry %q: mix must be plain, change1 or storm10", s)
		}
		arms = append(arms, pacedArm{v[0], v[1], v[2], v[3], f[4]})
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
	return float64(sorted[int(p*float64(len(sorted)-1))]) / 1e6
}

// pacedStateChange is a consumed presence claim for device. Sessions rise with i, so every
// claim is newer than the last and none is refused as stale.
func pacedStateChange(tb testing.TB, device string, i int, at time.Time, ack messaging.Acknowledger) messaging.Message {
	tb.Helper()
	state := "CONNECTED"
	if (i/10)%2 == 1 {
		state = "DISCONNECTED"
	}
	event := &resolvedevent.ResolvedEvent{
		Source: mqttTestSource, SourceDeviceToken: device, EventType: eventtype.StateChange,
		OccurredTime: at, ProcessedTime: at,
		Payload: &resolvedevent.ResolvedStateChangePayload{State: state, SessionId: uint64(i) + 1},
	}
	encoded, err := resolvedevent.MarshalResolvedEvent(event)
	if err != nil {
		tb.Fatalf("marshal resolved event: %v", err)
	}
	return messaging.NewConsumedMessage("instance1.acme.resolved-events", encoded, 1, nil, ack)
}

var pacedWarmed bool

// benchProjectionPaced runs BenchmarkProjectionWriters' paced arms.
func benchProjectionPaced(b *testing.B, mgr *rdb.RdbManager) {
	level := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(level) })
	for _, arm := range pacedArms(b) {
		arm := arm
		name := fmt.Sprintf("paced/rate=%d/commit=%dms/linger=%dms/writers=%d/mix=%s",
			arm.rate, arm.commitMs, arm.lingerMs, arm.writers, arm.mix)
		b.Run(name, func(b *testing.B) { benchPacedArm(b, mgr, arm) })
	}
}

func newPacedProcessor(tb testing.TB, mgr *rdb.RdbManager, cfg config.ProjectionConfiguration) (*StateProcessor, *prometheus.Registry, *slowMergeApi) {
	tb.Helper()
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "bench", FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(reg)
	api := &slowMergeApi{DeviceStateApi: newBenchProcessor(mgr).Api}
	sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), api, NewStateMetrics(ms),
		ms.NewPeriodicTaskMetrics("paced"), WithProjection(cfg))
	if err := sp.Initialize(context.Background()); err != nil {
		tb.Fatalf("initialize: %v", err)
	}
	return sp, reg, api
}

func benchPacedArm(b *testing.B, mgr *rdb.RdbManager, arm pacedArm) {
	n := b.N
	if !pacedWarmed {
		// Every device's row exists before anything is timed: two FIRST events for one
		// device race on its unique index, and a paced run is not about first sight.
		warm, _, _ := newPacedProcessor(b, mgr, config.ProjectionConfiguration{Writers: 1, MaxBatch: 1})
		var acked atomic.Int64
		at := time.Now().UTC()
		for i := 0; i < pacedFleet; i++ {
			warm.handOff(context.Background(), benchMeasurement(b, "acme", fmt.Sprintf("pf-%d", i), at, countingAck{&acked}))
			for acked.Load() < int64(i+1) {
				time.Sleep(100 * time.Microsecond)
			}
		}
		if err := warm.ExecuteStop(context.Background()); err != nil {
			b.Fatalf("stop: %v", err)
		}
		pacedWarmed = true
	}

	sp, reg, api := newPacedProcessor(b, mgr, pacedProjection(arm.writers, arm.lingerMs))
	api.d.Store(int64(time.Duration(arm.commitMs) * time.Millisecond))

	// Event times rise from the moment the arm starts, so no event is older than what an
	// earlier arm stored.
	t0 := time.Now().UTC()
	acksAt := make([]atomic.Int64, n)
	var acked atomic.Int64
	msgs := make([]messaging.Message, n)
	for i := range msgs {
		dev := fmt.Sprintf("pf-%d", i%pacedFleet)
		at := t0.Add(time.Duration(i) * (time.Second / time.Duration(arm.rate)))
		ack := pacedAck{at: acksAt, idx: i, n: &acked}
		switch {
		case arm.mix == "change1" && i%100 == 0, arm.mix == "storm10" && i%10 == 0:
			msgs[i] = pacedStateChange(b, dev, i, at, ack)
		default:
			msgs[i] = benchMeasurement(b, "acme", dev, at, ack)
		}
	}
	handoff := make([]int64, n)

	stopSweep := make(chan struct{})
	var sweeps sync.WaitGroup
	if arm.mix == "storm10" {
		sweeps.Add(1)
		go func() {
			defer sweeps.Done()
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-stopSweep:
					return
				case <-tick.C:
					_, _ = sp.Api.SweepInactive(core.WithSystemContext(context.Background()), time.Now().UTC().Add(600*time.Second-900*time.Millisecond))
				}
			}
		}()
	}

	dbMs0, dbOK := pgStatTotalMillis(mgr)
	sum0, cnt0 := histogramOf(reg, "state_batch_size")
	nanos0, merges0 := api.nanos.Load(), api.merges.Load()
	cpu0, cpuOK := processCPU()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	b.ResetTimer()
	start := time.Now()
	interval := time.Second / time.Duration(arm.rate)
	for i := 0; i < n; i++ {
		if d := time.Until(start.Add(time.Duration(i) * interval)); d > 0 {
			time.Sleep(d)
		}
		handoff[i] = time.Now().UnixNano()
		sp.handOff(context.Background(), msgs[i])
	}
	deadline := time.Now().Add(5 * time.Minute)
	for acked.Load() < int64(n) {
		if time.Now().After(deadline) {
			b.Fatalf("%d of %d events merged after 5 minutes", acked.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)
	cpu1, cpuOK1 := processCPU()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	dbMs1, dbOK1 := pgStatTotalMillis(mgr)
	sum1, cnt1 := histogramOf(reg, "state_batch_size")
	nanos1, merges1 := api.nanos.Load(), api.merges.Load()
	b.StopTimer()
	close(stopSweep)
	sweeps.Wait()
	if err := sp.ExecuteStop(context.Background()); err != nil {
		b.Fatalf("stop: %v", err)
	}

	commits := float64(cnt1 - cnt0)
	b.ReportMetric((sum1-sum0)/commits, "events/commit")
	b.ReportMetric(commits/elapsed.Seconds(), "commits/s")
	b.ReportMetric(float64(nanos1-nanos0)/1e6/float64(merges1-merges0), "commit-ms")
	b.ReportMetric(float64(n)/elapsed.Seconds(), "events/s")
	if cpuOK && cpuOK1 {
		b.ReportMetric(float64((cpu1-cpu0).Microseconds())/float64(n), "cpu-us/ev")
	}
	if dbOK && dbOK1 {
		b.ReportMetric((dbMs1-dbMs0)*1000/float64(n), "db-us/ev")
	}
	b.ReportMetric(float64(m1.Mallocs-m0.Mallocs)/float64(n), "allocs/ev")
	b.ReportMetric(1000*float64(testFallbacks(reg))/float64(n), "fallbacks/1000ev")
	lat := make([]int64, n)
	for i := range lat {
		lat[i] = acksAt[i].Load() - handoff[i]
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	b.ReportMetric(percentileMs(lat, 0.50), "ack-p50-ms")
	b.ReportMetric(percentileMs(lat, 0.99), "ack-p99-ms")

	// Admission cost per event (tenant from the subject + decode + mapping), measured
	// untimed over fresh messages.
	const admits = 20000
	probe := &StateProcessor{}
	am := make([]messaging.Message, admits)
	for i := range am {
		am[i] = msgs[i%n]
	}
	a0 := time.Now()
	for _, m := range am {
		probe.admit(context.Background(), m)
	}
	b.ReportMetric(float64(time.Since(a0).Microseconds())/admits, "admit-us/ev")
}

// testFallbacks is state_batch_fallbacks_total.
func testFallbacks(reg *prometheus.Registry) float64 {
	families, _ := reg.Gather()
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), "state_batch_fallbacks_total") {
			var v float64
			for _, m := range f.GetMetric() {
				v += m.GetCounter().GetValue()
			}
			return v
		}
	}
	return 0
}
