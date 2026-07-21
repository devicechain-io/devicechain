// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// fakeSim drives the loop without touching the network: each Tick just bumps the
// accepted counter, so drive's ledger/window/stop-boundary behavior is testable
// without a cluster.
type fakeSim struct {
	perTick int64
	ticks   int32
}

func (f *fakeSim) Manifest() sim.SimManifest                     { return sim.SimManifest{} }
func (f *fakeSim) Bootstrap(context.Context, *sim.Runtime) error { return nil }
func (f *fakeSim) Tick(_ context.Context, rt *sim.Runtime) error {
	atomic.AddInt32(&f.ticks, 1)
	rt.Stats.Emitted.Add(f.perTick)
	return nil
}

// The load-bearing property of drive: the accepted ledger is exact at the stop
// boundary. drive stops BETWEEN whole ticks and spawns no background emitters, so
// nothing lands after it returns — the reason it does not reuse
// sim.Lifecycle.Start/Stop (which cancels in-flight POSTs and would miscount).
func TestDriveKeepsLedgerExactAndFreezes(t *testing.T) {
	rt := &sim.Runtime{Load: sim.Load{EmitInterval: 5 * time.Millisecond}}
	fs := &fakeSim{perTick: 10}

	start, end, err := drive(context.Background(), rt, fs, 40*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !end.After(start) {
		t.Fatalf("end %s not after start %s", end, start)
	}
	emitted := rt.Stats.Emitted.Load()
	if emitted == 0 {
		t.Fatal("drive applied no load")
	}
	// Nothing emits after drive returns.
	time.Sleep(20 * time.Millisecond)
	if got := rt.Stats.Emitted.Load(); got != emitted {
		t.Fatalf("ledger moved after drive returned: %d -> %d", emitted, got)
	}
	// Freeze was called on the normal exit: the snapshot's elapsed is a fixed
	// window, not still growing against wall-clock.
	s1 := rt.Stats.Snapshot(time.Now())
	time.Sleep(10 * time.Millisecond)
	s2 := rt.Stats.Snapshot(time.Now())
	if s1.Seconds != s2.Seconds {
		t.Fatalf("elapsed not frozen after drive: %v then %v", s1.Seconds, s2.Seconds)
	}
}

func TestDriveAbortReturnsErrorNotVerdict(t *testing.T) {
	// A cancelled context aborts the drive with an error, never a (start, end) a
	// caller could mistake for a clean stop boundary to reconcile against.
	rt := &sim.Runtime{Load: sim.Load{EmitInterval: 5 * time.Millisecond}}
	fs := &fakeSim{perTick: 10}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := drive(ctx, rt, fs, time.Second); err == nil {
		t.Fatal("expected an error on a cancelled drive")
	}
}
