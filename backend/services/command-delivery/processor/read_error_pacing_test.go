// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
)

// 🔴 WHAT THIS FILE IS FOR. The command-responses read loop used to treat every error that
// was not io.EOF the same way: log it, read again, immediately. A read error that returns
// instantly and keeps returning — a broker refusing fetches, a
// subscription the reader's own self-heal cannot rebuild — therefore became a spin that
// burned a core and wrote one log line per iteration, forever.
//
// 🔑 AND THIS LOOP'S SILENCE PRODUCES WRONG DATA, NOT JUST MISSING DATA. Dispatch keeps
// running while nothing settles, so every command rides SENT to its TTL and terminalizes as
// TIMEOUT — which blames the device for a fault on this side of the wire. Nothing about that
// row looks wrong, which is why the loop's read RATE is the thing to measure: a paced loop
// and a spinning one settle exactly the same nothing.

const readCap = 5000

// The loop is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.
//
// 🔑 WHAT THESE STILL TEST NOW THE LOOP ITSELF LIVES IN messaging.RunConsumer: the WIRING.
// That this processor drives the shared loop with a pacer of its own, and that nothing an
// ordinary response contains ends the loop. Both are the "constructed correctly, connected
// to nothing" class a test of the library alone cannot see.
func TestTheResponseLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	cproc := &CommandDeliveryProcessor{
		CommandResponsesReader: reader,
		readPacer:              core.NewReadPacer(nil, "command responses").UseClock(core.VirtualClock()),
	}

	cproc.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the response loop read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it burns a core and logs at full rate "+
			"forever while every command in flight rides SENT to a TIMEOUT that blames the "+
			"device", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the response loop took %d reads to stop; that is too many to be a paced retry",
			reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT. The test above is satisfied by a loop that stops on the FIRST error,
// which would turn every transient broker hiccup into a service that settles nothing again.
// This pins that a successful read keeps the loop running AND clears the run of failures.
func TestASuccessfulReadKeepsTheResponseLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	cproc := &CommandDeliveryProcessor{
		CommandResponsesReader: reader,
		readPacer:              core.NewReadPacer(nil, "command responses").UseClock(core.VirtualClock()),
	}

	cproc.readLoop(context.Background())

	if reader.Reads != eofAt {
		t.Fatalf("the response loop stopped after %d reads even though every failure was followed "+
			"by a successful read; it should have run to its reader's EOF at %d", reader.Reads, eofAt)
	}
}

// intermittentReader fails every other read and otherwise returns an empty message, which
// the loop drops as having no parseable tenant.
type intermittentReader struct {
	msgtest.FailingReader
	n int
}

func (r *intermittentReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.n++
	// 🔑 THE EOFAfter GUARD IS LOAD-BEARING AND EASY TO DROP. Without "> 0" the comparison
	// Reads+1 >= EOFAfter is TRUE for an unset EOFAfter, so every read would fail and the
	// alternation this fake exists for would silently stop happening — the response-loop
	// test above uses it with no EOF at all.
	if r.n%2 == 1 || (r.EOFAfter > 0 && r.Reads+1 >= r.EOFAfter) {
		return r.FailingReader.ReadMessage(ctx)
	}
	// Counted, so Reads means "calls" at every caller. It used to count only the failures,
	// which made the write-back test below report half the reads it had actually done.
	r.Reads++
	return messaging.Message{}, nil
}

// 🔴 THE SECOND READ LOOP IN THIS SERVICE, and it is here rather than in a file of its own
// because readCap and intermittentReader above are already declared in this package — a
// second copy would not compile, and a second file would have to invent near-duplicates of
// both. The dead-letter write-back drains its own durable over the platform dead-letter
// stream and had the same unpaced shape the response loop had.
//
// 🔑 ITS SILENCE IS THE DEFECT IT EXISTS TO REMOVE, ARRIVED AT FROM THE INSIDE. The
// write-back is what stops a command whose answer was dead-lettered from riding SENT to a
// TIMEOUT that blames the device. A copy of it spinning on an unclearable error settles
// nothing, so every one of those commands terminalizes on exactly the mis-attribution this
// consumer was built to prevent — and each row looks individually plausible, which is why
// the loop's read RATE is the thing to measure rather than any assertion about state.
//
// The loop is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach.

// pacingWriteback assembles a write-back with nothing but the parts the read loop touches:
// a reader, a pacer on a virtual clock, and real counters, since Handle() increments one on
// every message and a zero WritebackMetrics holds nil interfaces.
func pacingWriteback(t *testing.T, reader messaging.MessageReader) *DeadLetterWriteback {
	t.Helper()
	w := &DeadLetterWriteback{
		reader: reader,
		WritebackMetrics: &WritebackMetrics{
			settled:       prometheus.NewCounter(prometheus.CounterOpts{Name: "settled_total"}),
			notAnswerable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_answerable_total"}),
			unreadable:    prometheus.NewCounter(prometheus.CounterOpts{Name: "unreadable_total"}),
			notOurs:       prometheus.NewCounter(prometheus.CounterOpts{Name: "not_ours_total"}),
			notActionable: prometheus.NewCounter(prometheus.CounterOpts{Name: "not_actionable_total"}),
			stranded:      prometheus.NewCounter(prometheus.CounterOpts{Name: "stranded_total"}),
		},
		readPacer: core.NewReadPacer(nil, "command dead letters").UseClock(core.VirtualClock()),
	}
	w.procCtx, w.procCancel = context.WithCancel(context.Background())
	t.Cleanup(w.procCancel)
	w.wg.Add(1)
	return w
}

func TestTheWritebackLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	w := pacingWriteback(t, reader)

	w.loop()

	if reader.Reads >= readCap {
		t.Fatalf("the write-back loop read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it retries forever behind a ready pod "+
			"while every command whose answer was lost rides SENT to a TIMEOUT that blames the "+
			"device", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the write-back loop took %d reads to stop; that is too many to be a paced retry",
			reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT, GUARDING THE RISKIEST LINE IN THE CHANGE. The test above is
// satisfied by a loop that stops on the FIRST error, which would turn every broker hiccup
// into a write-back that settles nothing again. What prevents that is the Succeeded() call
// on the success path and nothing else: without it the run of failures is never cleared, and
// a service hitting one blip every few minutes accumulates them across an uptime into a
// give-up it never earned. Delete that one line and this goes red; the test above stays green.
func TestASuccessfulReadKeepsTheWritebackLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	w := pacingWriteback(t, reader)

	w.loop()

	if reader.Reads != eofAt {
		t.Fatalf("the write-back loop stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
			reader.Reads, eofAt)
	}
}
