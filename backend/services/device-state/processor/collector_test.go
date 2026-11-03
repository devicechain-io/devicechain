// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Serialized collection (messaging.Collector): a pod's writers take turns receiving a batch,
// so a trickle fills ONE batch instead of being split among every writer that is lingering.

// sizingApi records the size of every batch merged and takes 5 ms to commit it.
type sizingApi struct {
	model.DeviceStateApi
	mu    sync.Mutex
	sizes []int
}

func (a *sizingApi) MergeProjectionBatch(_ context.Context, updates []model.ProjectionUpdate) error {
	time.Sleep(5 * time.Millisecond) // the commit
	a.mu.Lock()
	a.sizes = append(a.sizes, len(updates))
	a.mu.Unlock()
	return nil
}

func (a *sizingApi) mean() (mean float64, batches int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0
	for _, n := range a.sizes {
		total += n
	}
	return float64(total) / float64(len(a.sizes)), len(a.sizes)
}

func histogramCount(t *testing.T, reg *prometheus.Registry, suffix string) uint64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var n uint64
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), suffix) {
			for _, m := range f.GetMetric() {
				n += m.GetHistogram().GetSampleCount()
			}
		}
	}
	return n
}

// The projection's writers share ONE collector, and at a trickle that makes the batches
// big: ten writers, the default 10 ms linger and an event a millisecond come out as batches
// of about ten, where ten writers lingering on their own would see about two.
func TestTheProjectionWritersShareOneCollectorAtATrickle(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(reg)
	api := &sizingApi{}
	// No linger configured: the default applies.
	sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), api, NewStateMetrics(ms),
		ms.NewPeriodicTaskMetrics("collector"), WithProjection(config.ProjectionConfiguration{Writers: 10, MaxBatch: 32}))
	if sp.Linger() != 10*time.Millisecond {
		t.Fatalf("linger = %v; want the 10 ms default", sp.Linger())
	}
	if err := sp.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if sp.collector == nil {
		t.Fatal("the writers have no collector")
	}

	const total = 300
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	start := time.Now()
	for i := 0; i < total; i++ {
		if d := time.Until(start.Add(time.Duration(i) * time.Millisecond)); d > 0 {
			time.Sleep(d)
		}
		if !sp.handOff(context.Background(), threeMetrics(t, "trickle-"+string(rune('a'+i%26)), at.Add(time.Duration(i)*time.Second))) {
			t.Fatalf("hand-off %d refused", i)
		}
	}
	if err := sp.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	mean, batches := api.mean()
	if mean < 8 {
		t.Errorf("mean batch = %.1f over %d batches; want at least 8 at a trickle", mean, batches)
	}
	if n := histogramCount(t, reg, "state_handoff_depth"); n == 0 {
		t.Error("state_handoff_depth recorded nothing")
	}
}

// An event admission refuses (here, one with no parseable tenant) was still received, so it
// took its place in the batch: the batch the writer merges is smaller than its raw count.
// Its RED clock still ran from receipt and was closed by the refusal.
func TestARefusedEventIsCountedRawAndLeavesTheBatchSmaller(t *testing.T) {
	sp := &StateProcessor{projection: config.ProjectionConfiguration{Writers: 1, MaxBatch: 4}}
	sp.collector = &messaging.Collector{}
	sp.messages = make(chan messaging.Message, 8)
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	sp.messages <- threeMetrics(t, "ok-1", at)
	sp.messages <- messaging.Message{Subject: "no-tenant-here", Value: []byte("x")}
	sp.messages <- threeMetrics(t, "ok-2", at)
	sp.messages <- threeMetrics(t, "ok-3", at)
	sp.messages <- threeMetrics(t, "ok-4", at)
	w := sp.newWriterBuffers()
	batch, open := w.collect(context.Background())
	if !open || len(w.raw) != 4 || len(batch) != 3 {
		t.Fatalf("collected %d raw, admitted %d, open=%v; want 4 raw, 3 admitted, open", len(w.raw), len(batch), open)
	}
	if len(sp.messages) != 1 {
		t.Errorf("%d events left waiting; want 1 (the cap counts raw events)", len(sp.messages))
	}
}

// Once a batch is merged the writer holds no reference to it: every slot of its buffers is
// back to the zero value, not just the part the next batch will overwrite.
func TestAWriterDropsItsBatchReferencesOnceMerged(t *testing.T) {
	sp := &StateProcessor{projection: config.ProjectionConfiguration{Writers: 1, MaxBatch: 8}}
	sp.collector = &messaging.Collector{}
	sp.messages = make(chan messaging.Message, 8)
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		sp.messages <- threeMetrics(t, "drop-"+string(rune('a'+i)), at)
	}
	w := sp.newWriterBuffers()
	if batch, _ := w.collect(context.Background()); len(batch) != 8 {
		t.Fatalf("admitted %d; want 8", len(batch))
	}
	w.clear()
	raw, dones, batch := w.raw[:cap(w.raw)], w.dones[:cap(w.dones)], w.batch[:cap(w.batch)]
	if len(raw) != 8 || len(dones) != 8 || len(batch) != 8 {
		t.Fatalf("buffers hold %d/%d/%d slots; want MaxBatch (8) each, allocated once", len(raw), len(dones), len(batch))
	}
	for i := range raw {
		if raw[i].Value != nil || raw[i].Subject != "" {
			t.Errorf("raw[%d] still holds a message", i)
		}
		if dones[i] != nil {
			t.Errorf("dones[%d] still holds a clock-stop", i)
		}
		if batch[i].event != nil || batch[i].done != nil || batch[i].msg.Value != nil {
			t.Errorf("batch[%d] still holds an admitted event", i)
		}
	}
}

// The RED clock of each event starts when it is RECEIVED: an event that waits out the
// linger is in flight for it. Here the writer lingers 400 ms for a second event that never
// comes, and the first is in flight (state_inflight) the whole time.
func TestAnEventIsInFlightFromReceiptNotFromAdmission(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(reg)
	api := &sizingApi{}
	linger := 400
	sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), api, NewStateMetrics(ms),
		ms.NewPeriodicTaskMetrics("inflight"), WithProjection(config.ProjectionConfiguration{Writers: 1, MaxBatch: 8, LingerMillis: &linger}))
	if err := sp.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	sp.handOff(context.Background(), threeMetrics(t, "inflight-1", at))
	var seen atomic.Bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !seen.Load() {
		families, _ := reg.Gather()
		for _, f := range families {
			if strings.HasSuffix(f.GetName(), "state_inflight") && len(f.GetMetric()) == 1 &&
				f.GetMetric()[0].GetGauge().GetValue() == 1 {
				seen.Store(true)
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !seen.Load() {
		t.Error("state_inflight never read 1 while the writer lingered for the event's batch")
	}
	if err := sp.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// state_collect_full_total counts the batches the collector filled to the cap without
// waiting, and state_handoff_depth records how many events were still waiting when each
// batch was received. Ten events already waiting and a cap of 4: two full batches, then a
// partial one of two; the depth reads 6 and 2 (the third receive is cut short by the close
// and reads 0).
func TestCollectionMetricsReportFullBatchesAndTheHandOffDepth(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(reg)
	sp := &StateProcessor{projection: config.ProjectionConfiguration{Writers: 1, MaxBatch: 4}, metrics: NewStateMetrics(ms)}
	sp.collector = &messaging.Collector{}
	sp.messages = make(chan messaging.Message, 16)
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		sp.messages <- threeMetrics(t, "metrics-"+string(rune('a'+i)), at)
	}
	close(sp.messages)
	w := sp.newWriterBuffers()
	for open := true; open; {
		_, open = w.collect(context.Background())
		w.clear()
	}
	if got := testutil.ToFloat64(sp.metrics.collectFull); got != 2 {
		t.Errorf("state_collect_full_total = %v; want 2 (the two batches filled to the cap)", got)
	}
	if n := histogramCount(t, reg, "state_handoff_depth"); n != 3 {
		t.Errorf("state_handoff_depth observed %d batches; want 3", n)
	}
	families, _ := reg.Gather()
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), "state_handoff_depth") {
			if sum := f.GetMetric()[0].GetHistogram().GetSampleSum(); sum != 8 {
				t.Errorf("state_handoff_depth observed a total depth of %v; want 6 + 2 + 0 = 8", sum)
			}
		}
	}
}
