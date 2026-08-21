// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	dccore "github.com/devicechain-io/dc-microservice/core"
)

// These cover the fault NO event-driven feed can repair: a geofence-set publish that is LOST.
//
// The projection has three feeds and all three are triggered by something happening — a fact
// arriving, the process starting, a fence rule being published. emitMintedGeoFenceSet is
// best-effort by design (the authoring write has already committed and must not be failed by a
// wire problem), so when its publish is dropped device-management holds version N while DETECT
// holds N-1, no fact was ever written to redeliver, and no rule changed. The trigger for the
// repair is therefore the ABSENCE of an event, which only a timer can notice.

// countingFenceSource wraps the real schema-backed source so a test can see WHICH tenants a sweep
// read and HOW MANY times, and can hold a sweep open to exercise the in-flight guard.
//
// It counts through an atomic and guards its tenant log with a mutex because — unlike every other
// fence fixture here — it is called from the sweep goroutine while the test goroutine observes it.
type countingFenceSource struct {
	inner *schemaFenceSource

	calls atomic.Int64

	mu      sync.Mutex
	tenants []string

	// swept, when non-nil, receives once per CurrentFenceSet call so a test can wait for a sweep
	// deterministically instead of sleeping.
	swept chan string
	// hold, when non-nil, blocks each call until the test closes it — which keeps a sweep in
	// flight for as long as the in-flight guard needs to be observed.
	hold chan struct{}
}

func (c *countingFenceSource) CurrentFenceSet(ctx context.Context, tenant string) (*geofence.FenceSet, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.tenants = append(c.tenants, tenant)
	c.mu.Unlock()
	if c.swept != nil {
		c.swept <- tenant
	}
	if c.hold != nil {
		select {
		case <-c.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.inner.CurrentFenceSet(ctx, tenant)
}

var _ runtime.CurrentFenceSetSource = (*countingFenceSource)(nil)

func (c *countingFenceSource) tenantsRead() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.tenants...)
}

// newLoopRig builds a processor that can actually RUN its single-writer loop over a live fence
// projection: the fence fixtures of newFenceProcessor plus the plumbing run() dereferences (a
// parked reader, a cancellable context). TickInterval is the caller's, because the ticker is built
// once at loop entry and cannot be retuned afterwards.
func newLoopRig(t *testing.T, reg *runtime.RuleRegistry, src runtime.CurrentFenceSetSource,
	tick time.Duration) (*ResolvedEventsProcessor, context.CancelFunc) {
	t.Helper()
	loopCtx, cancel := context.WithCancel(context.Background())
	rp := &ResolvedEventsProcessor{
		// An unscripted reader parks on the loop context, so the read pump never feeds the loop
		// and the ONLY thing that wakes the select is the ticker under test.
		ResolvedEventsReader: &fakeReader{},
		Store:                newTestStore(t),
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   1000,
			CheckpointInterval: time.Hour,
			TickInterval:       tick,
			Clock:              detectcore.RealClock{},
		},
		registry:     reg,
		publisher:    runtime.NewPublisher(&recordingWriter{}, reg, (*detectMetrics)(nil)),
		clock:        detectcore.RealClock{},
		procCtx:      loopCtx,
		procCancel:   cancel,
		fenceUpdates: make(chan fenceUpdate, 8),
		FenceSets:    src,
	}
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return rp, cancel
}

// editedYard creates fence "yard" (version 1, a unit box at the origin) and then ENLARGES it
// (version 2, a box that still contains the origin corner). It returns device-management's Api and
// the facts it published.
//
// Enlarging rather than moving is what makes the control in the tests below airtight: the probe
// point 0.5,0.5 is inside BOTH versions, so a rule that does not fire can only be failing to
// RESOLVE version 2 — "the device was outside" is not an available explanation.
func editedYard(t *testing.T) (*dmmodel.Api, *fenceFactWriter) {
	t.Helper()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(context.Background(), "acme")
	facts := &fenceFactWriter{}
	api.GeoFenceSetPublisher = dmprocessor.NewGeoFenceSetWriter(facts, &fenceFactWriter{}, 0, nil, nil)

	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := api.UpdateGeoFence(dmCtx, "yard", &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 20, 20)}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Both versions really were minted AND really were handed to the publisher. The fault under
	// test is delivery, downstream of here — which is why the tests below simply never give these
	// payloads to the consumer.
	if len(facts.payloads) != 2 {
		t.Fatalf("fixture: device-management published %d fence facts, want 2", len(facts.payloads))
	}
	return api, facts
}

// A LOST fence-set publish is repaired by the sweep, and the tenant's containment rule evaluates
// again — with no restart, no fence edit, and no fact.
//
// The control is the same event before the sweep: the device sits inside version 2's fence, and the
// rule is live, yet nothing fires, because the projection never received version 2. That is the
// production symptom exactly — a counted eval error on every location event, forever.
func TestALostFenceSetPublishIsRepairedBySweepingTheArchive(t *testing.T) {
	ctx := context.Background()
	api, _ := editedYard(t)

	src := &schemaFenceSource{t: t, api: api}
	w := &captureWriter{}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), w, src)
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}

	// Startup seeded the CURRENT version, so to model the lost publish we roll the projection back
	// to the pre-edit state: version 1 held, version 2 never delivered. This is the state a pod is
	// left in when the edit's publish is dropped after startup.
	rp.fenceView = runtime.NewFenceSetView()
	rp.fenceView.Put("acme", boxFenceSetFor(1, "yard", 0, 0, 1, 1))
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != 1 {
		t.Fatalf("fixture: the projection holds %v, want [1] — the pre-edit state", held)
	}

	// CONTROL. The event is stamped with the version device-management now assigns, the device is
	// inside that fence, the rule is live — and nothing fires.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 2, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Fatalf("control: an unresolvable fence version published %d derived events, want 0", w.writes)
	}

	// The sweep. Joining the wait group is what makes this deterministic: startFenceReconcile
	// registers the sweep goroutine before launching it, so Wait returns only once it has finished
	// handing its results to the loop.
	rp.startFenceReconcile()
	rp.readerWG.Wait()
	if n := drainFenceUpdates(rp); n != 1 {
		t.Fatalf("the sweep marshalled %d fence updates onto the loop, want 1", n)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 2 || held[1] != 2 {
		t.Fatalf("after the sweep the projection holds %v, want [1 2]", held)
	}

	// And the same event now fires — the repair is evaluable, not merely present.
	rp.handle(locatedMsg(t, 2, "acme", "d1", "p@1", 2, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("after the sweep the fence rule published %d derived events, want 1", w.writes)
	}
}

// THE WIRING. The test above proves the sweep REPAIRS; this one proves the loop's ticker CALLS it.
//
// 🔴 THIS IS THE HALF THAT CANNOT BE ASSUMED. With startFenceReconcile perfect and the ticker
// branch missing, a lost publish stays lost for the pod's whole life and every assertion above
// still passes — which is exactly how a filter that was never wired in shipped green once already.
// It is also the first test in this package to drive the real ticker: every other one pins
// TickInterval to an hour and calls the tick's callees by hand.
func TestTheTickerSweepsTheGeofenceProjection(t *testing.T) {
	api, _ := editedYard(t)
	src := &countingFenceSource{
		inner: &schemaFenceSource{t: t, api: api},
		swept: make(chan string, 4),
	}
	rp, cancel := newLoopRig(t, fenceRuleReg(t, "acme", "p@1", "yard"), src, 5*time.Millisecond)
	rp.fenceView = runtime.NewFenceSetView()

	// Open the cadence gate the way the idle-advance suite opens its own: backdate the timestamp
	// rather than wait out a five-minute interval.
	rp.lastFenceReconcile = time.Now().Add(-time.Hour)

	rp.readerWG.Add(1)
	go rp.run()
	defer func() { cancel(); rp.readerWG.Wait() }()

	select {
	case tenant := <-src.swept:
		if tenant != "acme" {
			t.Fatalf("the ticker swept tenant %q, want acme", tenant)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ticker never swept the geofence projection: the periodic reconcile is not " +
			"wired into the loop's tick branch, so a lost fence-set publish is never repaired")
	}
}

// NEGATIVE CONTROL for the cadence. A tick that arrives INSIDE the interval sweeps nothing.
//
// Without this, the test above passes just as well against an implementation that reads
// device-management's archive on EVERY tick — once a second in production, per fence-rule tenant,
// forever. That is a cross-service read storm dressed as a repair.
func TestTheTickerDoesNotSweepInsideTheInterval(t *testing.T) {
	api, _ := editedYard(t)
	src := &countingFenceSource{
		inner: &schemaFenceSource{t: t, api: api},
		swept: make(chan string, 4),
	}
	rp, cancel := newLoopRig(t, fenceRuleReg(t, "acme", "p@1", "yard"), src, 5*time.Millisecond)
	rp.fenceView = runtime.NewFenceSetView()

	// The projection was just reconciled, which is the steady state: ExecuteStart stamps this
	// immediately after its post-replay reconcile.
	rp.lastFenceReconcile = time.Now()

	rp.readerWG.Add(1)
	go rp.run()
	defer func() { cancel(); rp.readerWG.Wait() }()

	// ~60 ticks at 5ms. Every one of them is inside the five-minute interval.
	select {
	case tenant := <-src.swept:
		t.Fatalf("a tick inside the interval swept tenant %q; the sweep is ungated and will hammer "+
			"device-management once per tick", tenant)
	case <-time.After(300 * time.Millisecond):
	}
	if n := src.calls.Load(); n != 0 {
		t.Fatalf("the archive was read %d times inside the interval, want 0", n)
	}
}

// Only tenants that actually hold a fence rule are swept. A tenant whose rules test no fence can
// produce no containment miss, so reading its archive every interval would buy nothing and cost a
// cross-service round trip per tenant in the instance.
func TestTheSweepReadsOnlyFenceRuleTenants(t *testing.T) {
	api, _ := editedYard(t)

	thr := 80.0
	plain, err := rules0.Compile(rules0.Rule{
		ID:   runtime.ComposeRuleID("globex", "hot"),
		Name: "hot",
		Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	reg := fenceRuleReg(t, "acme", "p@1", "yard")
	reg.Upsert(runtime.ScopedRule{Tenant: "globex", ProfileVersionToken: "p@1", Compiled: plain})

	src := &countingFenceSource{inner: &schemaFenceSource{t: t, api: api}}
	rp := newFenceProcessor(t, reg, &captureWriter{}, src)
	rp.fenceView = runtime.NewFenceSetView()

	rp.startFenceReconcile()
	rp.readerWG.Wait()

	if read := src.tenantsRead(); len(read) != 1 || read[0] != "acme" {
		t.Fatalf("the sweep read %v, want [acme] — a tenant with no fence rule was read", read)
	}
}

// Only ONE sweep runs at a time. If device-management is slow, ticks keep arriving; stacking a
// sweep per tick would turn a cross-service blip into a self-inflicted read storm against the
// service that is already struggling.
func TestASecondSweepIsSkippedWhileOneIsInFlight(t *testing.T) {
	api, _ := editedYard(t)
	src := &countingFenceSource{
		inner: &schemaFenceSource{t: t, api: api},
		swept: make(chan string, 4),
		hold:  make(chan struct{}),
	}
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, src)
	rp.fenceView = runtime.NewFenceSetView()

	// First sweep: starts, and parks inside the archive read.
	rp.startFenceReconcile()
	select {
	case <-src.swept:
	case <-time.After(2 * time.Second):
		t.Fatal("the first sweep never reached the archive")
	}

	// Second: refused while the first is still in flight.
	rp.startFenceReconcile()
	select {
	case <-src.swept:
		t.Fatal("a second sweep started while one was in flight; ticks against a slow " +
			"device-management will pile up")
	case <-time.After(150 * time.Millisecond):
	}

	close(src.hold)
	rp.readerWG.Wait()

	if n := src.calls.Load(); n != 1 {
		t.Fatalf("the archive was read %d times, want 1", n)
	}
	// And the guard RELEASES: a sweep after the first finishes runs normally. Without this the
	// test above is satisfied by a latch that permanently disables sweeping after the first one.
	rp.startFenceReconcile()
	rp.readerWG.Wait()
	if n := src.calls.Load(); n != 2 {
		t.Fatalf("after the first sweep finished the archive was read %d times, want 2 — the "+
			"in-flight guard latched instead of releasing", n)
	}
}

// The scaffold path — no fence source, so no projection — must not be reached into. A tick there
// is a no-op rather than a nil dereference.
func TestSweepingWithNoFenceSourceIsANoOp(t *testing.T) {
	rp := newFenceProcessor(t, fenceRuleReg(t, "acme", "p@1", "yard"), &captureWriter{}, nil)
	if err := rp.startFenceView(context.Background()); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}
	if rp.fenceView != nil {
		t.Fatal("fixture: no source must leave the projection nil")
	}
	rp.startFenceReconcile()
	rp.readerWG.Wait()
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("a disabled projection marshalled %d fence updates, want 0", n)
	}
}
