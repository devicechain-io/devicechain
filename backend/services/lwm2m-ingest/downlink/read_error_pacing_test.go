// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

// 🔴 WHAT THIS FILE IS FOR, AND WHY IT EXISTS AT ALL. This loop carried a comment saying it
// did not need a bound: "a durable NATS outage also stalls this term's lease renewal, which
// evicts the term (ctx cancel) and ends the loop — so this never spins forever on a dead
// broker." That claim was true and it was beside the point, and it went unchecked for as
// long as it was written down.
//
// The lease renews over the SAME connection the reader uses, so it covers exactly one case:
// the whole broker being gone. Lease.KeepAlive gives up only when Renew FAILS and the TTL
// window has passed. Every error that actually reaches this loop — a JetStream API error, a
// 409 from a stream at its MaxAckPending or MaxWaiting ceiling, a consumer whose leadership
// keeps moving, a subscription that cannot be rebuilt, an io.EOF from a rebind that gave up
// — happens on a connection that is perfectly healthy, where kv.Update keeps succeeding and
// the term is renewed indefinitely.
//
// 🔑 SO THE STATE THE CLAIM RULED OUT WAS REACHABLE, AND INVISIBLE FROM THE OUTSIDE. The pod
// reports Ready, it holds the lease, it reports leader and serving, and it dispatches no
// commands at all. Metrics has counters for parks, drains and claims and none for read
// errors, so the only trace is one log line a second. Every downlink command for every
// tenant this adapter serves goes undelivered for as long as the pod lives.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE, so
// these count the READS, on a virtual clock, so the number is the loop's and not the
// machine's.

const readCap = 5000

// pacedReads is the most a paced loop may take to give up: a two-minute budget against a
// backoff starting at 200ms and doubling to a 5s ceiling is about thirty reads, and the
// margin is so the constants can move without this file having to.
const pacedReads = 60

// pacingDispatcher builds a dispatcher with only what the read loop touches. One worker,
// because the workers do nothing here and each is a goroutine the test would otherwise
// leave parked.
//
// The pacer is reportless: an exhausted budget in production ends the PROCESS, which here is
// the test binary. That the report happens is core's to prove and it does, in
// readpacer_test.go; what belongs here is that the loop stops asking and that Run returns.
func pacingDispatcher(t *testing.T, reader reader) *Dispatcher {
	t.Helper()
	return NewDispatcher(reader, &fakePublisher{}, &fakeLookup{}, &fakeExecutor{}, &fakeFetcher{}, nil, Metrics{},
		Options{
			Workers:   1,
			ReadPacer: core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock()),
		})
}

// runWithin runs the dispatcher and fails if it does not return, rather than hanging the
// package for the go test timeout. A loop that never gives up is the defect under test, so
// the failure mode has to be an assertion and not a stuck run.
//
// 🔑 RETURNING IS PART OF WHAT IS BEING ASSERTED, not just how the test avoids hanging.
// Run ends by waiting on its workers, and those workers are ended by the read loop exiting
// — so a Run that stopped reading but left them parked would never return here. Unlike the
// other seven loops, this one does not treat io.EOF as a clean exit (every error goes to
// the pacer), so an unpaced Run does not end when the fake reader runs out either: it
// retries the EOF forever. Both regressions surface as this timeout.
func runWithin(t *testing.T, d *Dispatcher, ctx context.Context, budget time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("Dispatcher.Run did not return within %s", budget)
	}
}

// 🔴 THE NEGATIVE CONTROL. Before the pacer this returned only when the term was cancelled,
// which a healthy broker never does. Delete the PauseAfterError call and this goes red.
func TestTheCommandReadLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	d := pacingDispatcher(t, reader)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runWithin(t, d, ctx, 30*time.Second)

	if ctx.Err() != nil {
		t.Fatal("the term context was cancelled, so this proves nothing about a loop that has " +
			"to stop while the term is still HELD — which is the whole case the lease does not cover")
	}
	// No "ran out of reader" branch here, unlike the six loops in event-processing: this one
	// has no io.EOF escape, so a loop that never gives up does not stop at EOFAfter — it
	// retries the EOF. runWithin's timeout is what catches that, and EOFAfter is only a
	// bound on how much the fake will produce.
	if reader.Reads > pacedReads {
		t.Fatalf("the command-reader loop took %d reads to stop, too many to be a paced retry",
			reader.Reads)
	}
	t.Logf("stopped after %d reads with the term still held", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT. The test above is satisfied by a loop that gives up on the FIRST
// error, which would hand leadership away on every broker hiccup. Succeeded() on the good
// path is the only thing preventing that: without it the failure run is never cleared and an
// adapter taking one blip every few minutes accumulates them across an uptime into a give-up
// it never earned. Delete that line and this goes red while the test above stays green.
func TestASuccessfulReadKeepsTheCommandReadLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	d := pacingDispatcher(t, reader)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runWithin(t, d, ctx, 30*time.Second)

	if reader.Reads < eofAt {
		t.Fatalf("the command-reader loop gave up after %d reads while every other read was "+
			"succeeding; a loop that does not clear its failure run ends a term for faults it "+
			"already recovered from", reader.Reads)
	}
}

// intermittentReader fails every other read and succeeds in between, which is a stream having
// a bad time rather than a broken one. It ends at EOFAfter so a loop that never gives up still
// terminates the test.
//
// 🔑 THE EOFAfter GUARD IS LOAD-BEARING AND EASY TO DROP. Without the "> 0" the comparison
// Reads+1 >= EOFAfter is TRUE for an unset EOFAfter, so every read would fail and this fake
// would quietly become a second copy of FailingReader — making the counterweight pass for the
// wrong reason, which is the one thing a counterweight must not do.
type intermittentReader struct {
	msgtest.FailingReader
	n int
}

func (r *intermittentReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.n++
	if r.n%2 == 1 || (r.EOFAfter > 0 && r.Reads+1 >= r.EOFAfter) {
		return r.FailingReader.ReadMessage(ctx)
	}
	r.Reads++
	return messaging.Message{}, nil
}
