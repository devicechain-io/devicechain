// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// testFetchSettings is what every test manager in this package is built with. Unset it is
// the production default (one pull at a time). DC_TEST_FETCH_AHEAD=1 turns fetch-ahead on
// at the default batch for EVERY reader the suite builds, which is how the read-contact and
// retry-contract suites are re-run with the mechanism engaged:
//
//	DC_TEST_FETCH_AHEAD=1 go test -count=1 -p 2 ./messaging/
func testFetchSettings() config.NatsFetchConfiguration {
	if os.Getenv("DC_TEST_FETCH_AHEAD") == "1" {
		return config.NatsFetchConfiguration{Batch: fetchBatch, Ahead: config.AheadFlag(true)}
	}
	return config.NatsFetchConfiguration{}
}

// aheadManager is a test manager whose plain readers fetch ahead at the given batch, with
// the given AckWait (0 keeps the production one).
func aheadManager(t *testing.T, batch int, ackWait time.Duration) *NatsManager {
	t.Helper()
	nmgr, cleanup := newTestManager(t)
	t.Cleanup(cleanup)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch =
		config.NatsFetchConfiguration{Batch: batch, Ahead: config.AheadFlag(true)}
	if ackWait > 0 {
		nmgr.SetAckWaitForTesting(t, ackWait)
	}
	return nmgr
}

func newAheadReader(t *testing.T, nmgr *NatsManager, opts ...ReaderOption) *natsReader {
	t.Helper()
	rd, err := nmgr.NewReader(streams.InboundEvents, opts...)
	require.NoError(t, err)
	return rd.(*natsReader)
}

// publishAsync puts n messages on the inbound subject quickly, waiting for the broker to
// have stored them all.
func publishAsync(t *testing.T, nmgr *NatsManager, n int) {
	t.Helper()
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.InboundEvents)
	for i := 0; i < n; i++ {
		for {
			_, err := nmgr.js.PublishAsync(subject, []byte(`{"telemetry":true}`))
			if errors.Is(err, nats.ErrTooManyStalledMsgs) {
				time.Sleep(5 * time.Millisecond) // the client's pending window is full; let the broker catch up
				continue
			}
			require.NoError(t, err)
			break
		}
	}
	select {
	case <-nmgr.js.PublishAsyncComplete():
	case <-time.After(30 * time.Second):
		t.Fatal("the broker did not store the published messages in time")
	}
}

func read(t *testing.T, r *natsReader, d time.Duration) Message {
	t.Helper()
	msg, err := readWithin(t, r, d)
	require.NoError(t, err)
	return msg
}

// 🔴 ORDER. With a request always in flight and a consumer that stalls now and then, the
// stream sequences still come out strictly increasing and contiguous: one request at a time,
// consumed in the order it was issued. A second request in flight is what would break it.
func TestFetchAheadKeepsTheDeliveryOrder(t *testing.T) {
	const total = 10000
	nmgr := aheadManager(t, 128, 0)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, total)

	rng := rand.New(rand.NewSource(1))
	var last uint64
	for i := 0; i < total; i++ {
		msg := read(t, r, 30*time.Second)
		require.Greater(t, msg.StreamSeq, last, "message %d arrived out of order", i)
		require.Equal(t, last+1, msg.StreamSeq, "a sequence went missing before message %d", i)
		last = msg.StreamSeq
		require.NoError(t, msg.Ack())
		if rng.Intn(100) == 0 {
			time.Sleep(time.Duration(rng.Intn(3)) * time.Millisecond)
		}
	}
	require.Greater(t, r.aheadStarts.Load(), int64(total/128/2),
		"fetch-ahead never engaged, so the order above says nothing about it")
}

// A reconnect in the middle of the run: what the dropped request had in flight is lost to
// the reader (the broker redelivers it after AckWait), so the first deliveries may skip a
// range, but never wider than a batch and never backwards, and the redelivery fills it.
func TestFetchAheadAcrossABrokerRestartSkipsAtMostABatchAndLosesNothing(t *testing.T) {
	const total, batch = 3000, 64
	srv, storeDir, nmgr := singleBroker(t)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch =
		config.NatsFetchConfiguration{Batch: batch, Ahead: config.AheadFlag(true)}
	nmgr.SetAckWaitForTesting(t, 3*time.Second)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, total)

	seen := map[uint64]bool{}
	var firstLast uint64
	restarted := false
	deadline := time.Now().Add(90 * time.Second)
	for len(seen) < total && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		msg, err := r.ReadMessage(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, io.EOF) {
				continue // an idle second; the redelivery has not come yet
			}
			continue // the outage's read errors
		}
		if msg.NumDelivered == 1 {
			require.Greater(t, msg.StreamSeq, firstLast, "a first delivery went backwards")
			// A pull the connection dropped can lose a whole batch the broker counted as
			// delivered (none of it reached the client), so the range skipped is at most
			// one batch wide, and one request at a time means at most one such range.
			require.LessOrEqual(t, msg.StreamSeq-firstLast-1, uint64(batch),
				"first deliveries skipped %d sequences; a lost pull can lose at most one batch", msg.StreamSeq-firstLast-1)
			firstLast = msg.StreamSeq
		}
		seen[msg.StreamSeq] = true
		require.NoError(t, msg.Ack())
		if !restarted && len(seen) >= total/3 {
			restarted = true
			port := srv.Addr().(*net.TCPAddr).Port
			srv.Shutdown()
			srv.WaitForShutdown()
			time.Sleep(500 * time.Millisecond)
			srv = startBrokerKeepingStore(t, port, storeDir)
			t.Cleanup(srv.Shutdown)
			waitFor(t, "the client to reconnect", nmgr.nc.IsConnected)
		}
	}
	require.True(t, restarted)
	require.Len(t, seen, total, "messages were lost across the restart")
}

// 🔴 THE AGE BOUND. A consumer that takes longer than the hold budget to work through a
// batch stops fetching ahead by itself: nothing it has not started on sits waiting against
// the acknowledgement window. A fast one keeps a request in flight.
func TestFetchAheadStopsWhenTheConsumerIsSlowerThanTheBudget(t *testing.T) {
	// AckWait 3s => the default budget is a tenth of it, 300 ms.
	run := func(t *testing.T, perMessage time.Duration) (starts int64, redelivered int) {
		nmgr := aheadManager(t, 4, 3*time.Second)
		r := newAheadReader(t, nmgr)
		require.Equal(t, 300*time.Millisecond, r.aheadBudget)
		publishAsync(t, nmgr, 24)
		for i := 0; i < 24; i++ {
			msg := read(t, r, 15*time.Second)
			if msg.NumDelivered > 1 {
				redelivered++
			}
			time.Sleep(perMessage)
			require.NoError(t, msg.Ack())
		}
		return r.aheadStarts.Load(), redelivered
	}
	t.Run("slow", func(t *testing.T) {
		starts, redelivered := run(t, 100*time.Millisecond) // a batch of 4 takes 400 ms > 300 ms
		require.LessOrEqual(t, starts, int64(1), "a consumer slower than the budget kept fetching ahead")
		require.Zero(t, redelivered)
	})
	t.Run("fast", func(t *testing.T) {
		starts, redelivered := run(t, 0)
		require.GreaterOrEqual(t, starts, int64(3), "a fast consumer did not fetch ahead")
		require.Zero(t, redelivered)
	})
}

// A prefetched batch that has waited longer than half the acknowledgement window is not
// handed out: it is about to be redelivered, and handing it out now would run the work twice.
func TestFetchAheadDropsAResultThatSatTooLong(t *testing.T) {
	nmgr := aheadManager(t, 4, 3*time.Second)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 12)
	first := read(t, r, 10*time.Second)
	require.NotNil(t, r.future, "a request should be in flight after a non-empty batch")
	require.NoError(t, first.Ack())
	// Let the in-flight batch age past half the window while the consumer sits on the first.
	waitFor(t, "the in-flight batch to arrive", func() bool { return len(r.future.done) == 1 })
	time.Sleep(1700 * time.Millisecond)
	startsBefore := r.aheadStarts.Load()
	// Drain the first batch, then the next read must refuse the aged one and fetch fresh.
	for i := 0; i < 3; i++ {
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	msg := read(t, r, 10*time.Second)
	require.Greater(t, msg.StreamSeq, uint64(8), "the aged prefetched batch (sequences 5-8) was handed out")
	require.NoError(t, msg.Ack())
	require.Equal(t, startsBefore, r.aheadStarts.Load())
}

// 🔴 SHUTDOWN. A request in flight when the reader is stopped ends by itself, and what it
// had delivered is not lost: the broker redelivers it to whoever reads next.
func TestFetchAheadShutdownLeaksNoGoroutineAndLosesNothing(t *testing.T) {
	nmgr := aheadManager(t, 8, 2*time.Second)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 40)
	first := read(t, r, 10*time.Second)
	require.NoError(t, first.Ack())
	require.NotNil(t, r.future)
	require.Equal(t, int64(1), r.aheadStarts.Load(), "exactly one request should have been started")

	// The service stops: the caller's context ends (EOF), and the manager releases the
	// subscription, exactly as ExecuteStop does.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, r.sub.Load().Unsubscribe())
	waitFor(t, "the request in flight to end", func() bool { return r.aheadLive.Load() == 0 })

	// A replacement reader on the same durable gets everything that was not acked, the
	// dropped batches included, once their window runs out.
	other := newAheadReader(t, nmgr)
	got := map[uint64]bool{first.StreamSeq: true}
	for len(got) < 40 {
		msg, err := readWithin(t, other, 15*time.Second)
		require.NoError(t, err, "only %d of 40 messages came back after the restart", len(got))
		got[msg.StreamSeq] = true
		require.NoError(t, msg.Ack())
	}
}

// A reader that gives up its buffer when its term ends (ReaderWithReleaseOnPark) gives up
// the request in flight too, and the redelivery is immediate rather than a window away.
func TestFetchAheadReleaseOnParkNaksWhatTheRequestInFlightDelivered(t *testing.T) {
	nmgr := aheadManager(t, 8, 30*time.Second)
	var held atomic.Bool
	held.Store(true)
	r := newAheadReader(t, nmgr, ReaderWithTermGate(held.Load), ReaderWithReleaseOnPark())
	publishAsync(t, nmgr, 20)
	first := read(t, r, 10*time.Second)
	require.NoError(t, first.Ack())
	require.NotNil(t, r.future)
	waitFor(t, "the in-flight batch to arrive", func() bool { return len(r.future.done) == 1 })

	held.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	_, err := r.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.Nil(t, r.future, "the park left a request in flight")

	// AckWait is 30 s; a message back within a few seconds was Nak'd, not timed out.
	other := newAheadReader(t, nmgr)
	got := map[uint64]bool{first.StreamSeq: true}
	for len(got) < 20 {
		msg, err := readWithin(t, other, 5*time.Second)
		require.NoError(t, err, "only %d of 20 messages came back promptly", len(got))
		got[msg.StreamSeq] = true
		require.NoError(t, msg.Ack())
	}
}

// 🔴 THE TERM GATE. No request is started while the term is not held, and a new term drops
// the one in flight.
func TestFetchAheadStartsNothingWhileTheTermIsNotHeld(t *testing.T) {
	nmgr := aheadManager(t, 8, 0)
	// The gate is consulted by the loop before a fetch (1), again once the batch is in hand
	// (2), and last by the decision to fetch ahead (3): the term ends between (2) and (3).
	var calls atomic.Int32
	held := func() bool { return calls.Add(1) <= 2 }
	r := newAheadReader(t, nmgr, ReaderWithTermGate(held))
	publishAsync(t, nmgr, 20)
	msg := read(t, r, 10*time.Second)
	require.NoError(t, msg.Ack())
	require.Zero(t, r.aheadStarts.Load(), "a request was started although the term was no longer held")
	require.Nil(t, r.future)
}

func TestFetchAheadIsDroppedByANewTerm(t *testing.T) {
	nmgr := aheadManager(t, 8, 0)
	var held atomic.Bool
	held.Store(true)
	r := newAheadReader(t, nmgr, ReaderWithTermGate(held.Load))
	publishAsync(t, nmgr, 20)
	require.NoError(t, read(t, r, 10*time.Second).Ack())
	require.NotNil(t, r.future, "the test needs a request in flight to show it is dropped")
	require.NoError(t, r.UnbindTerm())
	require.NoError(t, r.BindTerm())
	require.Nil(t, r.future)
	require.Empty(t, r.pending)
	waitFor(t, "the dropped request to end", func() bool { return r.aheadLive.Load() == 0 })
}

// 🔴 BY GENERATION. A request pulled on a subscription that has since been replaced is
// stale whatever it delivered: its messages are dropped unacked and the next fetch runs on
// the new subscription.
func TestFetchAheadResultOfAReplacedSubscriptionIsDiscarded(t *testing.T) {
	nmgr := aheadManager(t, 8, 3*time.Second)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 40)
	require.NoError(t, read(t, r, 10*time.Second).Ack())
	require.NotNil(t, r.future)
	waitFor(t, "the in-flight batch to arrive", func() bool { return len(r.future.done) == 1 })

	// A re-bind that does not go through dropPending, as a self-heal does.
	require.NoError(t, r.bind())
	for i := 0; i < 7; i++ { // the rest of the batch in hand
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	msg := read(t, r, 10*time.Second)
	require.Greater(t, msg.StreamSeq, uint64(16),
		"the replaced subscription's batch (sequences 9-16) was handed out")
	require.NoError(t, msg.Ack())
}

// The consumer deleted under a request in flight: the error the request carries takes the
// same path a synchronous fetch's does, and the reader re-binds and goes on delivering.
func TestFetchAheadReBindsWhenTheConsumerIsDeletedUnderIt(t *testing.T) {
	nmgr := aheadManager(t, 4, 0)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 12)
	for i := 0; i < 4; i++ { // the first batch, and a request in flight for the second
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	require.NoError(t, nmgr.js.DeleteConsumer(r.stream, r.durable))
	oldSub := r.sub.Load()
	// The recreated durable replays the stream (DeliverAll): messages keep coming.
	deadline := time.Now().Add(30 * time.Second)
	for r.sub.Load() == oldSub && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		msg, err := r.ReadMessage(ctx)
		cancel()
		if err == nil {
			_ = msg.Ack()
		}
	}
	require.NotSame(t, oldSub, r.sub.Load(), "the reader never re-bound")
	require.NoError(t, read(t, r, 15*time.Second).Ack())
}

// A result that is a partial batch is handed out as it is, with no error, as a synchronous
// fetch's is; the next batch is then asked for like any other.
func TestFetchAheadHandsOutAPartialBatchWithoutAnError(t *testing.T) {
	nmgr := aheadManager(t, 64, 0)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 5) // a batch of 64 is asked for; 5 exist
	for i := 0; i < 5; i++ {
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	publishAsync(t, nmgr, 3)
	for i := 0; i < 3; i++ {
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
}

// The error a request in flight ends with is not a reason to start another: after an empty
// result the reader fetches synchronously, as it always has.
func TestFetchAheadStartsNothingAfterAnEmptyResult(t *testing.T) {
	nmgr := aheadManager(t, 8, 0)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 8)
	for i := 0; i < 8; i++ {
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	// The request in flight finds nothing and times out; that must not chain another.
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	_, err := r.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.Nil(t, r.future)
	require.Equal(t, int64(1), r.aheadStarts.Load())
}

// A capacity reader ignores fetch-ahead: it asks for its free slots and no more.
func TestACapacityReaderNeverFetchesAhead(t *testing.T) {
	nmgr := aheadManager(t, 8, 0)
	r := newAheadReader(t, nmgr, ReaderWithCapacity(4))
	require.False(t, r.ahead)
	publishAsync(t, nmgr, 8)
	for i := 0; i < 8; i++ {
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	require.Zero(t, r.aheadStarts.Load())
}

// A configuration nothing defaulted (a manager built by hand) fetches one batch at a time, at
// the default batch size. The ON default is applied where configuration is LOADED
// (InstanceConfiguration.ApplyDefaults), and is pinned there and by the load test in core.
func TestAnUndefaultedFetchConfigurationPullsOneBatchAtATime(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch = config.NatsFetchConfiguration{}
	r := newAheadReader(t, nmgr)
	require.False(t, r.ahead)
	require.Equal(t, config.DefaultFetchBatch, r.fetchSize)
}

// A configuration that went through ApplyDefaults, as every service's does, fetches ahead at a
// batch of 128 — and a capacity reader still never does.
func TestADefaultedFetchConfigurationFetchesAheadAtTheDefaultBatch(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	cfg := &config.InstanceConfiguration{}
	cfg.ApplyDefaults()
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch = cfg.Infrastructure.Nats.Fetch
	r := newAheadReader(t, nmgr)
	require.True(t, r.ahead, "a defaulted configuration did not fetch ahead")
	require.Equal(t, 128, r.fetchSize)
	require.False(t, newAheadReader(t, nmgr, ReaderWithCapacity(4)).ahead)
}

// An explicit hold budget over a tenth of the window is refused when the manager starts.
func TestAnOverlongHoldBudgetIsRefusedAtStartup(t *testing.T) {
	srv := startBrokerKeepingStore(t, -1, t.TempDir())
	t.Cleanup(srv.Shutdown)
	nmgr := managerFor(t, srv)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch =
		config.NatsFetchConfiguration{Batch: 64, Ahead: config.AheadFlag(true), AheadHoldBudgetMillis: int(AckWait/10/time.Millisecond) + 1}
	err := nmgr.ExecuteInitialize(context.Background())
	require.ErrorContains(t, err, "aheadHoldBudgetMillis")
}

// The widest pull the configuration allows leaves a range the cheap path reads: one request
// per sequence at exactly rangeDirectMax, and the consumer-backed read only above it.
func TestTheLargestConfiguredBatchLeavesARangeTheDirectPathReads(t *testing.T) {
	nmgr := aheadManager(t, config.MaxFetchBatch, 0)
	r := newAheadReader(t, nmgr)
	_ = r
	publishAsync(t, nmgr, rangeDirectMax+2)

	narrow, err := nmgr.NewRangeReader(streams.InboundEvents, 2, 1+rangeDirectMax)
	require.NoError(t, err)
	require.IsType(t, &seqRangeReader{}, narrow, "a full batch's range must take the direct path")
	wide, err := nmgr.NewRangeReader(streams.InboundEvents, 1, 1+rangeDirectMax)
	require.NoError(t, err)
	require.IsType(t, &consumerRangeReader{}, wide)
	_ = narrow.Close()
	_ = wide.Close()
}

// The broker must never see two of this reader's pull requests waiting at once,
// whatever the interleaving of idle reads (EOFs with a request in flight), trickled
// publishes and fresh batches. Sampled from the broker's own NumWaiting.
func TestFetchAheadNeverHasTwoPullRequestsWaiting(t *testing.T) {
	nmgr := aheadManager(t, 8, 0)
	r := newAheadReader(t, nmgr)
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.InboundEvents)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var maxWaiting, sawWaiting atomic.Int64
	wg.Add(2)
	go func() { // sampler
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if ci, err := nmgr.js.ConsumerInfo(r.stream, r.durable); err == nil {
				n := int64(ci.NumWaiting)
				if n > 0 {
					sawWaiting.Store(1)
				}
				for {
					m := maxWaiting.Load()
					if n <= m || maxWaiting.CompareAndSwap(m, n) {
						break
					}
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	go func() { // trickle: a few messages, then idle long enough for a request to wait
		defer wg.Done()
		for i := 0; i < 12; i++ {
			select {
			case <-stop:
				return
			default:
			}
			for j := 0; j < 10; j++ {
				_, _ = nmgr.js.Publish(subject, []byte(`{"telemetry":true}`))
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()

	deadline := time.Now().Add(4 * time.Second)
	got := 0
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		msg, err := r.ReadMessage(ctx)
		cancel()
		if err == nil {
			got++
			_ = msg.Ack()
		}
	}
	close(stop)
	wg.Wait()
	require.Positive(t, got)
	require.Positive(t, r.aheadStarts.Load(), "fetch-ahead never engaged")
	require.Equal(t, int64(1), sawWaiting.Load(), "the sampler never saw a waiting request, so it proves nothing")
	require.LessOrEqual(t, maxWaiting.Load(), int64(1), "the broker saw two pull requests waiting for one reader")
}

// 🔴 A forwarding reader whose downstream is applying backpressure starts no request: it
// would fetch messages it can only fail to publish, which the loop itself refuses to do
// before a synchronous fetch. The gate answers open to the loop's own check and refuses to
// the next one, so the only thing that can stop the request is the check startAhead makes.
func TestFetchAheadStartsNothingWhileTheDownstreamRefuses(t *testing.T) {
	run := func(t *testing.T, refuseAfterFirst bool) int64 {
		nmgr := aheadManager(t, 8, 0)
		r := newAheadReader(t, nmgr)
		r.downstream = streams.InboundEvents // a stream that applies backpressure
		g := nmgr.backpressure()
		base := time.Now()
		gs := &gateState{suffix: streams.InboundEvents, stream: StreamName("test", streams.InboundEvents), sampledAt: base}
		var calls atomic.Int32
		g.mu.Lock()
		g.gates[streams.InboundEvents] = gs
		g.now = func() time.Time {
			if refuseAfterFirst && calls.Add(1) > 1 {
				return base.Add(time.Hour) // stale: the gate refuses
			}
			return base
		}
		g.mu.Unlock()
		publishAsync(t, nmgr, 20)
		require.NoError(t, read(t, r, 10*time.Second).Ack())
		return r.aheadStarts.Load()
	}
	require.Equal(t, int64(1), run(t, false), "control: with the gate open a request is started")
	require.Zero(t, run(t, true), "a request was started while the downstream was refusing")
}

// An EOF (the caller's context ending) leaves the request in flight, as it leaves the fetch
// buffer: the next read takes that request's batch rather than fetching again.
func TestFetchAheadAnEOFKeepsTheRequestForTheNextRead(t *testing.T) {
	nmgr := aheadManager(t, 8, 0)
	r := newAheadReader(t, nmgr)
	publishAsync(t, nmgr, 24)
	for i := 0; i < 8; i++ { // the first batch; a request for the second is in flight
		require.NoError(t, read(t, r, 10*time.Second).Ack())
	}
	require.NotNil(t, r.future)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.ReadMessage(cancelled)
	require.ErrorIs(t, err, io.EOF)
	require.NotNil(t, r.future, "an EOF dropped the request in flight")
	msg := read(t, r, 10*time.Second)
	require.Equal(t, uint64(9), msg.StreamSeq, "the next read did not take the kept request's batch")
	require.Equal(t, 1, msg.NumDelivered, "the batch was delivered again instead of being kept")
	require.NoError(t, msg.Ack())
}
