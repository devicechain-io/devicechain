// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
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

// upsertSeq is the message before which the late rule is upserted into the running processor.
const upsertSeq = 120

func compileShardRule(t *testing.T, r rules0.Rule) *rules0.CompiledRule {
	t.Helper()
	cr, err := rules0.Compile(r, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile %s: %v", r.ID, err)
	}
	return cr
}

// lateRule is the rule the stream upserts partway through.
func lateRule(t *testing.T) runtime.ScopedRule {
	thr := 90.0
	return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: compileShardRule(t, rules0.Rule{
		ID: "acme/late", Name: "late", Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	})}
}

// shardedRegistry holds every rule kind whose routing differs: a group-scoped Duration hold (timers
// and descopes), a threshold (events), a correlation under an anchor (members of one anchor on
// different devices) and an absence armed through the dead-man path. late adds the rule the stream
// upserts at upsertSeq, for a processor that starts after it.
func shardedRegistry(t *testing.T, late bool) *runtime.RuleRegistry {
	t.Helper()
	thr, hot := 80.0, 95.0
	sr := []runtime.ScopedRule{
		{Tenant: "acme", ProfileVersionToken: "p@1", GroupToken: "arid-areas", GroupVersion: 1, Compiled: compileShardRule(t, rules0.Rule{
			ID: "acme/hold", Name: "sustained-heat", Type: rules0.TypeDuration, Hold: rules0.Duration(10 * time.Second),
			When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
		})},
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: compileShardRule(t, rules0.Rule{
			ID: "acme/spike", Name: "spike", Type: rules0.TypeThreshold,
			When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &hot},
		})},
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: compileShardRule(t, rules0.Rule{
			ID: "acme/corr", Name: "many in area", Type: rules0.TypeCorrelation, AnchorType: "area",
			Count: 3, Window: rules0.Duration(5 * time.Minute),
		})},
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: compileShardRule(t, rules0.Rule{
			ID: "acme/silent", Name: "silent", Type: rules0.TypeAbsence, Ttl: rules0.Duration(40 * time.Second),
		})},
	}
	if late {
		sr = append(sr, lateRule(t))
	}
	return runtime.NewRuleRegistry(sr)
}

// shardedRoster is every device the absence rule watches: the 24 that report, and five that never do.
func shardedRoster() ([]runtime.RosterEntry, []runtime.ActiveEntry) {
	var ros []runtime.RosterEntry
	for i := 0; i < 24; i++ {
		ros = append(ros, runtime.RosterEntry{Tenant: "acme", DeviceToken: fmt.Sprintf("dev-%02d", i), ProfileToken: "p", ExpectedSince: testBase})
	}
	for i := 0; i < 5; i++ {
		ros = append(ros, runtime.RosterEntry{Tenant: "acme", DeviceToken: fmt.Sprintf("silent-%d", i), ProfileToken: "p", ExpectedSince: testBase})
	}
	return ros, []runtime.ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@1", PublishedAt: testBase}}
}

// shardedProcessor is a processor over a shared snapshot store, configured for k shards, that
// publishes into w, with the dead-man armer reconciled as a term build does. A fresh one stands for
// a restart.
func shardedProcessor(t *testing.T, ctx context.Context, store *model.SnapshotStore, w *valueCapture, k int, late bool) *ResolvedEventsProcessor {
	t.Helper()
	reg := shardedRegistry(t, late)
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
	rp.armer = runtime.NewDeadmanArmer(reg, rp.engine)
	rp.armer.Reconcile(shardedRoster())
	return rp
}

// shardedMessage is message seq of the one input every run shares: 24 devices (the last four stop
// reporting after message 100), each under one of three areas, a reading per message, the value
// stepping through a pattern that opens holds, ends some early, and spikes. Every eleventh message
// is from outside the hold rule's group, which descopes that device.
func shardedMessage(t *testing.T, seq uint64) messaging.Message {
	t.Helper()
	n := seq % 24
	if n >= 20 && seq > 100 {
		n = seq % 20
	}
	dev := fmt.Sprintf("dev-%02d", n)
	val := "70"
	switch {
	case seq%7 == 0:
		val = "99"
	case seq%5 != 0:
		val = "85"
	}
	var refs []dmmodel.GroupRef
	if seq%11 != 0 {
		refs = []dmmodel.GroupRef{{GroupToken: "arid-areas", Version: 1}}
	}
	occurred := testBase.Add(time.Duration(seq) * time.Second)
	ev := &dmmodel.ResolvedEvent{
		Source: "http1", SourceDeviceToken: dev, ProfileVersionToken: "p@1",
		OccurredTime: occurred, ProcessedTime: occurred, EventType: esmodel.Measurement,
		ScopeMemberships: refs,
		Anchors:          []dmmodel.ResolvedAnchor{{AnchorType: "area", AnchorToken: fmt.Sprintf("zone-%d", n%3)}},
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: occurred,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: val}},
		}}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, &fakeAck{})
	m.StreamSeq = seq
	return m
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
		rp := shardedProcessor(t, ctx, store, w, k, next > upsertSeq)
		if k > 1 && shardCount(rp) != k {
			t.Fatalf("segment %d: configured %d shards but the processor is running %d", i, k, shardCount(rp))
		}
		for ; next <= cuts[i]; next++ {
			if next == upsertSeq {
				rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{lateRule(t)}})
			}
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
	const total = 300
	control, controlSnap := runSegments(t, []int{1}, []uint64{total})
	// The comparison means nothing for a rule kind the stream never fires, so name each one.
	all := strings.Join(control, "\n")
	for _, want := range []string{"acme/hold", "acme/spike", "acme/corr", "acme/silent", "silent-0", "acme/late"} {
		if !strings.Contains(all, want) {
			t.Fatalf("the control run never raised %q, so a sharded run matching it proves nothing about that rule", want)
		}
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
		rp := shardedProcessor(t, ctx, store, &valueCapture{}, k, false)
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

// The reset a term build makes when the stream head is behind the snapshot builds an empty engine,
// and it must build it at the configured shard count: an engine reset to a single shard would
// quietly run unsplit for the rest of the term.
func TestReplayResetKeepsTheConfiguredShardCount(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	p1 := shardedProcessor(t, ctx, store, &valueCapture{}, 4, false)
	for i := uint64(1); i <= 5; i++ {
		p1.handle(shardedMessage(t, i))
	}
	p1.checkpoint(ctx)

	// Restart against a stream whose head (2) is behind the snapshot (5).
	p2 := shardedProcessor(t, ctx, store, &valueCapture{}, 4, false)
	p2.Replay = &fakeReplayOpener{head: 2}
	p2.cfg.Suffix = "resolved-events"
	if got := shardCount(p2); got != 4 {
		t.Fatalf("before the reset: %d shards, want 4", got)
	}
	if err := p2.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if p2.engine.LastSeq() != 0 {
		t.Fatalf("engine not reset: lastSeq = %d, want 0", p2.engine.LastSeq())
	}
	if got := shardCount(p2); got != 4 {
		t.Fatalf("after the reset: %d shards, want 4", got)
	}
}
