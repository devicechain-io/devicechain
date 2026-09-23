// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
)

// capacityManager is newTestManager with the stream metrics a real manager builds, so the
// held-past-AckWait counter exists and can be read back, and with AckWait set when asked.
func capacityManager(t *testing.T, ackWait time.Duration) *NatsManager {
	t.Helper()
	nmgr, cleanup := newTestManager(t)
	t.Cleanup(cleanup)
	nmgr.metrics = newStreamMetrics(nmgr.Microservice)
	if ackWait > 0 {
		nmgr.SetAckWaitForTesting(t, ackWait)
	}
	return nmgr
}

// capacityReaderFor builds a reader through NewReader — the real constructor — with the given
// capacity (0 for a plain reader), and returns its implementation.
func capacityReaderFor(t *testing.T, nmgr *NatsManager, slots int) *natsReader {
	t.Helper()
	var opts []ReaderOption
	if slots > 0 {
		opts = append(opts, ReaderWithCapacity(slots))
	}
	_, err := nmgr.NewReader(streams.InboundEvents, opts...)
	require.NoError(t, err)
	return nmgr.readers[len(nmgr.readers)-1]
}

// readWithin reads one message under its own deadline, so a reader that blocks forever fails
// the test instead of hanging it.
func readWithin(t *testing.T, r *natsReader, d time.Duration) (Message, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return r.ReadMessage(ctx)
}

// ackPending is how many messages the BROKER has handed this durable and not had acked — the
// broker's own count of what the reader fetched, independent of anything the reader reports.
func ackPending(t *testing.T, nmgr *NatsManager, r *natsReader) int {
	t.Helper()
	info, err := nmgr.js.ConsumerInfo(r.stream, r.durable)
	require.NoError(t, err)
	return info.NumAckPending
}

func heldPastAckWait(nmgr *NatsManager, r *natsReader, stage string) float64 {
	return testutil.ToFloat64(nmgr.metrics.heldPastAckWait.WithLabelValues(r.durable, stage))
}

// injectLeftover makes the broker deliver the next published message into the reader's
// subscription buffer OUTSIDE any Fetch — the way a delivery lands after the client has stopped
// waiting on the pull request that asked for it. It issues a pull request of its own on the
// subscription's reply inbox, publishes one message, and waits for it to arrive in the buffer.
func injectLeftover(t *testing.T, nmgr *NatsManager, r *natsReader) {
	t.Helper()
	sub := r.sub.Load()
	require.NotNil(t, sub)
	inbox := strings.TrimSuffix(sub.Subject, "*") + "leftover"
	next := fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.%s", r.stream, r.durable)
	require.NoError(t, nmgr.nc.PublishRequest(next, inbox, []byte(`{"batch":1,"expires":30000000000}`)))
	require.NoError(t, nmgr.nc.Flush())
	publishN(t, nmgr, streams.InboundEvents, 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _, err := sub.Pending(); err == nil && n >= 1 {
			return
		}
		require.True(t, time.Now().Before(deadline), "the injected delivery never reached the subscription buffer")
		time.Sleep(10 * time.Millisecond)
	}
}

// A capacity reader fetches only what it has slots for: three slots, three messages, and then
// NOTHING until one is released — however much is waiting on the stream.
//
// GUARD: kills "fetch fetchBatch regardless". The broker's own ack-pending count is the
// instrument, because a reader that fetched 64 and handed out 3 would look identical from the
// messages it returned; what differs is how many clocks the broker started.
func TestCapacityBoundsFetch(t *testing.T) {
	nmgr := capacityManager(t, 0)
	r := capacityReaderFor(t, nmgr, 3)
	publishN(t, nmgr, streams.InboundEvents, 200)

	var held []Message
	for i := 0; i < 3; i++ {
		msg, err := readWithin(t, r, 5*time.Second)
		require.NoError(t, err)
		held = append(held, msg)
	}
	require.Equal(t, 3, ackPending(t, nmgr, r),
		"the broker handed out a different number of messages than the reader has slots")

	start := time.Now()
	_, err := readWithin(t, r, 2*time.Second)
	require.ErrorIs(t, err, io.EOF, "with every slot held the read must block until its context ends")
	require.GreaterOrEqual(t, time.Since(start), 1500*time.Millisecond, "the fourth read returned early")
	require.Equal(t, 3, ackPending(t, nmgr, r), "a read with no free slot fetched anyway")

	held[0].Release()
	msg, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err, "a released slot must let exactly one more message in")
	require.NotZero(t, msg.StreamSeq)
	require.Equal(t, 4, ackPending(t, nmgr, r), "releasing one slot must fetch exactly one more message")
	_, err = readWithin(t, r, time.Second)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 4, ackPending(t, nmgr, r))
}

// Every reader that did NOT ask for capacity keeps its full 64-message fetch, and its messages
// carry no AckDeadline.
//
// GUARD: kills "capacity/stamp applied to every reader". A full-batch consumer hands a message
// out an unknown time after its clock started, so a stamp there would invite a budget to be
// re-based onto a number that does not describe its queue — the reason the deadline is zero.
func TestNoCapacityKeepsFullBatchAndNoDeadline(t *testing.T) {
	nmgr := capacityManager(t, 0)
	r := capacityReaderFor(t, nmgr, 0)
	publishN(t, nmgr, streams.InboundEvents, 100)

	msg, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, r.pending, fetchBatch-1, "a plain reader must buffer the rest of a full batch")
	require.Equal(t, fetchBatch, ackPending(t, nmgr, r), "a plain reader must fetch a full batch")
	require.True(t, msg.AckDeadline().IsZero(), "a plain reader's message must carry no AckDeadline")
	_, ok := AckDeadlineFrom(WithAckDeadline(context.Background(), msg))
	require.False(t, ok, "WithAckDeadline must carry nothing for a message with no AckDeadline")
	require.Nil(t, r.capacity)
}

// The ack is sent and THEN the slot returns, whether or not the send succeeded.
//
// A failed ack leaves the message to be redelivered, and that redelivery is a new delivery with
// a slot of its own; keeping this one would shrink the pool by one for every ack a broker blip
// swallowed.
func TestAckReleasesEvenWhenTheAckFails(t *testing.T) {
	nmgr := capacityManager(t, 0)
	r := capacityReaderFor(t, nmgr, 1)
	publishN(t, nmgr, streams.InboundEvents, 1)

	msg, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 0, r.capacity.free())

	nmgr.nc.Close()
	require.Error(t, msg.Ack(), "an ack on a closed connection must fail, or this test proves nothing")
	require.Equal(t, 1, r.capacity.free(), "a failed ack kept its slot")
}

// A slot is reclaimed by its AckWait timer even while the reader is blocked waiting for one —
// not only when something next tries to acquire.
//
// GUARD: kills "reclaim only on acquire". A worker that never returns a message would otherwise
// hold its slot for good, and a reader with every slot held that way reads nothing ever again.
func TestSlotReclaimedAtAckDeadlineWhileAcquireBlocks(t *testing.T) {
	testCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nmgr := capacityManager(t, time.Second)
	r := capacityReaderFor(t, nmgr, 1)
	publishN(t, nmgr, streams.InboundEvents, 2)

	first, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)

	start := time.Now()
	next, err := r.ReadMessage(testCtx)
	elapsed := time.Since(start)
	require.NoError(t, err, "the blocked read never got the reclaimed slot")
	require.NotZero(t, next.StreamSeq)
	require.GreaterOrEqual(t, elapsed, 700*time.Millisecond, "the read did not wait for the AckWait")
	require.Less(t, elapsed, 5*time.Second, "the slot was not reclaimed at its AckWait")
	require.Equal(t, 1.0, heldPastAckWait(nmgr, r, stageWorker), "held past AckWait, stage=worker")
	require.Equal(t, 0.0, heldPastAckWait(nmgr, r, stageBuffer), "held past AckWait, stage=buffer")

	// The worker finally returns: its release must change nothing, because the slot was
	// already taken back and is now held by the next message.
	first.Release()
	require.Equal(t, 0, r.capacity.free(), "a release after the reclaim returned a second token")
	require.Equal(t, 1.0, heldPastAckWait(nmgr, r, stageWorker), "a release after the reclaim was counted")
}

// A buffered message whose AckWait has already run out is never handed out: the broker is
// redelivering it, so this copy is the duplicate. It is dropped and counted at stage=buffer, and
// the read goes on to return the broker's redelivery instead.
func TestStaleBufferedMessageIsNeverHandedOut(t *testing.T) {
	testCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ackWait := 2 * time.Second
	nmgr := capacityManager(t, ackWait)
	r := capacityReaderFor(t, nmgr, 2)
	publishN(t, nmgr, streams.InboundEvents, 1)

	first, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	require.NoError(t, first.Ack())
	since := r.leftoverSince
	require.False(t, since.IsZero())

	injectLeftover(t, nmgr, r)
	// Outlast the leftover's stamp: it is stamped with the earlier fetch, so it is stale now.
	time.Sleep(time.Until(since.Add(ackWait + 300*time.Millisecond)))

	msg, err := r.ReadMessage(testCtx)
	require.NoError(t, err)
	require.Equal(t, 2, msg.NumDelivered,
		"the read returned the stale buffered copy (delivery %d) instead of the broker's redelivery", msg.NumDelivered)
	require.Equal(t, 1.0, heldPastAckWait(nmgr, r, stageBuffer), "held past AckWait, stage=buffer")
	require.Equal(t, 0.0, heldPastAckWait(nmgr, r, stageWorker), "held past AckWait, stage=worker")
}

// A delivery that was already in the subscription's buffer when a fetch started keeps the
// EARLIER request's time: its clock started then, not at this fetch.
//
// GUARD: kills "stamp leftovers with t0". Stamped with the new fetch's time, a leftover would be
// credited budget the broker is not giving it, and a worker could still be sending when the
// broker hands it to someone else.
func TestLeftoverDeliveriesKeepTheirEarlierStamp(t *testing.T) {
	nmgr := capacityManager(t, 0)
	r := capacityReaderFor(t, nmgr, 2)
	publishN(t, nmgr, streams.InboundEvents, 1)

	first, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	require.NoError(t, first.Ack())
	since := r.leftoverSince
	require.False(t, since.IsZero())
	require.Equal(t, since.Add(AckWait), first.AckDeadline(), "a fresh fetch is stamped with its own start")

	injectLeftover(t, nmgr, r)
	time.Sleep(1500 * time.Millisecond)

	before := time.Now()
	msg, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, msg.NumDelivered)
	require.Equal(t, since, r.leftoverSince, "a fetch that found a leftover must not move leftoverSince")
	require.Equal(t, since.Add(AckWait), msg.AckDeadline(),
		"the leftover was not stamped with the earlier request's time")
	require.True(t, msg.AckDeadline().Before(before.Add(AckWait-time.Second)),
		"the leftover's deadline %v is not earlier than this fetch's would be", msg.AckDeadline())
}

// Process returns the slot however the handler leaves: by returning, by acking, or by panicking.
func TestProcessReleasesOnEveryPath(t *testing.T) {
	nmgr := capacityManager(t, 0)
	r := capacityReaderFor(t, nmgr, 1)
	publishN(t, nmgr, streams.InboundEvents, 3)

	handlers := map[string]func(Message){
		"returns":  func(Message) {},
		"acks":     func(m Message) { _ = m.Ack() },
		"panicked": func(Message) { panic("handler failed") },
	}
	for _, name := range []string{"returns", "acks", "panicked"} {
		msg, err := readWithin(t, r, 5*time.Second)
		require.NoError(t, err, name)
		require.Equal(t, 0, r.capacity.free(), "%s: the message did not hold its slot", name)
		func() {
			defer func() { _ = recover() }()
			Process(msg, handlers[name])
		}()
		require.Equal(t, 1, r.capacity.free(), "%s: Process did not return the slot", name)
		require.Equal(t, slotReleased, msg.slot.state.Load(), "%s: slot state", name)
	}
}

// AckDeadline rides a context as a value — never as a cancellation — and reads back exactly.
func TestAckDeadlineRidesTheContextAsAValue(t *testing.T) {
	nmgr := capacityManager(t, 0)
	r := capacityReaderFor(t, nmgr, 1)
	publishN(t, nmgr, streams.InboundEvents, 1)

	msg, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	defer msg.Release()
	require.False(t, msg.AckDeadline().IsZero())

	ctx := WithAckDeadline(context.Background(), msg)
	got, ok := AckDeadlineFrom(ctx)
	require.True(t, ok)
	require.Equal(t, msg.AckDeadline(), got)
	_, hasDeadline := ctx.Deadline()
	require.False(t, hasDeadline, "WithAckDeadline must not put a deadline on the context")
	require.False(t, errors.Is(ctx.Err(), context.DeadlineExceeded))
	_, ok = AckDeadlineFrom(context.Background())
	require.False(t, ok)
}

// A durable's configured AckWait and the AckDeadline its capacity reader stamps are one number.
func TestAckWaitSeamFeedsTheDurableAndTheDeadline(t *testing.T) {
	nmgr := capacityManager(t, 7*time.Second)
	r := capacityReaderFor(t, nmgr, 1)
	info, err := nmgr.js.ConsumerInfo(r.stream, r.durable)
	require.NoError(t, err)
	require.Equal(t, 7*time.Second, info.Config.AckWait, "the durable's AckWait")

	publishN(t, nmgr, streams.InboundEvents, 1)
	msg, err := readWithin(t, r, 5*time.Second)
	require.NoError(t, err)
	defer msg.Release()
	require.Equal(t, r.leftoverSince.Add(7*time.Second), msg.AckDeadline(), "the message's AckDeadline")
	require.Panics(t, func() { nmgr.SetAckWaitForTesting(t, time.Second) },
		"changing AckWait after a durable exists must be refused")
}
