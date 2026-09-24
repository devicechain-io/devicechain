// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// startedManager builds a NatsManager the way a service does — NewNatsManager, then
// the lifecycle's Initialize and Start — against srv.
func startedManager(t *testing.T, srv *natsserver.Server, area string, oncreate func(*NatsManager) error) *NatsManager {
	t.Helper()
	if oncreate == nil {
		oncreate = func(*NatsManager) error { return nil }
	}
	nmgr := NewNatsManager(testMicroservice(t, srv, uniqueArea(area)), core.NewNoOpLifecycleCallbacks(), oncreate)
	ctx := context.Background()
	nmgr.RecordMaxDeliveries(recordNothing)
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	return nmgr
}

// queueOnSlowSubscription subscribes a slow async handler on the manager's own
// connection and publishes n messages to it from another, returning the handled count.
// When it returns, the handler has started and the rest of the messages are sitting in
// the subscription's pending queue — the queue a drain exists to deliver.
func queueOnSlowSubscription(t *testing.T, srv *natsserver.Server, nc *nats.Conn, subject string, n int, handle func()) *atomic.Int64 {
	t.Helper()
	other, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect publisher: %v", err)
	}
	t.Cleanup(other.Close)

	var handled atomic.Int64
	sub, err := nc.Subscribe(subject, func(*nats.Msg) {
		handle()
		handled.Add(1)
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush subscription: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := other.Publish(subject, []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if err := other.Flush(); err != nil {
		t.Fatalf("flush publishes: %v", err)
	}
	// Wait until every message has reached the client — handled, in the handler, or in
	// the pending queue — so the stop starts with a real backlog rather than with
	// messages still in flight from the server. The one inside the handler is counted by
	// neither Pending nor handled, hence the allowance of one.
	waitFor(t, "the backlog to reach the client", func() bool {
		pending, _, _ := sub.Pending()
		return handled.Load()+int64(pending) >= int64(n-1)
	})
	return &handled
}

// Stop must not return until the drain it started has finished.
//
// nats.Conn.Drain is asynchronous. Stop used to return straight after calling it, and
// Terminate's Close a moment later cut the drain off: messages already queued on a live
// async subscription were never delivered (89 of 100, reproduced). The counts below are
// read the moment Stop returns — no wait, no Eventually — because "delivered by the time
// Stop returned" is the property; "delivered eventually" is what a cut-off drain can
// also look like when the Close happens to be slow.
func TestStopWaitsForTheDrainToDeliverTheBacklog(t *testing.T) {
	logs := captureLogs(t)
	srv := startEmbeddedServer(t)
	nmgr := startedManager(t, srv, "drain-waits", nil)
	nc := nmgr.Conn()

	const n = 100
	handled := queueOnSlowSubscription(t, srv, nc, "drain.backlog", n, func() {
		time.Sleep(5 * time.Millisecond)
	})
	if got := handled.Load(); got >= n {
		t.Fatalf("all %d messages were handled before the stop began; there is no backlog to drain", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := nmgr.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if got := handled.Load(); got != n {
		t.Errorf("%d of %d queued messages had been handled when Stop returned: the drain was "+
			"not waited for", got, n)
	}
	if !nc.IsClosed() {
		t.Errorf("the connection is %s when Stop returned, want CLOSED: Stop must leave it "+
			"closed rather than draining", nc.Status())
	}
	// Logged by Stop itself on the wait's success branch, so it is already there.
	if findLog(logs, "NATS connection drained and closed") == nil {
		t.Error("Stop did not report the drain finishing")
	}
	if rec := findLog(logs, "did not finish within the shutdown budget"); rec != nil {
		t.Errorf("a drain with ample budget reported running out of it: %v", rec["message"])
	}

	if err := nmgr.Terminate(ctx); err != nil {
		t.Fatalf("terminate: %v", err)
	}
}

// ...and the wait is BOUNDED by the stop's context, because a drain can hang: a
// subscription whose handler never returns holds it for the library's whole
// DrainTimeout, and the teardown budget is shorter than that.
//
// When the budget ends the wait, Stop closes the connection itself, inside the budget —
// Close is what flushes buffered publishes and acks, and leaving it to Terminate risks
// the teardown abandoning the stop before Terminate is reached.
//
// ⚠️ IsClosed is NOT asserted the moment Stop returns on this path, and that is the
// library, not slack. The inline Close lands while nats.go's drain goroutine is still
// waiting on its subscriptions; the Close ends that wait, and the goroutine then moves
// the status to DRAINING_PUBS over CLOSED and spends up to five seconds flushing a socket
// that is gone before closing again. So the status is read with a wait longer than that
// flush — and the WARN line, written before Stop returns, is what proves the inline
// Close ran.
func TestStopBoundsTheDrainByItsContextAndClosesInline(t *testing.T) {
	logs := captureLogs(t)
	srv := startEmbeddedServer(t)
	nmgr := startedManager(t, srv, "drain-bounded", nil)
	nc := nmgr.Conn()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	queueOnSlowSubscription(t, srv, nc, "drain.stuck", 5, func() { <-release })

	// Four times closeReserve, so the reserve is the full constant rather than its
	// quarter-of-the-budget clamp: the wait should end one closeReserve before the
	// deadline, which leaves a half-reserve margin on either side for the assertions.
	const budget = 4 * closeReserve
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	began := time.Now()
	if err := nmgr.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	took := time.Since(began)

	// The reserve is the contract: budget left for the Close and for the components that
	// stop after NATS. A wait that ran to the deadline leaves them none.
	if took > budget-closeReserve/2 {
		t.Errorf("Stop took %s against a %s budget: the drain wait did not leave the %s reserve "+
			"for what stops after it", took, budget, closeReserve)
	}
	if ctx.Err() != nil {
		t.Errorf("the stop's budget had expired when Stop returned (%v); the reserve was spent", ctx.Err())
	}
	// ...and Stop returning much sooner means it did not wait at all.
	if took < budget/2 {
		t.Errorf("Stop returned after %s with a hung subscription: it did not wait for the drain", took)
	}

	rec := findLog(logs, "did not finish within the shutdown budget")
	if rec == nil {
		t.Fatal("the budget ran out and nothing said so")
	}
	if lvl, _ := rec["level"].(string); lvl != "warn" {
		t.Errorf("the budget-expired line was logged at %q, want warn", lvl)
	}
	if subs, _ := rec["subscriptionsStillDraining"].(float64); subs < 1 {
		t.Errorf("the budget-expired line reports %v subscriptions still draining; the stuck one "+
			"should be counted", rec["subscriptionsStillDraining"])
	}

	waitFor(t, "the connection to close", nc.IsClosed)
}

// A Stop that finds the connection already closed has nothing to drain or release, and
// must say so quietly.
//
// This is the ordinary stop after an unrequested close: the liveness failure makes the
// kubelet stop the container, and that stop runs over a dead connection. Each reader Unsubscribe
// and the Drain fail there, and logging those at ERROR would bury the one ERROR that
// says why the pod is restarting — on every cycle of the crash loop.
func TestStopOverAnAlreadyClosedConnectionIsQuiet(t *testing.T) {
	logs := captureLogs(t)
	srv := startEmbeddedServer(t)
	nmgr := startedManager(t, srv, "drain-already-closed", func(n *NatsManager) error {
		_, err := n.NewReader(streams.InboundEvents)
		return err
	})
	if len(nmgr.readers) == 0 {
		t.Fatal("no reader was created; the unsubscribe half below would prove nothing")
	}

	// Closed without the manager asking, as a revoked credential would.
	nmgr.Conn().Close()
	waitFor(t, "the unrequested-close log", func() bool {
		return findLog(logs, "CLOSED permanently") != nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	began := time.Now()
	if err := nmgr.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Stop over a closed connection took %s; there was nothing to wait for", took)
	}

	// The positive half first, so an empty capture cannot pass the absences below.
	if findLog(logs, "nothing to drain") == nil {
		t.Error("Stop did not recognize the connection as already closed")
	}
	if findLog(logs, "no reader subscriptions to release") == nil {
		t.Error("Stop did not skip the reader unsubscribes on a closed connection")
	}
	for _, msg := range []string{"Error draining", "Error unsubscribing"} {
		if rec := findLog(logs, msg); rec != nil {
			t.Errorf("a stop over an already-closed connection logged %q at %v", msg, rec["level"])
		}
	}
}

// A stop that finds the broker gone — a pod stopping while NATS rolls — takes Drain's
// other route: a connection that is reconnecting has nothing in flight, so Drain closes
// it SYNCHRONOUSLY, on the stop's own goroutine, and queues the ClosedHandler right
// there. closeRequested therefore has to be set before Drain is called; set after it,
// the handler can run first, read the close as unrequested, and fail liveness on a pod
// that is only leaving.
//
// Whether the handler actually wins that race is scheduling, so it is not what this
// asserts. A sync subscription's closed handler runs INSIDE the library's close, on
// the goroutine that called it, before the ClosedHandler is even queued — so it reads
// the flag at the one instant that matters, deterministically.
func TestStopWhileReconnectingMarksTheCloseRequestedBeforeClosing(t *testing.T) {
	logs := captureLogs(t)
	srv := startEmbeddedServer(t)
	nmgr := startedManager(t, srv, "drain-reconnecting", nil)
	nc := nmgr.Conn()
	if nmgr.closeRequested == nil {
		t.Fatal("the manager carries no closeRequested flag; nothing below would mean anything")
	}

	// 0 = the close never reached this subscription, 1 = flag unset, 2 = flag set.
	var atClose atomic.Int32
	//subconfirm:ok nothing is ever published to this subject: the subscription exists only
	// for its closed handler, so there is no delivery for an unregistered SUB to drop.
	sub, err := nc.SubscribeSync("drain.reconnecting.probe")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.SetClosedHandler(func(string) {
		if nmgr.closeRequested.Load() {
			atClose.Store(2)
		} else {
			atClose.Store(1)
		}
	})

	srv.Shutdown()
	srv.WaitForShutdown()
	waitFor(t, "the connection to start reconnecting", func() bool {
		return nc.Status() == nats.RECONNECTING
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := nmgr.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if findLog(logs, "reconnecting when the stop began") == nil {
		t.Fatal("Stop did not take the reconnecting route; the ordering below was not exercised")
	}
	switch atClose.Load() {
	case 0:
		t.Fatal("the close never ran the subscription's closed handler; the probe saw nothing")
	case 1:
		t.Error("the connection was closed before closeRequested was set: its ClosedHandler " +
			"can read the stop's own close as unrequested and fail liveness")
	}
	if !nc.IsClosed() {
		t.Errorf("the connection is %s after Stop, want CLOSED", nc.Status())
	}
	waitFor(t, "the shutdown-close log", func() bool {
		return findLog(logs, "closed during shutdown") != nil
	})
	if err := nmgr.Microservice.Live(); err != nil {
		t.Errorf("a stop during a reconnect marked the process not live: %v", err)
	}
}
