// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// NudgeDrainer performs one nudge: it looks at a single device inside a single tenant and
// dispatches that device's queued backlog if — and only if — the rules in DrainDevice
// allow it.
//
// Narrow on purpose, exactly as presence.Releaser is on the wake path: the queue below can
// then be tested with no database, no broker and no processor, which is what lets the drop
// policy be exercised without staging a delivery.
type NudgeDrainer interface {
	DrainDevice(ctx context.Context, tenant, deviceToken string)
}

// NudgeMetrics measures a path whose every failure is silent.
//
// 🔴 EVERY OUTCOME HERE IS INVISIBLE WITHOUT A COUNTER. A nudge that is dropped, declined,
// or never wired produces the behaviour the platform had before the nudge existed — the
// command goes out on the sweep — so "the feature is off" and "the feature is working"
// look identical from every other vantage point: commands flow, nothing errors, latency is
// simply what it was. These are what tell the two apart.
type NudgeMetrics struct {
	// Requested counts nudges accepted onto the queue.
	Requested prometheus.Counter

	// Dropped counts nudges discarded because the queue was full. 🔑 A LATENCY SIGNAL,
	// NOT AN ERROR RATE — see the drop policy on NudgeDevice. A standing rate means
	// commands are reaching devices on the sweep's cadence instead of immediately, which
	// is slower and still correct.
	Dropped prometheus.Counter

	// Declined counts nudges the drain refused to act on, BY REASON.
	//
	// 🔴🔴 THIS IS WHAT STOPS THE NUDGE BEING A SILENT NO-OP, and it is the counter
	// easiest to dismiss as noise. The drain declines far more often than it acts — that
	// is the design, because the sole-queued-command rule is a refusal rule — so without
	// a reason-labelled count of the refusals, a nudge that declines EVERY device looks
	// exactly like one with nothing to do: Requested climbs, Applied stays flat, and both
	// readings are equally consistent with "an idle instance" and "the gate wired
	// backwards". The stranded reconciler carries StrandedSkipped for the same reason and
	// it is the same lesson.
	Declined *prometheus.CounterVec

	// Applied counts nudges that found exactly one queued command and put it through the
	// delivery gates. It is the only number here that says the feature is doing anything
	// at all.
	//
	// ⚠️ IT IS NOT A COUNT OF PUBLISHES, AND MUST NOT BE READ AS ONE. What follows is the
	// same gate the sweep applies, which may hold the command (the device is absent) or
	// fail it (its transport carries no command path). Applied means "the nudge handed
	// this to delivery", not "a device was actuated" — the publish is metered where every
	// other publish is.
	Applied prometheus.Counter
}

// Decline reasons. These are Prometheus label values, so they are a closed vocabulary and
// each names a DIFFERENT question — an operator reading the series needs to know which.
const (
	// declineNotSole: the device has more than one queued command, so the nudge stood
	// down and left the whole backlog to the sweep.
	//
	// 🔑 THIS IS THE EXPECTED SERIES ON A BUSY DEVICE AND IS NOT A FAULT. It is the rule
	// that removes the reorder race between the two dispatch paths: both order by id, but
	// two dispatchers walking one device's backlog can still interleave BETWEEN rows, so
	// the nudge refuses rather than reasoning about the interleaving. What it costs is
	// exactly the latency the platform had before — one sweep tick.
	declineNotSole = "not_sole"

	// declineNothingQueued: the device has no queued command left by the time the worker
	// looked. The sweep, a peer replica's nudge, or a cancel got there first. Benign, and
	// worth counting because a rate approaching Requested means the nudge is losing every
	// race and buying nothing.
	declineNothingQueued = "nothing_queued"

	// declineTenantDeleted: the command's tenant has been through the ADR-077 delete door,
	// so no dispatcher may be handed it. A refusal, not a failure — the rows are about to
	// be erased with the rest of the tenant.
	declineTenantDeleted = "tenant_deleted"

	// declineReadFailed: the per-device read failed, so the nudge could not decide
	// anything. Fails closed: no read, no dispatch. The sweep covers it.
	declineReadFailed = "read_failed"
)

// NudgeQueueDepth and NudgeWorkers size the dispatch-nudge queue.
//
// 🔴 SIZED FOR AN ENQUEUE BURST, WHICH IS THE ONLY LOAD THAT MATTERS HERE. Steady state is
// a trickle — an operator issuing a command from the console, a REACT rule firing. The
// shape to survive is a fan-out of automated enqueues: an alarm storm where one detection
// rule raises against a few hundred devices at once, each a separate CreateCommand and so
// each a separate nudge.
//
// The worker count is small deliberately, and the reasoning is the wake queue's unchanged:
// each nudge is one indexed SELECT and at most one indexed UPDATE against the same table,
// so more workers would mostly add concurrent writers to one table. Publishing is the only
// part that leaves the database, and it is already bounded by the broker.
//
// 🔑 THE QUEUE IS BOUNDED AND FULL MEANS DROP, WHICH IS SAFE ONLY BECAUSE THE SWEEP IS THE
// NET UNDERNEATH. A dropped nudge does not lose a command: the row stays QUEUED and the
// next sweep tick dispatches it. So the cost of a drop is latency — one tick, which is the
// latency the platform had before the nudge existed — while the cost of NOT dropping would
// be backpressure onto CreateCommand, which is the hot enqueue path serving the console,
// every SDK client and every REACT send-command. Making a create wait on a dispatch
// optimisation would be trading a correctness-neutral delay for a user-visible one.
const (
	NudgeQueueDepth = 1024
	NudgeWorkers    = 4
)

// nudgeRequest is one device that has just been given a command.
//
// 🔑 IT CARRIES NO COMMAND ID, AND THAT IS THE B1 GUARANTEE IN THE DATA MODEL. The worker
// re-reads the device's backlog and acts on its OLDEST row, so a nudge can never dispatch
// the command that triggered it ahead of an older one — there is no way to express that
// request. A queue of command ids would have made stepping over an older command a
// one-line change nobody would notice.
type nudgeRequest struct {
	tenant      string
	deviceToken string
}

// dispatchNudger is a bounded queue drained by a small pool of workers.
type dispatchNudger struct {
	drainer  NudgeDrainer
	queue    chan nudgeRequest
	metrics  NudgeMetrics
	wg       sync.WaitGroup
	stop     chan struct{}
	started  sync.Once
	stopOnce sync.Once
}

// newDispatchNudger builds the queue WITHOUT starting its workers.
//
// 🔴 THE SPLIT BETWEEN BUILDING AND STARTING IS DELIBERATE AND IS NOT THE WAKE QUEUE'S
// SHAPE. This one belongs to a lifecycle component: the processor is constructed in the
// NatsManager's create callback and started later, and Api.Nudger is bound at construction
// so an enqueue arriving before Start still lands in the buffer rather than on a nil
// interface. Starting goroutines from a constructor would also mean a processor that is
// built and never started leaks four of them.
func newDispatchNudger(drainer NudgeDrainer, metrics NudgeMetrics) *dispatchNudger {
	return &dispatchNudger{
		drainer: drainer,
		queue:   make(chan nudgeRequest, NudgeQueueDepth),
		metrics: metrics,
		stop:    make(chan struct{}),
	}
}

// NudgeDevice enqueues a nudge, dropping it if the queue is full.
//
// It never blocks and never returns an error, because its caller is CreateCommand and
// there is nothing useful an enqueue could do with either. See NudgeQueueDepth for why
// dropping is the safe direction.
//
// Nil-receiver safe: a processor assembled by literal (which every test in this package
// does) carries no nudger, and calling through it must be a no-op rather than a panic on
// the enqueue path.
func (n *dispatchNudger) NudgeDevice(tenant, deviceToken string) {
	if n == nil || n.queue == nil {
		return
	}
	select {
	case n.queue <- nudgeRequest{tenant: tenant, deviceToken: deviceToken}:
		incr(n.metrics.Requested, 1)
	default:
		incr(n.metrics.Dropped, 1)
	}
}

// Start launches the workers. Safe to call more than once; only the first starts anything.
func (n *dispatchNudger) Start() {
	if n == nil {
		return
	}
	n.started.Do(func() {
		for i := 0; i < NudgeWorkers; i++ {
			n.wg.Add(1)
			go n.run()
		}
	})
}

// Stop drains the workers. Safe to call more than once, and safe to call on a nudger that
// was never started — the WaitGroup is empty, so the wait returns immediately.
func (n *dispatchNudger) Stop() {
	if n == nil {
		return
	}
	n.stopOnce.Do(func() {
		close(n.stop)
		n.wg.Wait()
	})
}

// run is one worker.
//
// 🔴 IT CHECKS stop FIRST AND SEPARATELY. A single select over both channels picks
// uniformly between a ready stop and a ready queue, so a shutdown behind a full queue would
// take an unbounded number of extra dispatches to be noticed in the worst case — one extra
// per worker in expectation, which is the honest figure, but nothing bounds the tail. Draining what is buffered is
// explicitly NOT wanted at shutdown: every buffered nudge is a row the sweep will dispatch
// anyway, and publishing during a rolling restart is the one thing worth avoiding.
func (n *dispatchNudger) run() {
	defer n.wg.Done()
	for {
		select {
		case <-n.stop:
			return
		default:
		}
		select {
		case <-n.stop:
			return
		case req := <-n.queue:
			n.drainer.DrainDevice(context.Background(), req.tenant, req.deviceToken)
		}
	}
}
