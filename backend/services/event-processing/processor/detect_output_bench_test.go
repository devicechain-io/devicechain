// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog"
)

// BenchmarkDetectOutputBaseline is variant A of the DETECT output-path comparison: the path as it
// stands, where each detection's publish waits for its broker acknowledgement and is followed on
// the loop by a synchronous rule-health write (an erasure-fence read and an upsert), and the
// snapshot is saved and the inputs acknowledged only after all of that.
//
// One op is one INPUT event, through handle and its share of the checkpoints (every 1000 inputs,
// the service default, plus the final one). Each case reports where the time went, read from the
// production metrics the run recorded:
//
//	events/s, dets/s, dets/event   throughput, counted from what the broker acknowledged
//	handle-ns/event                decode, fan-out and the engine (outside every checkpoint)
//	ckpt-ns/event                  everything inside checkpoints, which the next four divide:
//	ack-ns/det                     waiting for the broker's acknowledgement of one derived event
//	stat-ns/det                    the rule-health write after it (fence read + upsert)
//	serialize-ns/event, save-ns/event   the snapshot, per input
//	ckptother-ns/event             the remainder: building and marshalling events, the ack
//	fence-ns/det, upsert-ns/det    (split cases only) the rule-health write's two statements;
//	                               upsert excludes the fence read nested inside it
//
// The broker and the database here are stand-ins with a FIXED latency in front of them (the
// rule-health store is real sqlite with the production callback chain behind its delay): this is
// a simulation that isolates the shape of the loop's waits, not a capacity figure. The real
// broker and PostgreSQL runs are BenchmarkDetectOutputBaselineReal (-tags integration).
//
// Every run is judged by the output oracle afterwards, outside the timer; a run that fails it
// fails the benchmark rather than reporting a number for a broken path. Run it repeated and
// compared, so a difference is read against its own noise:
//
//	go test ./processor -run '^$' -bench BenchmarkDetectOutputBaseline -benchtime 2000x -count 7 -benchmem | tee a.txt
//	benchstat a.txt
func BenchmarkDetectOutputBaseline(b *testing.B) {
	for _, c := range []outputBenchCase{
		{name: "broker=0ms/stats=0ms/fire=100%/lanes=32", fx: benchFixture(16, 1)},
		{name: "broker=0ms/stats=0ms/fire=100%/lanes=1", fx: outputFixture{tenants: []string{"acme"}, devices: 1, rulesPerTenant: 1, firePeriod: 1}},
		{name: "broker=0ms/stats=0ms/fire=10%/lanes=32", fx: benchFixture(16, 10)},
		{name: "broker=0ms/stats=0ms/fire=0%/lanes=32", fx: benchFixture(16, 0)},
		{name: "broker=0ms/stats=none/fire=100%/lanes=32", fx: benchFixture(16, 1), noStats: true},
		{name: "broker=1ms/stats=0ms/fire=100%/lanes=32", fx: benchFixture(16, 1), brokerAck: time.Millisecond},
		{name: "broker=8ms/stats=0ms/fire=10%/lanes=32", fx: benchFixture(16, 10), brokerAck: 8 * time.Millisecond},
		{name: "broker=0ms/stats=1ms/fire=100%/lanes=32", fx: benchFixture(16, 1), statRTT: time.Millisecond},
		{name: "broker=0ms/stats=10ms/fire=10%/lanes=32", fx: benchFixture(16, 10), statRTT: 10 * time.Millisecond},
		{name: "broker=1ms/stats=1ms/fire=100%/lanes=32", fx: benchFixture(16, 1), brokerAck: time.Millisecond, statRTT: time.Millisecond},
		{name: "split=fence/broker=0ms/stats=0ms/fire=100%/lanes=32", fx: benchFixture(16, 1), splitStatements: true},
	} {
		b.Run(c.name, func(b *testing.B) { benchOutput(b, c) })
	}
}

// BenchmarkDetectOutputStats is the A/B comparison for the rule-health counters: variant A
// writes them inline (the baseline), variant B through the production accumulator. Every case
// runs A then B inside the same parent, so with -count N the two interleave N times rather than
// one running all its repetitions first. Everything else is identical: the same source, rules,
// keys, broker stand-in and checkpoint store, so snapshot size and evaluation cost cannot
// masquerade as an output gain. The stand-in store delays are a SIMULATION of database round
// trips, not a capacity figure; the PostgreSQL and JetStream runs are
// BenchmarkDetectOutputStatsReal (-tags integration).
//
// B's figures include its final flush (settle-ns/event): the run is not done until the store
// holds every admitted fire. Its rule-health write time (offloop-stat-ns/det) is reported but
// is not loop time.
//
//	go test ./processor -run '^$' -bench BenchmarkDetectOutputStats -benchtime 2000x -count 7 -benchmem | tee ab.txt
//	benchstat -col /variant ab.txt
func BenchmarkDetectOutputStats(b *testing.B) {
	for _, c := range []outputBenchCase{
		{name: "stats=0ms/fire=100%/lanes=32", fx: benchFixture(16, 1)},
		{name: "stats=0ms/fire=10%/lanes=32", fx: benchFixture(16, 10)},
		{name: "stats=0ms/fire=0%/lanes=32", fx: benchFixture(16, 0)},
		{name: "stats=0ms/fire=100%/lanes=1", fx: outputFixture{tenants: []string{"acme"}, devices: 1, rulesPerTenant: 1, firePeriod: 1}},
		{name: "stats=0ms/fire=100%/rules=256", fx: outputFixture{tenants: []string{"acme", "beta"}, devices: 2, rulesPerTenant: 128, firePeriod: 1}},
		{name: "stats=1ms/fire=100%/lanes=32", fx: benchFixture(16, 1), statRTT: time.Millisecond},
		{name: "stats=10ms/fire=10%/lanes=32", fx: benchFixture(16, 10), statRTT: 10 * time.Millisecond},
		{name: "stats=100ms/fire=5%/lanes=32", fx: benchFixture(16, 20), statRTT: 100 * time.Millisecond},
	} {
		for _, v := range []string{"A", "B"} {
			c := c
			c.batched = v == "B"
			b.Run("variant="+v+"/"+c.name, func(b *testing.B) { benchOutput(b, c) })
		}
	}
	// B alone: a store that never answers. A cannot run this case at all (it would hold the loop
	// for the write timeout on every fire); B must keep detection at its healthy rate and turn the
	// lost writes into counted discards.
	b.Run("variant=B/stats=hang/fire=100%/lanes=32", func(b *testing.B) {
		benchOutput(b, outputBenchCase{fx: benchFixture(16, 1), batched: true, batchStore: failingBatchStore{hang: true}})
	})
}

// benchFixture is the benchmarks' source: two tenants, devicesPerTenant devices each, one rule
// per tenant, crossing every firePeriod samples (0: never).
func benchFixture(devicesPerTenant, firePeriod int) outputFixture {
	return outputFixture{tenants: []string{"acme", "beta"}, devices: devicesPerTenant, rulesPerTenant: 1, firePeriod: firePeriod}
}

// outputBenchCase is one arm of an output-path benchmark.
type outputBenchCase struct {
	name string
	fx   outputFixture
	// brokerAck is the stand-in broker's acknowledgement latency (ignored when broker is set).
	brokerAck time.Duration
	// broker, when set, is a real derived-event writer, built per run.
	broker func(b *testing.B) messaging.MessageWriter
	// statRTT is a fixed delay in front of the sqlite rule-health store; noStats wires none.
	statRTT time.Duration
	noStats bool
	// stats, when set, is a real rule-health store (with its statement timer, or nil), built per run.
	stats func(b *testing.B) (fireStatStore, *statementTimer)
	// splitStatements times the sqlite store's statements, to split fence from upsert.
	splitStatements bool
	// snapshots, when set, is a real checkpoint store, built per run.
	snapshots func(b *testing.B) *rdb.RdbManager
	variant   outputVariant
	// batched writes the rule-health store through the production accumulator (variant B)
	// rather than inline; the store must then also take batches (rulehealth.Store).
	batched bool
	// batchStore, when set with batched, replaces the store entirely (a failing one, say).
	batchStore rulehealth.Store
}

func benchOutput(b *testing.B, c outputBenchCase) {
	b.StopTimer()
	// Each run's setup and teardown log (a fresh engine, a new stream, a manager disconnecting
	// as it stops), and a log line written between a result's name and its figures makes the
	// line unreadable to benchstat. Errors still print; a broken run fails on its own anyway.
	// Benchmarks run after every test, so no log-capture test is affected.
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	spec := outputRigSpec{fx: c.fx, variant: c.variant}
	if spec.variant.checkpoint == nil {
		spec.variant = variantA
	}
	if c.broker != nil {
		spec.broker = c.broker(b)
	} else {
		spec.broker = latencyWriter{ack: c.brokerAck}
	}
	if c.snapshots != nil {
		spec.snapshots = c.snapshots(b)
	}
	var timer *statementTimer
	switch {
	case c.stats != nil:
		spec.stats, timer = c.stats(b)
	case !c.noStats:
		if c.splitStatements {
			timer = &statementTimer{}
		}
		var store fireStatStore = newSqliteRuleStatStore(b, timer)
		if c.statRTT > 0 {
			store = latencyStatStore{inner: store, rtt: c.statRTT}
		}
		spec.stats = store
	}
	if c.batched {
		switch {
		case c.batchStore != nil:
			spec.batch = c.batchStore
			spec.statLossExpected = true
			spec.batchCfg = rulehealth.DefaultConfig()
			spec.batchCfg.WriteTimeout = rulehealth.MinWriteTimeout
		case spec.stats != nil:
			spec.batch = spec.stats.(rulehealth.Store)
		}
		spec.stats = nil
		if spec.variant.name == variantA.name {
			spec.variant = variantB
		}
	}
	r := newOutputRig(b, spec)
	msgs := c.fx.messages(b, b.N, r.log)
	if timer != nil { // the fixture's own setup statements are not the run's
		timer.fenceNs.Store(0)
		timer.fenceN.Store(0)
		timer.otherNs.Store(0)
		timer.otherN.Store(0)
	}

	b.ReportAllocs()
	b.StartTimer()
	start := time.Now()
	r.run(b, msgs)
	elapsed := time.Since(start)
	b.StopTimer()

	v := judgeOutput(c.fx, b.N, r.log.snapshot())
	if !v.clean() {
		b.Fatalf("variant %s failed the output oracle: %s", r.variant.name, v)
	}
	reportOutputSplit(b, r, v, timer, elapsed)
}

// reportOutputSplit reports a run's throughput and where its time went.
func reportOutputSplit(b *testing.B, r *outputRig, v outputVerdict, timer *statementTimer, elapsed time.Duration) {
	events := float64(b.N)
	dets := float64(v.published)
	_, ackSum := histogramStats(b, r.metrics.derivedPublishAck)
	_, statSum := histogramStats(b, r.metrics.ruleStatWrite)
	_, serSum := histogramStats(b, r.metrics.checkpointSerialize)
	_, saveSum := histogramStats(b, r.metrics.checkpointSave)
	ns := func(seconds float64) float64 { return seconds * 1e9 }

	b.ReportMetric(events/elapsed.Seconds(), "events/s")
	b.ReportMetric(dets/elapsed.Seconds(), "dets/s")
	b.ReportMetric(dets/events, "dets/event")
	b.ReportMetric(float64(r.handleTime.Nanoseconds())/events, "handle-ns/event")
	b.ReportMetric(float64(r.checkpointTime.Nanoseconds())/events, "ckpt-ns/event")
	b.ReportMetric(ns(serSum)/events, "serialize-ns/event")
	b.ReportMetric(ns(saveSum)/events, "save-ns/event")
	b.ReportMetric((float64(r.checkpointTime.Nanoseconds())-ns(ackSum+statSum+serSum+saveSum))/events, "ckptother-ns/event")
	if r.acc != nil {
		// Variant B: the rule-health writes ran on the accumulator's worker, so statSum is NOT
		// loop time and is not in ckptother's subtraction; the final flush is inside the timer.
		b.ReportMetric((float64(r.checkpointTime.Nanoseconds())-ns(ackSum+serSum+saveSum))/events, "ckptother-ns/event")
		b.ReportMetric(float64(r.settleTime.Nanoseconds())/events, "settle-ns/event")
		// The loop's own rate, without the final flush: what detection sustained while the
		// counters were being written (or, against a failing store, discarded) behind it.
		if loop := elapsed - r.settleTime; loop > 0 {
			b.ReportMetric(events/loop.Seconds(), "loop-events/s")
		}
		if dets > 0 {
			b.ReportMetric(ns(ackSum)/dets, "ack-ns/det")
			b.ReportMetric(ns(statSum)/dets, "offloop-stat-ns/det")
			if timer != nil {
				b.ReportMetric(float64(timer.fenceN.Load())/dets, "fence-reads/det")
				b.ReportMetric(float64(timer.otherN.Load())/dets, "stat-stmts/det")
			}
		}
		return
	}
	if dets > 0 {
		b.ReportMetric(ns(ackSum)/dets, "ack-ns/det")
		b.ReportMetric(ns(statSum)/dets, "stat-ns/det")
		if timer != nil {
			if n := timer.fenceN.Load(); n != int64(v.published) {
				b.Fatalf("the statement timer saw %d fence reads for %d fires; the split would be misattributed", n, v.published)
			}
			// The fence read runs INSIDE the upsert's create callbacks, and gorm starts a
			// statement's clock before its callbacks, so the upsert's own time includes the
			// fence read nested in it. The upsert alone is the difference.
			b.ReportMetric(float64(timer.fenceNs.Load())/dets, "fence-ns/det")
			b.ReportMetric(float64(timer.otherNs.Load()-timer.fenceNs.Load())/dets, "upsert-ns/det")
		}
	}
}

// The benchmark's own instruments are checked before anything is timed with them: on the
// sqlite store with the production callbacks, every fire is exactly ONE fence read and ONE other
// statement, which is what lets the timer attribute them.
func TestTheStatementTimerSplitsOneFenceReadAndOneUpsertPerFire(t *testing.T) {
	timer := &statementTimer{}
	fx := benchFixture(2, 1)
	store := newSqliteRuleStatStore(t, timer)
	timer.fenceN.Store(0)
	timer.otherN.Store(0)
	r := newOutputRig(t, outputRigSpec{fx: fx, variant: variantA, stats: store, checkpointEvents: 50})
	r.run(t, fx.messages(t, 200, r.log))
	v := judgeOutput(fx, 200, r.log.snapshot())
	if !v.clean() || v.published == 0 {
		t.Fatalf("fixture run: %s", v)
	}
	if f, o := timer.fenceN.Load(), timer.otherN.Load(); f != int64(v.published) || o != int64(v.published) {
		t.Fatalf("%d fires made %d fence reads and %d other statements; want %d of each", v.published, f, o, v.published)
	}
	if timer.fenceNs.Load() <= 0 || timer.otherNs.Load() <= 0 {
		t.Fatal(fmt.Sprintf("the timer recorded no time: fence %dns other %dns", timer.fenceNs.Load(), timer.otherNs.Load()))
	}
}
