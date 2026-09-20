// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

// 🔴 WHAT THIS FILE IS FOR. The dispatch read loop used to treat every error that was not
// io.EOF the same way: log it, pause a fixed second, read again, forever. The fixed pause
// takes the hot-spin off the table and leaves the other failure mode untouched — an error
// that is never going to clear — a 409 from a stream at its MaxAckPending ceiling, a
// consumer whose leadership keeps moving, a subscription the reader hands back rather than
// rebuilding — now spins slowly instead of quickly, and the pod goes on reporting ready
// while dispatching nothing. Spinning slower is not making progress. (A DELETED consumer is
// deliberately not in that list: natsReader rebinds through it without ever returning, so it
// never reaches a pacer. core.ReadPacer's own doc has the full account.)
//
// 🔑 AND THIS SERVICE EMITS, SO ITS SILENCE IS NOT RECOVERABLE LATER. A consumer that
// retains can be caught up once it comes back; a connector that never fired is a webhook
// that never landed, and the dispatch requests waiting for it are discarded at the stream's
// per-tenant bound while they wait. Every connector the tenant configured quietly does not
// happen, behind a pod reporting ready throughout.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. A paced
// loop and a spinning one dispatch exactly the same nothing against a failing reader, so no
// assertion about what was sent can tell them apart. These count the reads, on a virtual
// clock, so the number is the loop's and not the machine's.
//
// The loop is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.

const readCap = 5000

// pacingDispatchConsumer assembles a consumer with nothing but the parts the read loop
// touches. It runs no workers: the loop's only job on a successful read is the hand-off, so
// a buffered channel nobody drains is the whole of the success path, and dispatch itself
// stays out of a test about read rates.
func pacingDispatchConsumer(t *testing.T, reader messaging.MessageReader) *DispatchConsumer {
	t.Helper()
	c := &DispatchConsumer{
		reader:    reader,
		messages:  make(chan messaging.Message, readCap),
		readPacer: core.NewReadPacer(nil, "connector dispatch").UseClock(core.VirtualClock()),
	}
	c.procCtx, c.procCancel = context.WithCancel(context.Background())
	t.Cleanup(c.procCancel)
	c.readerWG.Add(1)
	return c
}

func TestTheDispatchLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	c := pacingDispatchConsumer(t, reader)

	c.run()

	if reader.Reads >= readCap {
		t.Fatalf("the dispatch loop read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it retries forever behind a ready pod "+
			"while every connector the tenant configured quietly does not fire", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the dispatch loop took %d reads to stop; that is too many to be a paced retry",
			reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT, GUARDING THE RISKIEST LINE IN THE CHANGE. The test above is
// satisfied by a loop that stops on the FIRST error, which would turn every broker hiccup
// into a service that dispatches nothing again. What prevents that is the Succeeded() call
// on the success path and nothing else: without it the run of failures is never cleared, and
// a service hitting one blip every few minutes accumulates them across an uptime into a
// give-up it never earned. Delete that one line and this goes red; the test above stays green.
func TestASuccessfulReadKeepsTheDispatchLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentDispatchReader{}
	reader.EOFAfter = eofAt
	c := pacingDispatchConsumer(t, reader)

	c.run()

	if reader.Reads != eofAt {
		t.Fatalf("the dispatch loop stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
			reader.Reads, eofAt)
	}
}

// intermittentDispatchReader fails every other read and otherwise returns an empty message,
// which the loop hands to the (undrained) worker channel.
type intermittentDispatchReader struct {
	msgtest.FailingReader
	n int
}

func (r *intermittentDispatchReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.n++
	if r.n%2 == 1 || (r.EOFAfter > 0 && r.Reads+1 >= r.EOFAfter) {
		return r.FailingReader.ReadMessage(ctx)
	}
	r.Reads++
	return messaging.Message{}, nil
}

// 🔑 THE REFUSAL HAS TO BE EXERCISED OR IT IS JUST A COMMENT. NewDispatchConsumer panics on a
// nil pacer rather than substituting one, because a substitute paces the loop but cannot call
// FailNow — so a forgotten argument would read as working and leave a ready pod dispatching
// nothing. That is worth a test precisely because the branch is unreachable from every other
// one: nothing else in this package constructs a consumer without a pacer.
func TestAConsumerCannotBeBuiltWithoutAReadPacer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewDispatchConsumer accepted a nil read pacer; a consumer built that way " +
				"cannot report an exhausted retry budget, and the pod stays ready while it " +
				"dispatches nothing")
		}
	}()
	NewDispatchConsumer(&fakeReader{}, &fakeWriter{}, nil, nil, nil, 0, nil, 1, 1, nil, nil)
}
