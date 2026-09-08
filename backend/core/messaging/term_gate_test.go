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
