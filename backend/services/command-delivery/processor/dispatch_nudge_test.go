// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// nudgeable builds a QUEUED command for a named device, with an explicit status.
//
// It is separate from the sweep fixture's queued()/queuedFor() because those derive the
// device from the command TOKEN ("dev-"+token), which is fine for a sweep handed a
// pre-selected batch and useless here: every test below turns on which DEVICE a row belongs
// to, so the device has to be something a test states rather than something a helper
// derives.
//
// 🔑 THE STATUS IS WRITTEN OUT RATHER THAN LEFT ZERO. The sweep's own fixture leaves it
// empty because the sweep is handed its rows already selected; these tests exercise a read
// that FILTERS on status, so a fixture with no status would be asking the filter about a
// value the database can never hold.
func nudgeable(id uint, token, device string) *model.Command {
	cmd := &model.Command{DeviceToken: device, Name: "reboot", Status: model.CommandQueued.String()}
	cmd.ID = id
	cmd.Token = token
	cmd.TenantId = "acme"
	return cmd
}

// nudgeMetered wires real (unregistered) counters onto a processor so a test can read the
// nudge's declines. They are built with prometheus.NewCounter rather than through a
// Microservice, so they are registered nowhere at all — an unregistered counter still
// counts, which is the whole of what these assertions read.
func nudgeMetered(proc *CommandDeliveryProcessor) {
	proc.NudgeMetrics = NudgeMetrics{
		Requested: prometheus.NewCounter(prometheus.CounterOpts{Name: "nudges_requested_test"}),
		Dropped:   prometheus.NewCounter(prometheus.CounterOpts{Name: "nudges_dropped_test"}),
		Declined: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "nudges_declined_test"}, []string{"reason"}),
		Applied: prometheus.NewCounter(prometheus.CounterOpts{Name: "nudges_applied_test"}),
	}
}

// The whole point of the feature: a command for an idle device goes out NOW rather than on
// the sweep's next tick.
//
// 🔴 IT ASSERTS THE CLAIM AS WELL AS THE PUBLISH, AND BOTH IN THE RIGHT DIRECTION. A nudge
// that published without claiming would satisfy "the device was written to" perfectly while
// removing the one mechanism that makes a second dispatch path safe — the compare-and-set
// in MarkSent, which is what makes the loser of a race with the sweep decline instead of
// actuating the hardware twice.
func TestANudgeDispatchesTheSoleQueuedCommand(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 1 {
		t.Fatalf("the sole queued command must be published, got %d publishes", writer.count())
	}
	if len(writer.devices) != 1 || writer.devices[0] != "dev-1" {
		t.Fatalf("it must go to its own device's subject, got %v", writer.devices)
	}
	if len(api.markedSent) != 1 || api.markedSent[0] != 1 {
		t.Fatalf("the command must be CLAIMED before it is published, marked sent = %v", api.markedSent)
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Applied); got != 1 {
		t.Fatalf("applied = %v, want 1 — the only counter that says the nudge did anything", got)
	}
}

// The read must be tenant-fenced and bounded, and both are invisible from the outside.
//
// 🔴 THE TENANT IS ASSERTED BECAUSE THE FENCE IS THE READ'S ONLY ISOLATION. Device tokens
// are unique per tenant, not per instance, so a read issued with no tenant on its context
// either fails closed in production (the scope callback) or, if the callback were ever
// bypassed, would answer with another tenant's device. The LIMIT is asserted because it is
// what makes the sole-command rule expressible at all.
func TestANudgeReadsUnderItsTenantAndBoundsThePage(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}}
	proc := procWith(api, &recordingWriter{})
	nudgeMetered(proc)

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if len(api.queuedReads) != 1 {
		t.Fatalf("expected exactly one per-device read, got %d", len(api.queuedReads))
	}
	read := api.queuedReads[0]
	if read.tenant != "acme" {
		t.Fatalf("the read must carry the nudge's tenant, got %q", read.tenant)
	}
	if read.deviceToken != "dev-1" {
		t.Fatalf("the read must name the nudged device, got %q", read.deviceToken)
	}
	if read.limit != model.NudgeProbeLimit || read.limit < 2 {
		t.Fatalf("the read must be bounded at the probe limit and the probe limit must be able to "+
			"see a SECOND row, got %d (NudgeProbeLimit=%d)", read.limit, model.NudgeProbeLimit)
	}
}

// B2, the rule that removes the reorder race between the two dispatch paths.
//
// 🔴 IT IS A REFUSAL RULE, NOT AN OPTIMISATION. Both paths order by id, but two dispatchers
// walking one device's backlog can interleave BETWEEN rows — the sweep publishes row 1
// while the nudge, holding a newer read, publishes row 2 — and the device then receives a
// sequence out of order with neither path having done anything wrong. Per-device order is a
// delivery guarantee (a firmware update is a sequence whose order IS its meaning), so the
// nudge declines rather than reasoning about the interleaving.
func TestANudgeStandsDownWhenTheDeviceHasAnotherQueuedCommand(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{
		nudgeable(1, "c1", "dev-1"),
		nudgeable(2, "c2", "dev-1"),
	}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 0 {
		t.Fatalf("a device with more than one queued command must be left entirely to the sweep, "+
			"got %d publishes", writer.count())
	}
	if len(api.markedSent) != 0 {
		t.Fatalf("nothing may be claimed on a stand-down, marked sent = %v", api.markedSent)
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Declined.WithLabelValues(declineNotSole)); got != 1 {
		t.Fatalf("declined{not_sole} = %v, want 1 — a stand-down that is not counted is "+
			"indistinguishable from an idle instance", got)
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Applied); got != 0 {
		t.Fatalf("applied = %v, want 0", got)
	}
}

// The tenant lifecycle gate belongs on EVERY path that can put a command in front of a
// dispatcher, and this is now the second such path.
//
// 🔴 PUBLISHING IS A PHYSICAL ACTUATION AND IT HAPPENS BEFORE MarkSent FINDS ITS ROW. A
// command queued before an operator deleted the tenant would otherwise fire a valve or a
// relay on an offboarded customer's hardware, and by the time the row is swept away the
// actuation has already happened. The sweep has carried this refusal since the delete door
// existed; a nudge that skipped it would make the gate a thing that holds on one path and
// not the other, which is the same as not having it.
func TestANudgeRefusesADeletedTenant(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)
	proc.TenantDeleted = func(tenant string) bool { return tenant == "acme" }

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 0 {
		t.Fatalf("a deleted tenant's command must never be published, got %d publishes", writer.count())
	}
	if len(api.markedSent) != 0 {
		t.Fatalf("nothing may be claimed for a deleted tenant, marked sent = %v", api.markedSent)
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Declined.WithLabelValues(declineTenantDeleted)); got != 1 {
		t.Fatalf("declined{tenant_deleted} = %v, want 1", got)
	}
}

// The counterweight to the test above: every assertion there is also satisfied by a nudge
// that refuses EVERY tenant, which would make the feature a no-op that looks like a gate.
func TestANudgeStillDispatchesForALiveTenant(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)
	proc.TenantDeleted = func(tenant string) bool { return tenant == "someone-else" }

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 1 {
		t.Fatalf("a live tenant's command must still be published, got %d", writer.count())
	}
}

// The presence gate is the sweep's, and the nudge inherits it by calling the same code.
//
// 🔴 WITHOUT IT THE NUDGE WOULD BE A HOLE IN THE GATE THAT WIDENS WITH ADOPTION. An MQTT
// publish reaches only a device connected at that instant, so a command nudged at an absent
// device is dropped by the broker, recorded SENT, and expires as a TIMEOUT blaming a device
// that was never given it — the exact silent loss the gate exists to prevent, reintroduced
// on the faster of the two paths.
func TestANudgeWithholdsForAnAbsentDevice(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-1": asserted(false, mqttSource)}}

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 0 {
		t.Fatalf("a command nudged at an authoritatively absent device must be withheld, not "+
			"published, got %d publishes", writer.count())
	}
	if len(api.held) != 1 || api.held[0] != 1 {
		t.Fatalf("it must be moved to HELD, got held=%v", api.held)
	}
	if len(api.markedSent) != 0 {
		t.Fatalf("a held command must not be claimed for dispatch, marked sent = %v", api.markedSent)
	}
}

// The presence read must be issued under the command's TENANT, exactly as the sweep's is —
// the projection is tenant-scoped, so a read with the wrong tenant answers about nobody and
// the gate silently fails open for every device.
func TestANudgeReadsPresenceUnderTheCommandsTenant(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}}
	reader := &scriptedReader{states: map[string]presence.State{}}
	proc := procWith(api, &recordingWriter{})
	nudgeMetered(proc)
	proc.Presence = reader

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if len(reader.tenants) != 1 || reader.tenants[0] != "acme" {
		t.Fatalf("the presence read must carry the command's tenant, got %v", reader.tenants)
	}
	if len(reader.askedFor) != 1 || len(reader.askedFor[0]) != 1 || reader.askedFor[0][0] != "dev-1" {
		t.Fatalf("it must ask about the nudged device only, got %v", reader.askedFor)
	}
}

// A lost claim is the ORDINARY outcome of the nudge racing the sweep, and it must end the
// dispatch rather than publish anyway.
//
// 🔴 THIS IS THE PROPERTY THAT MAKES A SECOND DISPATCH PATH SAFE AT ALL. The sweep lock does
// not make delivery exactly-once — its own comment says so — so nothing but the
// compare-and-set stands between two paths and a device actuated twice.
func TestANudgeThatLosesTheClaimPublishesNothing(t *testing.T) {
	api := &fakeApi{queuedRows: []*model.Command{nudgeable(1, "c1", "dev-1")}, claimFails: true}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 0 {
		t.Fatalf("a dispatcher that lost the claim must not publish; the winner already did, and a "+
			"command is a physical actuation (got %d publishes)", writer.count())
	}
}

// B5: the counter whose meaning this feature inverted must be able to say WHICH path lost.
//
// 🔴 THE MUTANT THIS IS AIMED AT IS THE WEAKEST ONE, NOT THE MOST DESTRUCTIVE. Deleting the
// counter entirely would be caught by almost anything; labelling every loss "sweep" would
// leave a counter that still moves, still has a label, and still reads as plausible on a
// dashboard — while making the nudge's losses indistinguishable from the sweep's, which is
// precisely the signal the label exists to preserve.
func TestALostClaimIsCountedAgainstTheDispatchPathThatLostIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		run   func(*CommandDeliveryProcessor)
		want  dispatchPath
		other dispatchPath
	}{
		{
			name:  "the nudge",
			run:   func(p *CommandDeliveryProcessor) { p.DrainDevice(context.Background(), "acme", "dev-1") },
			want:  pathNudge,
			other: pathSweep,
		},
		{
			name:  "the sweep",
			run:   func(p *CommandDeliveryProcessor) { p.sweepLocked(context.Background()) },
			want:  pathSweep,
			other: pathNudge,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeApi{
				claimFails:    true,
				lockAvailable: true,
				pending:       []*model.Command{nudgeable(1, "c1", "dev-1")},
				queuedRows:    []*model.Command{nudgeable(1, "c1", "dev-1")},
			}
			proc := procWith(api, &recordingWriter{})
			nudgeMetered(proc)
			proc.ClaimsLost = prometheus.NewCounterVec(
				prometheus.CounterOpts{Name: "claims_lost_test"}, []string{"path"})

			tc.run(proc)

			if got := testutil.ToFloat64(proc.ClaimsLost.WithLabelValues(string(tc.want))); got != 1 {
				t.Fatalf("claims_lost{path=%q} = %v, want 1", tc.want, got)
			}
			if got := testutil.ToFloat64(proc.ClaimsLost.WithLabelValues(string(tc.other))); got != 0 {
				t.Fatalf("claims_lost{path=%q} = %v, want 0 — a loss attributed to the wrong path "+
					"is worse than no label: the series still moves and still looks right", tc.other, got)
			}
		})
	}
}

// A read failure must fail CLOSED — no read, no dispatch — and be visible.
func TestANudgeWhoseReadFailsDispatchesNothing(t *testing.T) {
	api := &fakeApi{queuedReadErr: errors.New("database unreachable")}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	nudgeMetered(proc)

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if writer.count() != 0 {
		t.Fatalf("a nudge that could not read must dispatch nothing, got %d publishes", writer.count())
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Declined.WithLabelValues(declineReadFailed)); got != 1 {
		t.Fatalf("declined{read_failed} = %v, want 1", got)
	}
}

// The race the nudge loses most often: the sweep, a peer replica, or a cancel emptied the
// device's queue before the worker looked. Benign, and counted so that "losing every race"
// is distinguishable from "an idle instance".
func TestANudgeForADeviceWithNothingQueuedIsCounted(t *testing.T) {
	api := &fakeApi{}
	proc := procWith(api, &recordingWriter{})
	nudgeMetered(proc)

	proc.DrainDevice(context.Background(), "acme", "dev-1")

	if got := testutil.ToFloat64(proc.NudgeMetrics.Declined.WithLabelValues(declineNothingQueued)); got != 1 {
		t.Fatalf("declined{nothing_queued} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(proc.NudgeMetrics.Applied); got != 0 {
		t.Fatalf("applied = %v, want 0", got)
	}
}

// blockingDrainer parks every drain until it is released, so a test can fill the queue.
type blockingDrainer struct {
	release chan struct{}
	mu      sync.Mutex
	seen    []string
}

func (d *blockingDrainer) DrainDevice(_ context.Context, _, deviceToken string) {
	d.mu.Lock()
	d.seen = append(d.seen, deviceToken)
	d.mu.Unlock()
	<-d.release
}

// countingDrainer just records.
type countingDrainer struct {
	mu   sync.Mutex
	seen []nudgeRequest
	done chan struct{}
}

func (d *countingDrainer) DrainDevice(_ context.Context, tenant, deviceToken string) {
	d.mu.Lock()
	d.seen = append(d.seen, nudgeRequest{tenant: tenant, deviceToken: deviceToken})
	d.mu.Unlock()
	if d.done != nil {
		d.done <- struct{}{}
	}
}

// The drop policy, which is the whole reason the nudge cannot hurt the enqueue path.
//
// 🔴 IT IS RUN WITH NO WORKERS STARTED, WHICH IS WHAT MAKES IT DETERMINISTIC. With workers
// draining, "how many fit" depends on how many happen to have been picked up, and a test
// that tolerated that range would tolerate a queue several times the intended depth.
// Unstarted, exactly NudgeQueueDepth fit and the next one must be dropped — so a mutant
// that merely ENLARGED the queue fails here, not only one that removed the bound.
//
// 🔴 THE ENQUEUE IS TIMED, BECAUSE THE OTHER MUTANT IS A BLOCKING SEND. Dropping the
// `default:` arm turns NudgeDevice into a send that waits for room, which is backpressure
// onto CreateCommand — the hot enqueue path serving the console, every SDK client and every
// REACT send-command. Without a deadline that mutant would hang the suite rather than fail
// it, which reports as a timeout minutes later instead of as this assertion.
func TestTheNudgeQueueDropsWhenFull(t *testing.T) {
	metrics := NudgeMetrics{
		Requested: prometheus.NewCounter(prometheus.CounterOpts{Name: "q_requested_test"}),
		Dropped:   prometheus.NewCounter(prometheus.CounterOpts{Name: "q_dropped_test"}),
	}
	nudger := newDispatchNudger(&countingDrainer{}, metrics)

	for i := 0; i < NudgeQueueDepth; i++ {
		nudger.NudgeDevice("acme", "dev-1")
	}
	if got := testutil.ToFloat64(metrics.Requested); got != float64(NudgeQueueDepth) {
		t.Fatalf("requested = %v, want %d — every nudge up to the depth must be accepted",
			got, NudgeQueueDepth)
	}
	if got := testutil.ToFloat64(metrics.Dropped); got != 0 {
		t.Fatalf("dropped = %v, want 0 before the queue is full", got)
	}

	returned := make(chan struct{})
	go func() {
		nudger.NudgeDevice("acme", "dev-1")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("NudgeDevice blocked on a full queue; it must drop instead. Blocking here is " +
			"backpressure onto CreateCommand, which trades a correctness-neutral delay (one sweep " +
			"tick) for a user-visible one on every enqueue")
	}

	if got := testutil.ToFloat64(metrics.Dropped); got != 1 {
		t.Fatalf("dropped = %v, want 1 — the nudge past the depth must be discarded", got)
	}
	if got := testutil.ToFloat64(metrics.Requested); got != float64(NudgeQueueDepth) {
		t.Fatalf("requested = %v, want %d — a dropped nudge must not also count as requested",
			got, NudgeQueueDepth)
	}
}

// The counterweight: a queue that dropped EVERYTHING would pass the test above.
func TestTheNudgeQueueActuallyDrains(t *testing.T) {
	drainer := &countingDrainer{done: make(chan struct{}, 4)}
	nudger := newDispatchNudger(drainer, NudgeMetrics{})
	nudger.Start()
	defer nudger.Stop()

	nudger.NudgeDevice("acme", "dev-7")

	select {
	case <-drainer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a nudge on a started queue was never drained")
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if len(drainer.seen) != 1 || drainer.seen[0] != (nudgeRequest{tenant: "acme", deviceToken: "dev-7"}) {
		t.Fatalf("the worker must be handed the tenant AND the device verbatim, got %v", drainer.seen)
	}
}

// Stop must return even with work still parked in the workers, and must not wait for the
// backlog to drain: a rolling restart is precisely when publishing is worth avoiding, and
// every buffered nudge is a row the sweep will dispatch anyway.
func TestStoppingTheNudgeQueueDoesNotWaitForItsBacklog(t *testing.T) {
	drainer := &blockingDrainer{release: make(chan struct{})}
	nudger := newDispatchNudger(drainer, NudgeMetrics{})
	nudger.Start()

	for i := 0; i < NudgeWorkers*4; i++ {
		nudger.NudgeDevice("acme", "dev-1")
	}
	// Let the workers pick up their blocking drains, then release them and stop.
	close(drainer.release)

	stopped := make(chan struct{})
	go func() {
		nudger.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	// Safe to call twice — the processor's ExecuteStop may be reached more than once.
	nudger.Stop()
}

// A processor assembled by literal — which is every test in this package, and any future
// caller that builds one without the constructor — has no nudger, and the enqueue path must
// be a no-op rather than a panic. The nudge is a latency optimisation; it may never be the
// reason a command cannot be created.
func TestANudgerlessProcessorAcceptsNudgesSilently(t *testing.T) {
	proc := procWith(&fakeApi{}, &recordingWriter{})
	if proc.Nudger() != nil {
		t.Fatal("a processor with no queue must report a nil nudger, not a non-nil interface " +
			"holding a nil pointer — a nil check that cannot detect nil is worse than none")
	}
	proc.nudger.NudgeDevice("acme", "dev-1")
	proc.nudger.Start()
	proc.nudger.Stop()
}

// eofReader ends the response-consumer loop immediately, so a test can drive the real
// lifecycle without a broker. ProcessMessage returns on EOF before it touches the RED
// metrics, which a literal-built processor does not have.
type eofReader struct{}

func (eofReader) ReadMessage(context.Context) (messaging.Message, error) {
	return messaging.Message{}, io.EOF
}
func (eofReader) HandleResponse(error) {}

// 🔴🔴 THE SECOND HALF OF THE PLUMBING, AND THE HALF A CONSTRUCTOR TEST CANNOT REACH. The
// constructor BUILDS the queue; ExecuteStart is what gives it workers. Deleting
// `cproc.nudger.Start()` leaves a perfectly wired nudger that accepts every nudge into a
// buffer nothing ever reads — the feature dead in production, no error, no log, and a suite
// that stays green because every other test in this file starts the queue itself. This test
// exists because the mutation harness found exactly that mutant surviving.
//
// It drives Initialize/Start/Stop rather than calling Start on the queue, for the same
// reason the constructor test drives the sweep: a component is not started because its Start
// method is right, it is started because the lifecycle carries the call.
func TestTheLifecycleStartsAndStopsTheNudgeQueue(t *testing.T) {
	drainer := &countingDrainer{done: make(chan struct{}, 8)}
	proc := procWith(&fakeApi{}, &recordingWriter{})
	proc.CommandResponsesReader = eofReader{}
	proc.nudger = newDispatchNudger(drainer, NudgeMetrics{})

	if err := proc.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := proc.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}

	proc.nudger.NudgeDevice("acme", "dev-1")
	select {
	case <-drainer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the lifecycle's Start must give the nudge queue its workers; without it every " +
			"nudge lands in a buffer nothing reads and the feature is inert in production")
	}

	if err := proc.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("ExecuteStop: %v", err)
	}

	// The counterweight: Stop must actually stop the workers. Enqueue again and require
	// silence. The window is enormous relative to the work — a live worker drains in
	// microseconds — so this is a widened race rather than a tight one.
	proc.nudger.NudgeDevice("acme", "dev-2")
	select {
	case <-drainer.done:
		t.Fatal("a nudge enqueued after the lifecycle's Stop was still drained; the queue's " +
			"workers outlived the component that owns them")
	case <-time.After(500 * time.Millisecond):
	}
}

// 🔴 THE PLUMBING, which every test above skips. Same rule and the same defect class this
// file's neighbours were written for: procWith builds the processor by struct literal, so
// everything above measures DrainDevice and the queue as FIELDS. The shipped binary reaches
// them only through NewCommandDeliveryProcessor, and with nothing covering that, deleting
// the constructor's `cproc.nudger = newDispatchNudger(...)` line would leave Api.Nudger
// bound to a nil queue — every command back on the sweep's cadence, nothing logged, nothing
// failed, this entire suite green.
//
// It drives the real path end to end rather than reading fields back: enqueue through the
// same interface main.go binds to Api.Nudger, then assert the per-device READ arrived. The
// read is the first thing only the real queue, a real worker and the real drainer can
// produce together.
//
// The functional area is the one every other constructor test in this package uses, and
// sharing it is safe: a Microservice built as a struct literal has no metrics registry, so
// the collectors the constructor builds are registered nowhere. They still count — which is
// what the assertions below read — and two constructions on one area no longer collide.
func TestConstructorWiresTheDispatchNudge(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "commanddelivery"}
	api := &fakeApi{}

	proc := NewCommandDeliveryProcessor(ms, nil, &recordingWriter{}, core.NewNoOpLifecycleCallbacks(),
		api, nil, nil, nil, NewDeliveryMetrics(ms))

	nudger := proc.Nudger()
	if nudger == nil {
		t.Fatal("the constructor must hand out a usable nudger; main.go binds this to Api.Nudger, " +
			"and a nil one disables the feature in silence")
	}
	if proc.NudgeMetrics.Requested == nil || proc.NudgeMetrics.Dropped == nil ||
		proc.NudgeMetrics.Declined == nil || proc.NudgeMetrics.Applied == nil {
		t.Fatalf("the constructor left a nudge counter unwired: requested=%v dropped=%v "+
			"declined=%v applied=%v", proc.NudgeMetrics.Requested != nil,
			proc.NudgeMetrics.Dropped != nil, proc.NudgeMetrics.Declined != nil,
			proc.NudgeMetrics.Applied != nil)
	}

	// 🔴 THE FIELDS BEING NON-NIL IS NOT THE SAME AS THE QUEUE HOLDING THEM, and the
	// difference is invisible without this: NudgeMetrics is passed BY VALUE, so a
	// constructor handing newDispatchNudger an empty struct — or simply built before the
	// fields were assigned — leaves `requested` and `dropped` frozen at zero in the shipped
	// binary while `declined` and `applied` (read from the processor) still move. The
	// counters the author's own doc calls the ones that make this path visible would be the
	// two that never move. Asserted below, after a real nudge, rather than here.
	if got := testutil.CollectAndCount(proc.ClaimsLost); got != 2 {
		t.Fatalf("claims_lost exports %d series, want 2 (sweep and nudge, pre-initialised). "+
			"A CounterVec gathers nothing until a label is first used, and the chart's dashboard "+
			"drives its instance picker off this counter — an instance that has never lost a "+
			"claim would name no instance at all", got)
	}

	proc.nudger.Start()
	defer proc.nudger.Stop()
	nudger.NudgeDevice("acme", "dev-1")

	deadline := time.Now().Add(5 * time.Second)
	for {
		api.mu.Lock()
		reads := append([]queuedRead(nil), api.queuedReads...)
		api.mu.Unlock()
		if len(reads) > 0 {
			if reads[0].tenant != "acme" || reads[0].deviceToken != "dev-1" {
				t.Fatalf("the nudge must reach the drain with its tenant and device intact, got %+v",
					reads[0])
			}
			// The queue must be holding the PROCESSOR'S counters, not a copy of an empty
			// struct. Requested is incremented inside the queue, so a zero here means the
			// queue was handed metrics the processor does not own -- the shipped binary
			// would run with `requested` and `dropped` permanently at zero while the two
			// counters read from the processor kept moving, which reads as a healthy path
			// doing no work.
			if got := testutil.ToFloat64(proc.NudgeMetrics.Requested); got != 1 {
				t.Fatalf("nudges_requested = %v after one nudge, want 1: the queue is not "+
					"incrementing the processor's own counters", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a nudge enqueued through the constructor's own nudger never reached the drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
