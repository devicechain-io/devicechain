// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// TestIdleAdvanceSettlesAHeldRateSample: a DeltaRate verdict waits for the frontier to pass its
// sample, so for a tenant that reports a spike and then goes quiet, NO later event will ever move
// the frontier. The wall-clock idle advance is the only thing that can settle it — so the held
// sample must count as pending work, and the advance must publish the raise. Without that the
// rate alarm would hang until the device happened to report again. Once the run is drained the
// engine is quiet again: a further idle tick must not checkpoint a bare watermark move.
func TestIdleAdvanceSettlesAHeldRateSample(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	w := &captureWriter{}
	cr, err := rules0.Compile(rules0.Rule{ID: "acme/spike", Name: "spike", Type: rules0.TypeDeltaRate,
		Metric: "temperature", Op: rules0.OpGt, Threshold: ptrF(50)}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile delta rate: %v", err)
	}
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
	pctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rp := &ResolvedEventsProcessor{
		Store: newTestStore(t),
		cfg: Config{PartitionId: "singleton", CheckpointEvents: 100, CheckpointInterval: 10 * time.Second,
			TickInterval: time.Hour, Lateness: 5 * time.Second, IdleAdvanceGuard: 5 * time.Second, Clock: clock},
		registry:     reg,
		publisher:    runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
		clock:        clock,
		procCtx:      pctx,
		procCancel:   cancel,
		backlogProbe: &fakeProber{},
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "0", &fakeAck{}))
	rp.handle(measuredMsg(t, 2, "acme", "d1", "p@1", "temperature", "100", &fakeAck{})) // +100 > 50
	if w.writes != 0 {
		t.Fatalf("both samples are inside the lateness budget, so nothing is final yet; writes=%d", w.writes)
	}
	if !rp.engine.HasPendingWork() {
		t.Fatal("a held sample is work only the frontier can finish: HasPendingWork must report it")
	}

	now := testBase.Add(30 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)
	if w.writes != 1 {
		t.Fatalf("the idle advance must settle the held spike and publish its raise; writes=%d", w.writes)
	}
	if rp.engine.HasPendingWork() {
		t.Fatal("every held sample was settled: the engine must be quiet again")
	}

	later := now.Add(time.Minute)
	openGates(rp, later)
	clock.Set(later)
	rp.idleAdvance(ctx, later)
	snap, ok, err := rp.Store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("the settling advance must have committed; ok=%v err=%v", ok, err)
	}
	if want := now.Add(-5 * time.Second); !snap.Watermark.Equal(want) {
		t.Fatalf("a drained engine must not checkpoint a bare watermark move: committed %v, want %v", snap.Watermark, want)
	}
}

func ptrF(v float64) *float64 { return &v }
