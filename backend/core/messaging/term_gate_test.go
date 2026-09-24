// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// publishN writes n messages for one tenant onto a suffix's subject.
func publishN(t *testing.T, nmgr *NatsManager, suffix string, n int) {
	t.Helper()
	writer, err := nmgr.NewWriter(suffix)
	require.NoError(t, err)
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", suffix)
	ctx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < n; i++ {
		require.NoError(t, writer.WriteMessages(ctx, Message{
			Subject: subject, Key: []byte("k"), Value: []byte(`{"telemetry":true}`),
		}))
	}
}

// A reader gated on a leadership term must hand out NOTHING while the term is not
// held — and it must do so by PARKING, not by reporting end-of-stream.
//
// The distinction is the whole point of the option. Every DETECT read loop treats
// io.EOF as "shut down" and returns, so a gate that closed by reporting EOF would
// leave a pod that has acquired the lease, finished its term build and published
// is_leader=1 with no goroutine reading anything — a wedge no liveness signal can
// see. So this asserts two separate things: that no message came out, and that the
// call was still blocked when we cancelled it rather than having returned early.
func TestATermGatedReaderParksInsteadOfReportingEndOfStream(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool // starts false: no term
	reader, err := nmgr.NewReader(streams.InboundEvents, ReaderWithTermGate(held.Load))
	require.NoError(t, err)

	publishN(t, nmgr, streams.InboundEvents, 3)

	// Give the gate real time to misbehave. fetchTimeout is well under this, so a
	// reader that fetched-then-gated (or did not gate at all) has ample opportunity
	// to return a message here.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err = reader.ReadMessage(ctx)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, io.EOF, "a cancelled context still unwinds as EOF, which is how a term teardown ends its loops")
	require.GreaterOrEqual(t, elapsed, 2*time.Second-100*time.Millisecond,
		"ReadMessage returned before its context expired, so it did not park — it reported "+
			"end-of-stream while the term was merely not held, which silently stops every read loop")
}

// The counterweight: once the term IS held, the same reader delivers. Without this,
// a gate hard-wired shut would pass the test above.
func TestATermGatedReaderDeliversOnceTheTermIsHeld(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool
	reader, err := nmgr.NewReader(streams.InboundEvents, ReaderWithTermGate(held.Load))
	require.NoError(t, err)

	publishN(t, nmgr, streams.InboundEvents, 1)

	held.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, msg.Value)
}

// 🔴 THE GATE MUST COVER THE FETCH BUFFER, NOT ONLY THE FETCH.
//
// Messages arrive in batches of up to fetchBatch and are buffered, so a term that
// ends between two ReadMessage calls leaves messages already in hand. Those were
// fetched under OUR ownership but would be applied, published and acked under the
// SUCCESSOR'S — the single-writer violation the lease exists to prevent, and the
// one a gate wrapped around the network call instead of around the handout would
// miss entirely.
//
// So: read one message with the term held (which pulls a batch into the buffer),
// drop the term, and assert the buffered remainder does not come out.
func TestATermGatedReaderWithholdsMessagesAlreadyInItsBuffer(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool
	reader, err := nmgr.NewReader(streams.InboundEvents, ReaderWithTermGate(held.Load))
	require.NoError(t, err)

	publishN(t, nmgr, streams.InboundEvents, 10)

	held.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = reader.ReadMessage(ctx)
	require.NoError(t, err, "the first read primes the buffer")

	// Confirm the buffer really is primed, or the assertion below would hold
	// vacuously for a reader that simply had nothing left to give.
	nr, ok := reader.(*natsReader)
	require.True(t, ok)
	require.NotEmpty(t, nr.pending,
		"no messages were buffered, so this test cannot show that the gate covers the buffer")

	held.Store(false)
	gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer gcancel()
	_, err = reader.ReadMessage(gctx)
	require.ErrorIs(t, err, io.EOF,
		"a buffered message was handed out after the term ended; it would be applied and acked "+
			"by a replica that no longer owns the partition")
}

// An ungated reader must be unaffected. Every other reader in the platform is one,
// so a gate that engaged when no predicate was supplied would stop the fleet.
func TestAReaderWithNoTermGateIsUnaffected(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	reader, err := nmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)
	publishN(t, nmgr, streams.InboundEvents, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, msg.Value)
}

// UnbindTerm drops the subscription and BindTerm restores it, and a reader between
// the two must not panic on its nil subscription — it parks, like a closed gate.
//
// The split exists because bind() makes JetStream API calls that fail in exactly
// the outage that ends a term, whereas Unsubscribe is local and succeeds while
// disconnected. This pins the mechanics of the split; the failure mode it prevents
// is described on BindTerm.
func TestUnbindTermParksTheReaderAndBindTermRestoresIt(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool
	held.Store(true)
	reader, err := nmgr.NewReader(streams.InboundEvents, ReaderWithTermGate(held.Load))
	require.NoError(t, err)
	nr := reader.(*natsReader)

	require.NoError(t, nr.UnbindTerm())
	require.Nil(t, nr.sub.Load(), "UnbindTerm left a subscription in place, so the reply inbox still has interest")

	publishN(t, nmgr, streams.InboundEvents, 1)

	// Held is TRUE here, so anything this reader does now is the unbound path rather
	// than the gate: it must park rather than dereference a nil subscription.
	uctx, ucancel := context.WithTimeout(context.Background(), time.Second)
	defer ucancel()
	_, err = reader.ReadMessage(uctx)
	require.ErrorIs(t, err, io.EOF)

	require.NoError(t, nr.BindTerm())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, msg.Value)
}

// primeBuffer reads one message through a term-gated reader whose term is held, so the
// rest of a three-message batch is left in its fetch buffer, and returns the reader's
// internals. It refuses to continue with an empty buffer: every assertion that follows is
// about what happens to buffered messages, and would hold vacuously without any.
func primeBuffer(t *testing.T, nmgr *NatsManager, held *atomic.Bool, opts ...ReaderOption) (MessageReader, *natsReader) {
	t.Helper()
	opts = append([]ReaderOption{ReaderWithTermGate(held.Load)}, opts...)
	reader, err := nmgr.NewReader(streams.InboundEvents, opts...)
	require.NoError(t, err)
	publishN(t, nmgr, streams.InboundEvents, 3)

	held.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.StreamSeq)
	require.NoError(t, first.Ack())

	nr := reader.(*natsReader)
	require.Len(t, nr.pending, 2,
		"the first read did not leave the other two messages in the fetch buffer, so this test "+
			"cannot show what happens to a buffer")
	return reader, nr
}

// parkBriefly closes the gate and makes one read that parks for well over two gate polls,
// which is how a term that ends between two reads looks to the reader.
func parkBriefly(t *testing.T, reader MessageReader, held *atomic.Bool) {
	t.Helper()
	held.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 6*termGatePoll)
	defer cancel()
	_, err := reader.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF, "a read with the gate closed handed a message out")
}

// A reader built with ReaderWithReleaseOnPark gives its buffer up when its gate closes,
// and gives it up by NAKING it: the broker hands the messages out again at once, not an
// AckWait later.
//
// 🔴 THE WHOLE PARK HAPPENS INSIDE ONE ReadMessage CALL, and that is the production shape.
// A parked reader does not return: it polls the gate inside the same call until the gate
// reopens, and its context is cancelled only at shutdown. So the release at the park is
// the ONLY thing that drops the buffer on a term loss — the end-of-stream release never
// runs. A test that ended its park with a context timeout would let the end-of-stream
// release do the work and could not tell the two apart.
//
// What the call returns once the gate reopens tells the three cases apart: no release at
// all hands out the buffered sequence 2 on its FIRST delivery; a release that dropped
// without Naking leaves the broker waiting out the whole AckWait, so the bounded read comes
// back empty; a release that Naked hands out a REDELIVERY (NumDelivered 2) at once.
func TestReleaseOnParkNaksAndDrops(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool
	reader, _ := primeBuffer(t, nmgr, &held, ReaderWithReleaseOnPark())

	// Far inside the AckWait the durable is configured with: only a Nak redelivers this fast.
	require.Greater(t, nmgr.ackWait(), 20*time.Second)

	held.Store(false)
	type result struct {
		msg Message
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		msg, err := reader.ReadMessage(ctx)
		done <- result{msg, err}
	}()

	// Well over two gate polls, so the call has parked; then reopen the gate from here,
	// with the call still in flight.
	time.Sleep(6 * termGatePoll)
	select {
	case r := <-done:
		t.Fatalf("the read returned while the gate was closed (err=%v): it did not park", r.err)
	default:
	}
	held.Store(true)

	r := <-done
	require.NoError(t, r.err, "nothing was redelivered inside AckWait: the buffer was dropped without being Nak'd")
	require.False(t, r.msg.StreamSeq == 2 && r.msg.NumDelivered == 1,
		"the parked call handed out the copy it had buffered before its gate closed: the park did not release it")
	require.Contains(t, []uint64{2, 3}, r.msg.StreamSeq)
	require.Equal(t, 2, r.msg.NumDelivered,
		"the message handed out after the park was not a redelivery; the stale buffered copy came out")
}

// The same release happens when the reader stops reading, so a clean stop hands the batch
// to whoever reads the durable next at once instead of an AckWait later. The read below is
// made with a context that is already cancelled while the gate is OPEN, so only the
// end-of-stream path — not the park — can be what releases the buffer.
func TestReleaseOnParkReleasesWhenTheReaderStops(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool
	reader, nr := primeBuffer(t, nmgr, &held, ReaderWithReleaseOnPark())

	stopped, stop := context.WithCancel(context.Background())
	stop()
	_, err := reader.ReadMessage(stopped)
	require.ErrorIs(t, err, io.EOF)
	require.Empty(t, nr.pending, "a stopped reader kept its buffer")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err, "nothing was redelivered inside AckWait: the stop dropped the buffer without Naking it")
	require.Contains(t, []uint64{2, 3}, msg.StreamSeq)
	require.Equal(t, 2, msg.NumDelivered)
}

// 🔴 THE GUARD FOR DETECT: a term-gated reader WITHOUT ReaderWithReleaseOnPark keeps its
// buffer across a park and hands it out, first-delivery copies and all, once the gate
// reopens. Held can flicker false and back inside one term, and a single writer that
// dropped its buffer there would see its input reordered. Release is opt-in; this is what
// fails if it ever becomes the default.
func TestTermGatedReaderKeepsItsBufferWithoutRelease(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var held atomic.Bool
	reader, nr := primeBuffer(t, nmgr, &held)

	parkBriefly(t, reader, &held)
	require.Len(t, nr.pending, 2, "a reader without release-on-park lost its buffer across a park")

	held.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), msg.StreamSeq, "the buffer was not handed out in order after the park")
	require.Equal(t, 1, msg.NumDelivered, "the buffered message was redelivered rather than handed out from the buffer")
}

// A Fetch waits up to its long-poll for messages, so a term can end while one is in
// flight. The batch it brings back must not be handed out under a gate that closed while
// it waited: the gate is re-checked when the Fetch returns.
//
// The gate here closes itself the moment the Fetch is under way. It is modelled as a
// predicate that answers true exactly once — the check before the Fetch — and false ever
// after, so every message that comes out of this read came out after the gate closed.
func TestATermGatedReaderRechecksItsGateAfterAFetch(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	var calls atomic.Int32
	once := func() bool { return calls.Add(1) == 1 }
	reader, err := nmgr.NewReader(streams.InboundEvents, ReaderWithTermGate(once))
	require.NoError(t, err)
	publishN(t, nmgr, streams.InboundEvents, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = reader.ReadMessage(ctx)
	require.ErrorIs(t, err, io.EOF,
		"a message fetched while the gate was closing was handed out after it had closed")
	require.Len(t, reader.(*natsReader).pending, 3,
		"the Fetch brought nothing back, so this test did not reach the post-Fetch check")
}
