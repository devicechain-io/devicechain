// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"strings"
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

// ---- The harness refuses a scenario that generates nothing ----------------------

// 🔴 THE FAILURE THIS PREVENTS IS NOT A WASTED RUN, IT IS A MISREPORTED ONE. A scenario
// whose devices publish their own telemetry emits nothing from Sim.Tick, so a load run
// against it provisions, holds for its whole window, accepts zero events, and then
// fails on the MinAccepted floor — whose message is about a job that lost its load
// flags. The operator goes looking for a missing --devices, and nothing anywhere names
// the scenario, because by then the only evidence left is a zero.
//
// Two paths reach that with nobody having chosen the scenario: cmd/loadtest-contention
// builds its --manifest offering from the registry, and cmd/loadtest, -monitor and
// -selftest default the id from the HANDSHAKE — so pointing any of them at an existing
// sim is enough.
//
// Asserted through Profile.Validate rather than through Run, because Validate is the
// one gate every harness shares, and asserting it here is what makes the refusal cover
// harnesses nobody has written yet.
func TestProfileRefusesAScenarioWhoseDevicesPublishTheirOwnTelemetry(t *testing.T) {
	// Find one from the registry rather than naming sitepulse: the property is what
	// matters, and a named scenario would make this test a description of today's
	// registry instead of of the rule.
	var selfPublishing string
	for _, id := range sim.ManifestIds() {
		if m, ok := sim.ScenarioManifest(id); ok && m.DevicesPublishTheirOwnTelemetry {
			selfPublishing = id
			break
		}
	}
	if selfPublishing == "" {
		t.Skip("no registered scenario declares DevicesPublishTheirOwnTelemetry")
	}

	err := Profile{Manifest: selfPublishing}.withDefaults().Validate()
	if err == nil {
		t.Fatalf("a load run against %q was accepted; it would hold for %s, accept zero events, "+
			"and fail the min-accepted floor as though its load flags had been lost",
			selfPublishing, DefaultHold)
	}
	// The message has to name the real cause, since the whole point is that the floor's
	// message does not.
	for _, want := range []string{selfPublishing, "zero events"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not mention %q, so it sends the reader to the same "+
				"wrong place the floor failure would", err, want)
		}
	}

	// The counterweight, and it matters as much as the refusal: every LOAD-DRIVABLE
	// scenario must still validate, or the gate would have closed the harness entirely
	// while looking like a careful check.
	drivable := sim.LoadDrivableManifestIds()
	if len(drivable) == 0 {
		t.Fatal("no load-drivable scenarios remain, so the refusal above has closed the harness")
	}
	for _, id := range drivable {
		if err := (Profile{Manifest: id}).withDefaults().Validate(); err != nil {
			t.Errorf("load-drivable scenario %q was refused: %v", id, err)
		}
	}

	// An UNREGISTERED id is not this check's business — it belongs to sim.NewSim, which
	// reports it with the known-ids list. Two voices for one mistake is how a caller
	// learns to read neither.
	if err := (Profile{Manifest: "no-such-scenario"}).withDefaults().Validate(); err != nil {
		t.Errorf("an unregistered manifest was rejected by Validate (%v); that refusal belongs "+
			"to NewSim, which names the known ids", err)
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
