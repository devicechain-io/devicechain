// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSim is a Sim whose Tick does nothing but count itself (and optionally
// take a while doing it), so the tick LOOP can be tested without an ingress.
type fakeSim struct {
	ticks atomic.Int64
	delay time.Duration
}

func (f *fakeSim) Manifest() SimManifest                     { return SimManifest{Name: "fake"} }
func (f *fakeSim) Bootstrap(context.Context, *Runtime) error { return nil }
func (f *fakeSim) Tick(ctx context.Context, rt *Runtime) error {
	f.ticks.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return nil
}

func runFor(t *testing.T, f *fakeSim, load Load, d time.Duration) *Runtime {
	t.Helper()
	rt := &Runtime{Tenant: "acme", InstanceId: "dc", Load: load}
	lc := NewLifecycle(f, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(d)
	if err := lc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	return rt
}

// The configured interval must actually drive the ticker.
//
// Asserted in BOTH directions: a short interval has to speed the loop up, and
// an absent one has to leave the 5s demo cadence alone. Checking only the first
// would pass just as well if the loop ignored the interval and free-ran.
func TestTheConfiguredIntervalDrivesTheTickLoop(t *testing.T) {
	fast := &fakeSim{}
	runFor(t, fast, Load{EmitInterval: 20 * time.Millisecond}, 300*time.Millisecond)
	// 300ms/20ms = 15 ticks nominally; assert well under that to stay immune to
	// scheduler jitter while still being impossible to reach at the 5s default.
	if got := fast.ticks.Load(); got < 5 {
		t.Errorf("a 20ms interval produced %d ticks in 300ms: the configured "+
			"cadence is not reaching the ticker", got)
	}

	slow := &fakeSim{}
	runFor(t, slow, Load{}, 300*time.Millisecond)
	if got := slow.ticks.Load(); got != 0 {
		t.Errorf("the default 5s cadence produced %d ticks in 300ms, want 0", got)
	}
}

// A tick that outruns its interval must be COUNTED.
//
// time.Ticker drops ticks silently, so this is the only signal that the
// achieved rate is bounded by emit latency rather than by the interval that was
// asked for. Without it, an over-driven run looks identical to a healthy one
// and publishes a footprint against a load it never applied.
func TestATickThatOverrunsItsIntervalIsCounted(t *testing.T) {
	slowTick := &fakeSim{delay: 60 * time.Millisecond}
	rt := runFor(t, slowTick, Load{EmitInterval: 10 * time.Millisecond}, 300*time.Millisecond)
	if got := rt.Stats.Overruns.Load(); got == 0 {
		t.Error("a 60ms tick on a 10ms interval recorded no overruns: the sim " +
			"cannot tell an over-driven run from one that hit its target")
	}

	fastTick := &fakeSim{}
	rt = runFor(t, fastTick, Load{EmitInterval: 50 * time.Millisecond}, 300*time.Millisecond)
	if got := rt.Stats.Overruns.Load(); got != 0 {
		t.Errorf("a fast tick recorded %d overruns: the counter fires on healthy "+
			"runs, so it says nothing about the over-driven ones", got)
	}
}

// Starting a run must zero the counters, so /status reports THIS run's rate.
func TestStartBeginsAFreshMeasurementWindow(t *testing.T) {
	f := &fakeSim{}
	rt := &Runtime{Tenant: "acme", InstanceId: "dc", Load: Load{EmitInterval: time.Hour}}
	rt.Stats.Emitted.Store(12345)

	lc := NewLifecycle(f, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	before := time.Now()
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = lc.Stop() }()

	if got := rt.Stats.Emitted.Load(); got != 0 {
		t.Errorf("Emitted = %d after Start: a previous run's emits are still in "+
			"the numerator of this run's rate", got)
	}
	// The window must start at THIS run, not at some earlier one: a snapshot
	// taken now can only have covered the sliver of time since Start.
	if snap := rt.Stats.Snapshot(before.Add(time.Second)); snap.Seconds > 2 {
		t.Errorf("elapsed = %vs immediately after Start: the rate is being "+
			"averaged over time the sim spent stopped", snap.Seconds)
	}
}

// GET /status must report what actually happened, over the wire.
//
// This is the one surface a measurement run reads a published number from, and
// until this test existed the whole reporting layer was unpinned: overwriting
// the achieved rate with the CONFIGURED one — the exact lie this accounting
// exists to prevent — passed the entire suite. Every other test drove
// Stats.Snapshot directly and never went through the handler, so the JSON field
// names and the Snapshot-to-response wiring were guaranteed by nothing.
func TestStatusReportsTheAchievedRateNotTheConfiguredOne(t *testing.T) {
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc",
		Load:    Load{EmitInterval: 100 * time.Millisecond},
		Devices: make([]DeviceInstance, 10),
	}
	lc := NewLifecycle(&fakeSim{}, rt)

	// A run that reached a tenth of its target: 10 devices per 100ms asks for
	// 100/sec; 20 emits over 2s achieved 10/sec.
	rt.Stats.Reset(time.Now().Add(-2 * time.Second))
	rt.Stats.Emitted.Store(20)
	rt.Stats.Failed.Store(3)
	rt.Stats.Overruns.Store(7)

	mux := http.NewServeMux()
	NewControlServer(lc, rt).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	var got statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode /status: %v", err)
	}

	if got.TargetRate != 100 {
		t.Errorf("targetRatePerSec = %v, want 100", got.TargetRate)
	}
	// The load-bearing assertion: the achieved rate is derived from emits, and
	// is nowhere near the target this run failed to reach.
	if got.Stats.Rate < 9 || got.Stats.Rate > 11 {
		t.Errorf("achievedRatePerSec = %v, want ~10 (20 emits over 2s)", got.Stats.Rate)
	}
	if got.Stats.Rate == got.TargetRate {
		t.Error("the achieved rate equals the configured one: /status is echoing " +
			"the request back as if it were a measurement")
	}
	if got.Stats.Emitted != 20 || got.Stats.Failed != 3 || got.Stats.Overruns != 7 {
		t.Errorf("counters = %+v, want 20 emitted / 3 failed / 7 overruns", got.Stats)
	}
	if got.DeviceCount != 10 || got.EmitIntervalMs != 100 {
		t.Errorf("deviceCount/emitIntervalMs = %d/%d, want 10/100",
			got.DeviceCount, got.EmitIntervalMs)
	}
}

// A stopped run's rate must stay fixed at what it achieved.
//
// The numerator stops at Stop but wall-clock does not, so an unfrozen elapsed
// makes the reported rate decay without bound — a 60s run read a minute later
// halves. It decays silently and looks exactly like a real number.
func TestAStoppedRunsRateStopsDecaying(t *testing.T) {
	f := &fakeSim{}
	rt := &Runtime{Tenant: "acme", InstanceId: "dc",
		Load: Load{EmitInterval: 10 * time.Millisecond}}
	lc := NewLifecycle(f, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	rt.Stats.Emitted.Store(500)
	time.Sleep(100 * time.Millisecond)
	if err := lc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	first := rt.Stats.Snapshot(time.Now())
	time.Sleep(150 * time.Millisecond)
	second := rt.Stats.Snapshot(time.Now())

	if first.Rate != second.Rate {
		t.Errorf("a stopped run's rate moved from %v to %v: elapsed is still "+
			"counting wall-clock, so every later read understates the run",
			first.Rate, second.Rate)
	}
	if first.Rate <= 0 {
		t.Fatalf("stopped run reported a rate of %v", first.Rate)
	}
}

// Stop must JOIN the tick loop, not merely signal it.
//
// Otherwise a Stop/Start pair resets the counters while the previous run's
// emits are still landing, and the new window opens holding the old run's work
// — attributing one run's load to another's measurement.
func TestStopWaitsForTheTickLoopToExit(t *testing.T) {
	// A tick slow enough that it is certainly mid-flight when Stop lands.
	f := &fakeSim{delay: 150 * time.Millisecond}
	rt := &Runtime{Tenant: "acme", InstanceId: "dc",
		Load: Load{EmitInterval: 10 * time.Millisecond}}
	lc := NewLifecycle(f, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(30 * time.Millisecond) // let a tick get under way

	if err := lc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// By the time Stop returns the loop has exited, so the tick count is final.
	settled := f.ticks.Load()
	time.Sleep(200 * time.Millisecond)
	if got := f.ticks.Load(); got != settled {
		t.Errorf("ticks went from %d to %d after Stop returned: the loop was "+
			"still running, so a restart would reset counters underneath it",
			settled, got)
	}
}

// A REJECTING ingress must be visible in `failed`, because it is invisible in
// `overruns`.
//
// This pins a measured blind spot rather than a bug. When the ingress rejects
// quickly — a 429 from per-tenant rate limiting (ADR-023) — the tick gets
// SHORTER, not longer, so the overrun detector never fires. Measured against a
// 10%-accept ingress: the sim applied a tenth of its target rate with overruns
// sitting at exactly 0 for the whole run.
//
// So triage cannot start at `overruns`. The test exists to keep that true in
// the docs: if someone later "fixes" overruns to fire here, or drops the
// failure counter, this fails and points at the reasoning.
func TestAFastRejectingIngressShowsUpInShedNotOverruns(t *testing.T) {
	// Reject everything with a 429 (a per-tenant rate-limit shed), immediately — the
	// fastest possible tick.
	rt := fakeIngress(t, 20, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	rt.Load = Load{EmitInterval: 20 * time.Millisecond}

	lc := NewLifecycle(&emittingSim{}, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := lc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	snap := rt.Stats.Snapshot(time.Now())
	// A 429 is a governed shed, not a failure — but it must still be VISIBLE (the point
	// of this test: fast rejection must show up in a counter, unlike Overruns, which is
	// blind to it). It shows up in Shed.
	if snap.Shed == 0 {
		t.Error("a fully-rejecting (429) ingress recorded no sheds: fast rejection is " +
			"invisible, which would leave a run under-applying load with every signal " +
			"reading healthy")
	}
	if snap.Failed != 0 {
		t.Errorf("failed = %d against a 429-only ingress: a governed shed is a clean "+
			"non-accept, not a failure", snap.Failed)
	}
	if snap.Emitted != 0 {
		t.Errorf("emitted = %d against an ingress that accepted nothing", snap.Emitted)
	}
	if snap.Rate != 0 {
		t.Errorf("achieved rate = %v with zero successful emits", snap.Rate)
	}
	// Documenting the blind spot, not endorsing it: overruns SHOULD be ~0 here,
	// which is exactly why it must not be the first thing anyone reads.
	if snap.Overruns > 0 {
		t.Logf("note: overruns = %d — rejection was slow enough to overrun; the "+
			"blind spot this test documents is the fast-rejection case", snap.Overruns)
	}
}

// emittingSim drives real emits through EmitAll so the accounting under test is
// the production path, not a counter poked by hand.
type emittingSim struct{}

func (emittingSim) Manifest() SimManifest                     { return SimManifest{Name: "emitting"} }
func (emittingSim) Bootstrap(context.Context, *Runtime) error { return nil }
func (emittingSim) Tick(ctx context.Context, rt *Runtime) error {
	return EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)), constantMetrics)
}
