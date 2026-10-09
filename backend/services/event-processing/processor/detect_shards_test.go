// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// valueCapture records the bytes of every derived event published, so two runs can be compared on
// WHAT they raised and not only how many.
type valueCapture struct {
	mu   sync.Mutex
	vals []string
}

func (w *valueCapture) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range msgs {
		w.vals = append(w.vals, string(m.Key)+"|"+string(m.Value))
	}
	return nil
}
func (w *valueCapture) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *valueCapture) HandleResponse(error) {}

func (w *valueCapture) sorted() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]string(nil), w.vals...)
	sort.Strings(out)
	return out
}

// shardedRegistry holds a Duration rule (temperature > 80 held 10s) and a threshold rule, both for
// tenant acme / profile p@1, so raises come from timers (the watermark) and from events (the keys).
func shardedRegistry(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr := 80.0
	hold, err := rules0.Compile(rules0.Rule{
		ID: "acme/hold", Name: "sustained-heat", Type: rules0.TypeDuration, Hold: rules0.Duration(10 * time.Second),
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile hold: %v", err)
	}
	hot := 95.0
	spike, err := rules0.Compile(rules0.Rule{
		ID: "acme/spike", Name: "spike", Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &hot},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile spike: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: hold},
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: spike},
	})
}

// shardedProcessor is a processor over a shared snapshot store, configured for k shards, that
// publishes into w. A fresh one stands for a restart.
func shardedProcessor(t *testing.T, ctx context.Context, store *model.SnapshotStore, w *valueCapture, k int) *ResolvedEventsProcessor {
	t.Helper()
	reg := shardedRegistry(t)
	rp := &ResolvedEventsProcessor{
		Store:  store,
		Replay: &fakeReplayOpener{},
		cfg: Config{
			PartitionId: "singleton", CheckpointEvents: 1 << 20, CheckpointInterval: time.Hour,
			TickInterval: time.Hour, Clock: detectcore.RealClock{}, Shards: k,
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		procCtx:   ctx,
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore (k=%d): %v", k, err)
	}
	return rp
}

// shardedMessage is message seq of the one input every run shares: 24 devices, a reading per
// message, the value stepping through a pattern that opens holds, ends some early, and spikes.
func shardedMessage(t *testing.T, seq uint64) messaging.Message {
	t.Helper()
	dev := fmt.Sprintf("dev-%02d", seq%24)
	val := "70"
	switch {
	case seq%7 == 0:
		val = "99"
	case seq%5 != 0:
		val = "85"
	}
	return measuredMsgScoped(t, seq, "acme", dev, "p@1", "temperature", val, &fakeAck{}, nil)
}

// shardCount is K as the processor is actually running it.
func shardCount(rp *ResolvedEventsProcessor) int {
	if s, ok := rp.engine.(*detectcore.Sharded); ok {
		return s.Shards()
	}
	return 1
}

// runSegments feeds seqs 1..total through one processor per segment: segment i runs with
// ks[i] shards, over seqs up to cuts[i], checkpoints, and the next segment restarts from the
// committed snapshot. It returns every derived event published and the final snapshot bytes.
func runSegments(t *testing.T, ks []int, cuts []uint64) ([]string, []byte) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	w := &valueCapture{}
	next := uint64(1)
	for i, k := range ks {
		rp := shardedProcessor(t, ctx, store, w, k)
		if k > 1 && shardCount(rp) != k {
			t.Fatalf("segment %d: configured %d shards but the processor is running %d", i, k, shardCount(rp))
		}
		for ; next <= cuts[i]; next++ {
			rp.handle(shardedMessage(t, next))
		}
		rp.checkpoint(ctx)
		if i == len(ks)-1 {
			snap, ok, err := store.Load(ctx, "singleton")
			if err != nil || !ok {
				t.Fatalf("load final snapshot: ok=%v err=%v", ok, err)
			}
			return w.sorted(), snap.Payload
		}
	}
	return nil, nil
}

// Changing K across a restart must just work: the snapshot is the same bytes for every K, the
// restart re-splits it, and the run raises exactly what an unbroken K=1 run over the same stream
// raises, ending in the same snapshot, byte for byte.
func TestChangingShardCountAcrossRestartsMatchesSingleEngine(t *testing.T) {
	const total = 240
	control, controlSnap := runSegments(t, []int{1}, []uint64{total})
	if len(control) < 20 {
		t.Fatalf("the stream raised only %d derived events: too little for a comparison to mean anything", len(control))
	}
	cases := []struct {
		name string
		ks   []int
		cuts []uint64
	}{
		{"1 to 4", []int{1, 4}, []uint64{101, total}},
		{"4 to 1", []int{4, 1}, []uint64{101, total}},
		{"4 to 7 to 2 to 1", []int{4, 7, 2, 1}, []uint64{57, 120, 181, total}},
		{"4 throughout", []int{4}, []uint64{total}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, snap := runSegments(t, c.ks, c.cuts)
			if len(got) != len(control) {
				t.Fatalf("raised %d derived events, a single engine raised %d", len(got), len(control))
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

// The shard count in force is observable: the gauge is set from the configured count when the
// engine is built, and again when one is restored.
func TestShardCountGauge(t *testing.T) {
	ctx := context.Background()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	metrics := NewDetectMetrics(ms)
	store := newTestStore(t)
	for _, k := range []int{0, 1, 4, 4, 2} {
		rp := shardedProcessor(t, ctx, store, &valueCapture{}, k)
		rp.metrics = metrics
		if err := rp.restore(ctx); err != nil {
			t.Fatalf("restore: %v", err)
		}
		rp.handle(shardedMessage(t, 1))
		rp.checkpoint(ctx)
		want := float64(max(k, 1))
		if got := testutil.ToFloat64(metrics.shards); got != want {
			t.Fatalf("detect_shards = %v with detectShards %d, want %v", got, k, want)
		}
	}
}
