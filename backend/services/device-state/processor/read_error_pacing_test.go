// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

// 🔴 WHAT THIS FILE IS FOR. The resolved-events read loop used to treat every error that was
// not io.EOF the same way: log it, read again, immediately. A read error that returns
// instantly and keeps returning — a broker refusing fetches, a
// subscription the reader's own self-heal cannot rebuild — therefore became a spin that
// burned a core and wrote one log line per iteration. It never stopped, and the projection
// went stale behind a pod that reported ready throughout.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. A paced
// loop and a spinning one both merge nothing against a failing reader, so no assertion about
// state can tell them apart. This counts the reads, on a virtual clock, so the number is the
// loop's and not the machine's.
//
// 🔑 WHAT IT STILL TESTS NOW THE LOOP ITSELF LIVES IN messaging.RunConsumer. The loop's
// behaviour is pinned in core, once. What is service-specific — and what these two would
// catch — is the WIRING: that this processor drives the shared loop with a real pacer of its
// own rather than a fresh one per read, and that its handoff to the worker pool does not
// end the loop on an ordinary message. Both are exactly the "constructed correctly,
// connected to nothing" class that a test of the library alone cannot see.
//
// The loop is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.

const readCap = 5000

// pacingProcessor is a StateProcessor with just enough assembled to run its read loop.
// procCtx is not optional: the handoff selects on it, so a nil one panics rather than
// blocking, and messages is buffered so a good read never parks the loop.
func pacingProcessor(reader messaging.MessageReader) *StateProcessor {
	return &StateProcessor{
		ResolvedEventsReader: reader,
		procCtx:              context.Background(),
		messages:             make(chan messaging.Message, readCap),
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}
}

func TestTheResolvedEventsLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	sp := pacingProcessor(reader)

	sp.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the resolved-events loop read %d times against an error that never clears and "+
			"only stopped because the test's reader ran out: it burns a core and logs at full "+
			"rate forever while the projection goes stale behind a ready pod", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the resolved-events loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT. The test above is satisfied by a loop that stops on the FIRST error,
// which would turn every transient broker hiccup into a dead consumer. This pins that a
// successful read keeps the loop running AND clears the run of failures — the reset without
// which one error an hour accumulates, across a day, into a service that tears itself down
// for faults it recovered from. The loop here reaches its reader's EOF, which is the only
// way it is allowed to end.
func TestASuccessfulReadKeepsTheResolvedEventsLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	sp := pacingProcessor(reader)

	sp.readLoop(context.Background())

	if reader.Reads != eofAt {
		t.Fatalf("the resolved-events loop stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
			reader.Reads, eofAt)
	}
}

// intermittentReader fails every other read and otherwise returns an empty message.
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
