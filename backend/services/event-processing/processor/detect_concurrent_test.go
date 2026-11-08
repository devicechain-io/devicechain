// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"fmt"
	runtimepkg "runtime"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// runWithCheckpoints feeds messages 1..total through one processor at k shards, checkpointing every
// `every` messages and running the tick's early flush every `tickEvery` (collecting what the shards
// have made, publishing what is ready ahead of the checkpoint). It returns every derived event
// published, with its bytes, and the final snapshot.
//
// The checkpoint positions are the same for every k, because a publish-time drop of a superseded
// detection (and the latch clear it makes) depends on where the checkpoints fall, today as well.
func runWithCheckpoints(t *testing.T, k, total, every, tickEvery int) ([]string, []byte) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	w := &valueCapture{}
	rp := shardedProcessor(t, ctx, store, w, k, false)
	defer rp.closeDetector()
	for next := uint64(1); next <= uint64(total); next++ {
		if next == upsertSeq {
			rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{lateRule(t)}})
		}
		rp.handle(shardedMessage(t, next))
		if tickEvery > 0 && int(next)%tickEvery == 0 {
			rp.collectDetections()
			rp.flushDetections(ctx)
		}
		if int(next)%every == 0 {
			rp.checkpoint(ctx)
		}
	}
	rp.checkpoint(ctx)
	snap, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load final snapshot: ok=%v err=%v", ok, err)
	}
	return w.laneOrdered(), snap.Payload
}

// The concurrent engine, checkpointed at awkward positions with early flushes in between, raises
// exactly what the single engine raises, byte for byte, and ends in the same snapshot. This is the
// processor-level identity: the stamps, the dedup ids and the checkpoint barrier all sit between the
// shards and these bytes.
func TestConcurrentShardsMatchSingleEngineAtAnyCheckpointCadence(t *testing.T) {
	const total = 300
	for _, c := range []struct{ every, tickEvery int }{{1000, 0}, {7, 0}, {41, 13}, {1, 1}, {97, 5}} {
		control, controlSnap := runWithCheckpoints(t, 1, total, c.every, c.tickEvery)
		all := strings.Join(control, "\n")
		for _, want := range []string{"acme/hold", "acme/spike", "acme/corr", "acme/silent", "acme/late"} {
			if !strings.Contains(all, want) {
				t.Fatalf("every=%d: the control run never raised %q, so a match proves nothing about that rule", c.every, want)
			}
		}
		for _, k := range []int{2, 4, 7} {
			c, k := c, k
			t.Run(fmt.Sprintf("K=%d/checkpoint=%d/tick=%d", k, c.every, c.tickEvery), func(t *testing.T) {
				got, snap := runWithCheckpoints(t, k, total, c.every, c.tickEvery)
				if len(got) != len(control) {
					t.Fatalf("raised %d derived events, the single engine raised %d", len(got), len(control))
				}
				for i := range got {
					if got[i] != control[i] {
						t.Fatalf("derived event %d differs from the single engine's:\n got  %s\n want %s", i, got[i], control[i])
					}
				}
				if !bytes.Equal(snap, controlSnap) {
					t.Fatalf("final snapshot differs from the single engine's (%d vs %d bytes)", len(snap), len(controlSnap))
				}
			})
		}
	}
}

// Every term builds an engine, and every engine's shard goroutines end with it: a rebuilt term, a
// stop, and the reset a replay makes must not leave shards of an earlier engine running.
func TestConcurrentShardsDoNotOutliveTheirEngine(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := shardedProcessor(t, ctx, store, &valueCapture{}, 1, false)
	rp.cfg.Shards = 4
	if err := rp.restore(ctx); err != nil {
		t.Fatal(err)
	}
	rp.handle(shardedMessage(t, 1))
	rp.checkpoint(ctx)
	if got := settledShardGoroutines(4); got != 4 {
		t.Fatalf("expected one engine's four shard goroutines, found %d", got)
	}

	for i := 0; i < 6; i++ {
		if err := rp.restore(ctx); err != nil { // a new term: the previous engine must be closed
			t.Fatal(err)
		}
		rp.handle(shardedMessage(t, uint64(2+i)))
		rp.checkpoint(ctx)
	}
	if got := settledShardGoroutines(4); got != 4 {
		t.Fatalf("%d shard goroutines after 6 term rebuilds, want the current engine's 4: shards of replaced engines are still running", got)
	}
	rp.closeDetector()
	if got := settledShardGoroutines(0); got != 0 {
		t.Fatalf("%d shard goroutines after the term ended", got)
	}
	// The engine still answers after its term ended, synchronously.
	if rp.engine.LastSeq() == 0 {
		t.Fatal("the closed engine lost its position")
	}
}

// The shard series are exported: queue depth and busy time per shard, the barrier wait, and a
// violation count that stays zero.
func TestConcurrentShardsExportTheirMetrics(t *testing.T) {
	ctx := context.Background()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	metrics := NewDetectMetrics(ms)
	store := newTestStore(t)
	rp := shardedProcessor(t, ctx, store, &valueCapture{}, 1, false)
	rp.cfg.Shards = 3
	rp.metrics = metrics
	if err := rp.restore(ctx); err != nil {
		t.Fatal(err)
	}
	defer rp.closeDetector()
	for i := uint64(1); i <= 120; i++ {
		rp.handle(shardedMessage(t, i))
	}
	rp.checkpoint(ctx)
	rp.collectDetections()
	rp.recordStateBudget()

	if n := testutil.CollectAndCount(metrics.shard.busy); n != 3 {
		t.Fatalf("detect_shard_busy_seconds_total has %d series, want 3", n)
	}
	for i := 0; i < 3; i++ { // the clock may be too coarse to see 120 messages' worth of busy time
		_ = testutil.ToFloat64(metrics.shard.busy.WithLabelValues(fmt.Sprint(i)))
		_ = testutil.ToFloat64(metrics.shard.queueDepth.WithLabelValues(fmt.Sprint(i)))
	}
	keys := 0.0
	for i := 0; i < 3; i++ {
		keys += testutil.ToFloat64(metrics.shard.liveKeys.WithLabelValues(fmt.Sprint(i)))
	}
	if keys <= 0 {
		t.Fatal("no shard reported any live keys")
	}
	if got := testutil.ToFloat64(metrics.shard.violations); got != 0 {
		t.Fatalf("invariant violations = %v on a healthy run", got)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), "detect_barrier_wait_seconds") && f.GetMetric()[0].GetHistogram().GetSampleCount() == 0 {
			t.Fatal("no barrier wait was observed for a checkpoint")
		}
	}
}

// drainMessage is shardedMessage over a larger fleet: device n of devices, a reading per message.
func drainMessage(b *testing.B, seq uint64, devices int) messaging.Message {
	b.Helper()
	n := int(seq) % devices
	val := "70"
	switch {
	case seq%7 == 0:
		val = "99"
	case seq%5 != 0:
		val = "85"
	}
	occurred := testBase.Add(time.Duration(seq) * time.Millisecond * 5)
	ev := &resolvedevent.ResolvedEvent{
		Source: "http1", SourceDeviceToken: fmt.Sprintf("dev-%05d", n), ProfileVersionToken: "p@1",
		OccurredTime: occurred, ProcessedTime: occurred, EventType: eventtype.Measurement,
		ScopeMemberships: []resolvedevent.GroupRef{{GroupToken: "arid-areas", Version: 1}},
		Anchors:          []resolvedevent.ResolvedAnchor{{AnchorType: "area", AnchorToken: fmt.Sprintf("zone-%d", n%97)}},
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: occurred,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: val}},
		}}},
	}
	raw, err := resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		b.Fatal(err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", raw, 0, nil, &fakeAck{})
	m.StreamSeq = seq
	return m
}

// BenchmarkReplayDrain is how fast the detection loop works through a recorded stream: decode, rule
// fan-out, the engine, and a count-based checkpoint every 1000 messages (publish into a no-op
// writer, snapshot into the test store), at each shard count. It is the loop without the broker, so
// the rate is what a replay to the head, or a backlog after an outage, drains at when the
// broker is not the limit. messages/s is the figure to read.
//
//	go test -run '^$' -bench BenchmarkReplayDrain -benchtime 1x ./processor
func BenchmarkReplayDrain(b *testing.B) {
	const devices, messages = 2000, 40000
	msgs := make([]messaging.Message, messages)
	for i := range msgs {
		msgs[i] = drainMessage(b, uint64(i+1), devices)
	}
	for _, c := range []struct {
		name  string
		every int
	}{{"checkpoint=1000", 1000}, {"checkpoint=never", 0}} {
		for _, k := range []int{1, 2, 4, 8} {
			c, k := c, k
			b.Run(fmt.Sprintf("%s/K=%d", c.name, k), func(b *testing.B) {
				benchReplayDrain(b, msgs, k, c.every)
			})
		}
	}
}

func benchReplayDrain(b *testing.B, msgs []messaging.Message, k, every int) {
	ctx := context.Background()
	for iter := 0; iter < b.N; iter++ {
		b.StopTimer()
		store := newTestStore(b)
		rp := shardedProcessor(b, ctx, store, &valueCapture{}, k, false)
		b.StartTimer()
		start := time.Now()
		for i := range msgs {
			rp.handle(msgs[i])
			if every > 0 && (i+1)%every == 0 {
				rp.checkpoint(ctx)
			}
		}
		rp.checkpoint(ctx)
		el := time.Since(start)
		b.ReportMetric(float64(len(msgs))/el.Seconds(), "messages/s")
		b.StopTimer()
		rp.closeDetector()
	}
}

// settledShardGoroutines is shardGoroutines once it has reached want, or what it still reads after
// ten seconds. A shard's WaitGroup is released by a defer, so Close returns while the goroutine is
// still unwinding and still in a stack dump: a count taken the instant a Close returns (this test's,
// or the previous test's cleanup) can be one engine's worth too high. Waiting for the count to settle
// is observing an exit, not papering over a leak: a goroutine that never exits fails after the deadline.
func settledShardGoroutines(want int) int {
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := shardGoroutines()
		if n == want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// shardGoroutines counts the running detection shard goroutines in the process.
func shardGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtimepkg.Stack(buf, true)]
	return strings.Count(string(buf), "(*shardWorker).run")
}
