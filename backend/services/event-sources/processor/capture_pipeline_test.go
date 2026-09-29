// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// THE PIPELINED PUBLISH. The gateway source hands every decoded event to ONE ordered
// writer, which keeps up to CAPTURE_PUBLISH_WINDOW publishes awaiting their PubAck at once
// and reports each outcome to the message's settler. These tests drive the real source —
// its read loop, decode workers, submitter and settler, through Start and Stop — against
// heldWriter, which holds every outcome until the test releases it, so what is in flight
// and when a message is acked are observable exactly rather than by timing.

// heldWriter is an OrderedWriter that records every call and HOLDS every done until the
// test releases it. With window > 0, Publish blocks while window publishes are held, as the
// real writer does while its window is full.
type heldWriter struct {
	mu     sync.Mutex
	cond   *sync.Cond
	window int

	calls    []string // "publish", "draining", "close", in order
	dones    []func(error)
	msgs     []messaging.Message
	released []bool
	inFlight int
	maxIn    int
	// blocked counts the Publish calls waiting for room in a full window.
	blocked int
	busy    atomic.Bool
	closed  bool
	// misuse records a contract violation (overlapping submitters, a publish after
	// Close) instead of panicking on a goroutine the test does not own.
	misuse []string
}

func newHeldWriter(window int) *heldWriter {
	w := &heldWriter{window: window}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *heldWriter) Publish(_ context.Context, msg messaging.Message, done func(error)) {
	if !w.busy.CompareAndSwap(false, true) {
		w.mu.Lock()
		w.misuse = append(w.misuse, "overlapping Publish")
		w.mu.Unlock()
	} else {
		defer w.busy.Store(false)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.window > 0 && w.inFlight >= w.window {
		w.blocked++
		w.cond.Broadcast()
		w.cond.Wait()
		w.blocked--
	}
	if w.closed {
		w.misuse = append(w.misuse, "Publish after Close")
	}
	w.calls = append(w.calls, "publish")
	w.dones = append(w.dones, done)
	w.msgs = append(w.msgs, msg)
	w.released = append(w.released, false)
	w.inFlight++
	w.maxIn = max(w.maxIn, w.inFlight)
	w.cond.Broadcast()
}

func (w *heldWriter) Fail(err error, done func(error)) { done(err) }

func (w *heldWriter) Draining() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "draining")
}

// Close records itself and blocks until every held done has been released, as the real
// writer's Close returns only once every done has run.
func (w *heldWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "close")
	w.closed = true
	for w.inFlight > 0 {
		w.cond.Wait()
	}
}

// release runs the i-th held done with err, on the calling goroutine, and returns once
// it has returned.
func (w *heldWriter) release(i int, err error) {
	w.mu.Lock()
	done := w.dones[i]
	w.released[i] = true
	w.mu.Unlock()
	done(err)
	w.mu.Lock()
	w.inFlight--
	w.cond.Broadcast()
	w.mu.Unlock()
}

// message returns which captured message the i-th held publish carries: its index in
// the order the reader delivered them (the capture sequence less one). The decode workers
// hand publishes to the writer in whatever order they finish, not in delivery order.
func (w *heldWriter) message(t *testing.T, i int) int {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var seq int
	_, err := fmt.Sscanf(w.msgs[i].DedupID, "acme:%d", &seq)
	require.NoError(t, err, "held publish %d carries dedup id %q", i, w.msgs[i].DedupID)
	return seq - 1
}

// releaseAll releases every held done that has not been released, with nil, until
// nothing is held — including publishes that arrive while it runs.
func (w *heldWriter) releaseAll() {
	for {
		w.mu.Lock()
		next := -1
		for i, r := range w.released {
			if !r {
				next = i
				break
			}
		}
		w.mu.Unlock()
		if next < 0 {
			return
		}
		w.release(next, nil)
	}
}

// waitHeld blocks until n publishes have reached the writer.
func (w *heldWriter) waitHeld(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.dones) >= n
	}, 5*time.Second, time.Millisecond, "fewer than %d publishes reached the writer", n)
}

// waitBlocked blocks until n Publish calls are waiting for room in the window.
func (w *heldWriter) waitBlocked(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.blocked >= n
	}, 5*time.Second, time.Millisecond, "fewer than %d publishes are waiting for room in the window", n)
}

// releaseWithin is release bounded by a deadline, so a settle that blocks fails the test
// by name instead of hanging it until go test's own timeout.
func (w *heldWriter) releaseWithin(t *testing.T, i int, err error, limit time.Duration) {
	t.Helper()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		w.release(i, err)
	}()
	select {
	case <-returned:
	case <-time.After(limit):
		t.Fatalf("the settle of held publish %d did not return within %v: an outcome blocked the writer's goroutine", i, limit)
	}
}

// stopUntilDone stops the source, releasing whatever the writer holds until the stop
// returns, and fails the test if it does not.
func (pc *pipelineCase) stopUntilDone(t *testing.T, stopped <-chan struct{}) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		pc.writer.releaseAll()
		select {
		case <-stopped:
			return
		case <-deadline:
			t.Fatal("the source did not stop")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (w *heldWriter) snapshot() (calls []string, maxIn int, misuse []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...), w.maxIn, append([]string(nil), w.misuse...)
}

// sliceReader hands out its messages in order, counting every one taken, then blocks
// until its context is cancelled.
type sliceReader struct {
	mu    sync.Mutex
	msgs  []messaging.Message
	taken int
}

func (r *sliceReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.mu.Lock()
	if len(r.msgs) > 0 {
		m := r.msgs[0]
		r.msgs = r.msgs[1:]
		r.taken++
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}

func (r *sliceReader) HandleResponse(error) {}

func (r *sliceReader) takenCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.taken
}

// pipelineCase is a started gateway source over a sliceReader and a heldWriter.
type pipelineCase struct {
	source *GatewayJetStreamSource
	writer *heldWriter
	reader *sliceReader
	acks   []*recordingAck

	mu          sync.Mutex
	failedCalls int
}

// startPipelineCase starts a real source, through its lifecycle, over n captured messages
// delivered numDelivered times each. failed, if non-nil, is the failed-decode route.
func startPipelineCase(t *testing.T, w *heldWriter, n, numDelivered int, failed func() error) *pipelineCase {
	t.Helper()
	return startPipelineCaseWith(t, w, n, numDelivered, failed, nil)
}

// startPipelineCaseWith is startPipelineCase with the message builder replaced, when build
// is non-nil.
func startPipelineCaseWith(t *testing.T, w *heldWriter, n, numDelivered int, failed func() error,
	build InboundMessageFunc) *pipelineCase {
	t.Helper()
	if build == nil {
		build = func(_ string, tenant string, event *model.UnresolvedEvent, _ interface{}, seq uint64) (context.Context, messaging.Message, bool) {
			return core.WithTenant(context.Background(), tenant),
				messaging.Message{Value: []byte(event.Device), DedupID: DedupID(tenant, seq)}, true
		}
	}
	pc := &pipelineCase{writer: w, reader: &sliceReader{}}
	for i := 0; i < n; i++ {
		ack := &recordingAck{}
		pc.acks = append(pc.acks, ack)
		body := fmt.Sprintf(`{"device":"sensor-001","eventType":"Measurement",`+
			`"payload":{"entries":[{"measurements":{"t":"%d"}}]}}`, i)
		pc.reader.msgs = append(pc.reader.msgs, capturedMsg(captureSubject, body, numDelivered, uint64(i+1), ack))
	}
	pc.source = NewGatewayJetStreamSource(nil, "gw-pipeline", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		build,
		func(string, string, []byte, error) error {
			pc.mu.Lock()
			pc.failedCalls++
			pc.mu.Unlock()
			if failed != nil {
				return failed()
			}
			return nil
		},
		nil)
	ctx := context.Background()
	require.NoError(t, pc.source.Initialize(ctx))
	pc.source.SetReader(pc.reader)
	pc.source.SetWriter(w)
	require.NoError(t, pc.source.Start(ctx))
	// Whatever a test leaves held is released, or Stop (and so the test) would never end.
	t.Cleanup(func() {
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			_ = pc.source.Stop(context.Background())
		}()
		deadline := time.After(10 * time.Second)
		for {
			w.releaseAll()
			select {
			case <-stopped:
				return
			case <-deadline:
				t.Error("the source did not stop")
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	return pc
}

func (pc *pipelineCase) acked() []int {
	out := make([]int, len(pc.acks))
	for i, a := range pc.acks {
		out[i] = a.counts()
	}
	return out
}

func (pc *pipelineCase) failed() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.failedCalls
}

// THE DEFECT: each decode worker published and waited for the PubAck before taking the
// next message, so a pod kept at most DECODE_WORKER_COUNT publishes in flight — on a
// replicated stream, five quorum commits at a time. The source now keeps every message
// the writer's window has room for in flight, however few workers decoded them.
func TestGatewayKeepsMorePublishesInFlightThanItHasWorkers(t *testing.T) {
	const n = 20
	w := newHeldWriter(0)
	startPipelineCase(t, w, n, 1, nil)

	w.waitHeld(t, n)
	_, maxIn, misuse := w.snapshot()
	require.Equal(t, n, maxIn,
		"publishes held in flight at once: at most one per decode worker (%d) means each worker "+
			"still waits for its own PubAck", DECODE_WORKER_COUNT)
	require.Empty(t, misuse)
}

// Pipelining must not move the ack: a captured message is acked only once the broker has
// acknowledged the event it carried. Acking at hand-off would be the very loss the capture
// stream exists to remove.
func TestACapturedMessageIsAckedOnlyAfterItsPublishIsAcknowledged(t *testing.T) {
	w := newHeldWriter(0)
	pc := startPipelineCase(t, w, 1, 1, nil)

	w.waitHeld(t, 1)
	require.Equal(t, []int{0}, pc.acked(), "acked while its publish was still awaiting the PubAck")

	w.release(0, nil)
	require.Equal(t, []int{1}, pc.acked(), "not acked once its publish was acknowledged")
}

// A failed publish leaves its message unacked, for AckWait-paced redelivery.
func TestAHeldPublishThatFailsLeavesItsMessageUnacked(t *testing.T) {
	w := newHeldWriter(0)
	pc := startPipelineCase(t, w, 1, 1, nil)

	w.waitHeld(t, 1)
	w.release(0, errors.New("jetstream unavailable"))
	// release returns after the settler has run, so this is read after the decision.
	require.Equal(t, []int{0}, pc.acked(), "a message whose publish FAILED was acked: it is lost")
	require.Zero(t, pc.failed(), "a first-delivery publish failure is not poison")
}

func TestStartingWithoutAnInboundWriterFailsLoudly(t *testing.T) {
	source := NewGatewayJetStreamSource(nil, "gw-no-writer", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) (context.Context, messaging.Message, bool) {
			return nil, messaging.Message{}, false
		},
		func(string, string, []byte, error) error { return nil },
		nil)
	ctx := context.Background()
	require.NoError(t, source.Initialize(ctx))
	source.SetReader(&sliceReader{})

	err := source.Start(ctx)

	require.Error(t, err, "a source with no writer started; its first publish would panic a worker")
	require.Contains(t, err.Error(), "without an inbound-events writer")
}

// Stop settles everything it took, in the order the writer's contract requires: Draining
// first, every message the source holds handed to the writer, and only then Close — which
// the stop waits on, so it returns after every outcome. The window here is smaller than
// what the source holds, so messages are still queued behind the submitter when Stop
// begins. Whether Close waits for the submitter is pinned exactly, rather than by the
// timing of these releases, by TestStopClosesTheWriterOnlyAfterTheSubmitterReturns.
func TestStopDrainsThenSettlesEveryPublishBeforeClosing(t *testing.T) {
	const n, window = 10, 4
	w := newHeldWriter(window)
	pc := startPipelineCase(t, w, n, 1, nil)
	w.waitHeld(t, window)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = pc.source.Stop(context.Background())
	}()
	require.Eventually(t, func() bool {
		calls, _, _ := w.snapshot()
		return len(calls) > 0 && calls[len(calls)-1] == "draining"
	}, 2*time.Second, time.Millisecond, "the stop never told the writer it was draining")
	select {
	case <-stopped:
		t.Fatal("Stop returned while publishes were still held")
	case <-time.After(50 * time.Millisecond):
	}

	// Release everything as it arrives; the source hands over the rest as room frees.
	for released := 0; released < n; released++ {
		w.waitHeld(t, released+1)
		w.release(released, nil)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return once every publish had settled")
	}

	calls, _, misuse := w.snapshot()
	require.Empty(t, misuse, "the writer's contract was broken: a publish after Close, or two submitters")
	require.Equal(t, n, countOf(calls, "publish"), "every message the source held must be published")
	require.Equal(t, 1, countOf(calls, "close"))
	require.Equal(t, "close", calls[len(calls)-1], "Close must come after every publish: %v", calls)
	require.Equal(t, "draining", calls[window],
		"the writer must be told it is draining as soon as the stop begins: %v", calls)
	ones := make([]int, n)
	for i := range ones {
		ones[i] = 1
	}
	require.Equal(t, ones, pc.acked(), "every message the stop settled must be acked")
}

func countOf(calls []string, what string) int {
	n := 0
	for _, c := range calls {
		if c == what {
			n++
		}
	}
	return n
}

// THE BOUND ON WHAT THE SOURCE HOLDS. Messages taken from the stream and not yet settled
// are held while the broker's AckWait clock runs on them. While the writer's window is full
// — during a failure episode the writer admits one publish per backoff — the source must
// stop taking: the read loop one message, the decode queue, one per worker, the one the
// submitter is placing, and the window, and nothing more.
func TestTheSourceStopsTakingMessagesWhileTheWindowIsFull(t *testing.T) {
	const window = 8
	w := newHeldWriter(window)
	pc := startPipelineCase(t, w, 500, 1, nil)
	w.waitHeld(t, window)

	want := 1 + DECODE_CHANNEL_DEPTH + DECODE_WORKER_COUNT + 1 + window
	require.Eventually(t, func() bool { return pc.reader.takenCount() >= want },
		5*time.Second, time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, want, pc.reader.takenCount(),
		"messages taken from the stream while the window was full")
}

// Routing a poison message to failed-decode is a synchronous publish of up to 5 s. It must
// not run on the writer's settle goroutine, which reports every other outcome behind it.
func TestAPoisonRouteDoesNotHoldTheWriterGoroutine(t *testing.T) {
	unblock := make(chan struct{})
	entered := make(chan struct{}, 1)
	w := newHeldWriter(0)
	pc := startPipelineCase(t, w, 1, messaging.MaxDeliver, func() error {
		entered <- struct{}{}
		<-unblock
		return nil
	})
	w.waitHeld(t, 1)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		w.release(0, errors.New("permanently unpublishable"))
	}()
	<-entered
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(unblock)
		t.Fatal("the settle returned only after the failed-decode route did: the route holds the writer's goroutine")
	}
	require.Equal(t, []int{0}, pc.acked(), "acked before the route had stored it")

	close(unblock)
	pc.source.poison.running.Wait()
	require.Equal(t, []int{1}, pc.acked(), "a routed poison message must be acked")
	require.Equal(t, 1, pc.failed())
}

// At most POISON_ROUTE_LIMIT routes run at once. One beyond that is left UNACKED — this is
// its last delivery, so the broker terminates it and the max-delivery recorder letters it —
// and is NOT routed.
func TestPoisonRoutesBeyondTheLimitAreLeftUnacked(t *testing.T) {
	const n = POISON_ROUTE_LIMIT + 1
	unblock := make(chan struct{})
	var entered atomic.Int32
	w := newHeldWriter(0)
	pc := startPipelineCase(t, w, n, messaging.MaxDeliver, func() error {
		entered.Add(1)
		<-unblock
		return nil
	})
	w.waitHeld(t, n)

	// Every route is held open, so a settle that WAITED for a free slot would never
	// return: each release is bounded, and that reads as a failure, not a hang.
	t.Cleanup(func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})
	for i := 0; i < POISON_ROUTE_LIMIT; i++ {
		w.releaseWithin(t, i, errors.New("permanently unpublishable"), 2*time.Second)
	}
	require.Eventually(t, func() bool { return entered.Load() == POISON_ROUTE_LIMIT },
		2*time.Second, time.Millisecond, "the routes within the limit never started")
	w.releaseWithin(t, POISON_ROUTE_LIMIT, errors.New("permanently unpublishable"), 2*time.Second)

	close(unblock)
	pc.source.poison.running.Wait()
	want := make([]int, n)
	for i := 0; i < n; i++ {
		want[i] = 1
	}
	want[w.message(t, POISON_ROUTE_LIMIT)] = 0
	require.Equal(t, want, pc.acked(), "the routed messages are acked; the one beyond the limit is not")
	require.Equal(t, POISON_ROUTE_LIMIT, pc.failed(), "the message beyond the limit must not be routed")
}

// Stop waits for the poison routes its outcomes started, so a route is never cut off by
// the NATS drain that follows the stop — which would leave a message that reached
// failed-decode unacked, to be lettered as well.
func TestStopWaitsForPoisonRoutes(t *testing.T) {
	unblock := make(chan struct{})
	entered := make(chan struct{}, 1)
	w := newHeldWriter(0)
	pc := startPipelineCase(t, w, 1, messaging.MaxDeliver, func() error {
		entered <- struct{}{}
		<-unblock
		return nil
	})
	w.waitHeld(t, 1)
	w.release(0, errors.New("permanently unpublishable"))
	<-entered

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = pc.source.Stop(context.Background())
	}()
	select {
	case <-stopped:
		close(unblock)
		t.Fatal("Stop returned while a failed-decode route was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(unblock)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return once the route finished")
	}
	require.Equal(t, []int{1}, pc.acked(), "the route's ack must land before Stop returns")
}

// Close only once the submitter has returned: the writer's contract forbids Close while a
// submission is in progress, and the real writer panics on it. The state is built exactly
// rather than raced for: the window is full, the submitter is INSIDE Publish waiting for
// room with the last message, and every decode worker has handed its message over — so the
// moment the stop sees the workers finish, the one thing between it and Close is the wait
// for the submitter. Nothing is released until that moment has passed.
func TestStopClosesTheWriterOnlyAfterTheSubmitterReturns(t *testing.T) {
	const window = 4
	w := newHeldWriter(window)
	pc := startPipelineCase(t, w, window+1, 1, nil)
	w.waitHeld(t, window)
	w.waitBlocked(t, 1)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = pc.source.Stop(context.Background())
	}()
	select {
	case <-pc.source.workersDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the decode workers did not return once the stop began")
	}
	time.Sleep(200 * time.Millisecond)
	calls, _, _ := w.snapshot()
	require.NotContains(t, calls, "close",
		"the writer was closed while the submitter was still inside Publish: %v", calls)

	pc.stopUntilDone(t, stopped)
	calls, _, misuse := w.snapshot()
	require.Empty(t, misuse, "the writer's contract was broken: %v", calls)
	require.Equal(t, window+1, countOf(calls, "publish"))
	require.Equal(t, "close", calls[len(calls)-1], "Close must come after every publish: %v", calls)
}

// The writer is told it is draining BEFORE the stop waits for the read loop. With the
// pipeline full, the read loop is blocked handing its last message in, and room frees only
// as publishes settle — one backoff at a time while a failing stream holds the writer in
// backoff. Draining is what ends that backoff, so it cannot wait behind the loop it frees.
func TestStopTellsTheWriterItIsDrainingWhileTheReadLoopIsBlocked(t *testing.T) {
	const window = 8
	w := newHeldWriter(window)
	pc := startPipelineCase(t, w, 500, 1, nil)
	w.waitHeld(t, window)
	full := 1 + DECODE_CHANNEL_DEPTH + DECODE_WORKER_COUNT + 1 + window
	require.Eventually(t, func() bool { return pc.reader.takenCount() >= full },
		5*time.Second, time.Millisecond, "the pipeline never filled")

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = pc.source.Stop(context.Background())
	}()
	// Nothing is released: the read loop stays blocked until the draining call lands.
	require.Eventually(t, func() bool {
		calls, _, _ := w.snapshot()
		return countOf(calls, "draining") > 0
	}, 2*time.Second, time.Millisecond,
		"the stop did not tell the writer it was draining while the read loop was blocked on a full pipeline")
	select {
	case <-pc.source.drained:
		t.Fatal("the read loop returned with nothing released; this test did not hold it blocked")
	default:
	}
	pc.stopUntilDone(t, stopped)
}

// A source that was never started — the shutdown after a startup failure that came before
// its reader and writer were wired — stops cleanly, without touching the writer it never got.
func TestStoppingASourceThatNeverStartedDoesNothing(t *testing.T) {
	source := NewGatewayJetStreamSource(nil, "gw-never-started", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) (context.Context, messaging.Message, bool) {
			return nil, messaging.Message{}, false
		},
		func(string, string, []byte, error) error { return nil },
		nil)
	ctx := context.Background()
	require.NoError(t, source.Initialize(ctx))

	require.NotPanics(t, func() { require.NoError(t, source.Stop(ctx)) })
}

// A message the builder deliberately drops (ok=false) is settled nil — ACKED — and not
// published: the ACK TRAP rule, on the pipelined path. Settling it with an error would ask
// the broker to redeliver a message that will be dropped identically every time.
func TestADeliberateDropOnThePipelinedPathIsAcked(t *testing.T) {
	w := newHeldWriter(0)
	pc := startPipelineCaseWith(t, w, 1, 1, nil,
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) (context.Context, messaging.Message, bool) {
			return nil, messaging.Message{}, false
		})

	require.True(t, pc.acks[0].settled(t), "a deliberately dropped message was left unacked: it will redeliver forever")
	calls, _, _ := w.snapshot()
	require.Zero(t, countOf(calls, "publish"), "a dropped message must not be published")
}
