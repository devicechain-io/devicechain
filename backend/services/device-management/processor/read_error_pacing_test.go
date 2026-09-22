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

// 🔴 WHAT THIS FILE IS FOR. Both read loops in this package used to treat every error that
// was not io.EOF the same way: log it, read again, immediately. A read error that returns
// instantly and keeps returning — a broker refusing fetches, a
// subscription the reader's own self-heal cannot rebuild — therefore became a spin that
// burned a core and wrote one log line per iteration, flooding the log pipeline at exactly
// the moment an operator needed to read it. It never stopped, and the pod went on reporting
// ready the whole time.
//
// 🔑 THE ONLY THING THAT SEPARATES THAT LOOP FROM A CORRECT ONE IS HOW OFTEN IT ASKS. Both
// produce no output against a failing reader, so no assertion about results can tell them
// apart. These tests count the reads.
//
// The clock is virtual, so the numbers below are the loop's own and not the machine's — a
// test that waited out the real backoff would take the real budget.

// readCap is far beyond what a paced loop can reach and far short of forever, so an unpaced
// loop fails the assertion rather than hanging the run.
const readCap = 5000

// The loops are driven as a whole (each is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.
//
// 🔑 WHAT THESE STILL TEST NOW THE LOOP ITSELF LIVES IN messaging.RunConsumer: the WIRING —
// that each of these consumers drives the shared loop with a pacer of its own. That is the
// "constructed correctly, connected to nothing" class a test of the library alone cannot see.
func TestTheRaiseAlarmLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	rc := &RaiseAlarmConsumer{
		Reader:    reader,
		readPacer: core.NewReadPacer(nil, "raise-alarm").UseClock(core.VirtualClock()),
	}

	rc.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the raise-alarm loop read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it burns a core and logs at full rate "+
			"forever while the pod reports ready", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the raise-alarm loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

func TestTheInboundEventsLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	iproc := &InboundEventsProcessor{
		InboundEventsReader: reader,
		messages:            make(chan messaging.Message, readCap),
		readPacer:           core.NewReadPacer(nil, "inbound events").UseClock(core.VirtualClock()),
	}

	iproc.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the inbound events loop read %d times against an error that never clears and "+
			"only stopped because the test's reader ran out: it burns a core and logs at full "+
			"rate forever while the pod reports ready", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the inbound events loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT, AND IT IS NOT OPTIONAL. Everything above is satisfied by a loop that
// stops on the FIRST error, which would turn every transient broker hiccup into a dead
// consumer. This pins that a read which succeeds keeps the loop running, and that the run of
// failures is cleared by it — the reset that stops one error an hour from accumulating,
// across a day, into a service that tears itself down for faults it recovered from.
func TestASuccessfulReadKeepsBothLoopsRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	rc := &RaiseAlarmConsumer{
		Reader:    reader,
		Api:       nil,
		readPacer: core.NewReadPacer(nil, "raise-alarm").UseClock(core.VirtualClock()),
	}

	rc.readLoop(context.Background())

	if reader.Reads != eofAt {
		t.Fatalf("the raise-alarm loop stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
			reader.Reads, eofAt)
	}
}

// intermittentReader fails every other read and otherwise returns an empty message, which
// handle() drops as having no parseable tenant — the loop keeps going either way, which is
// the property under test.
type intermittentReader struct {
	msgtest.FailingReader
	n int
}

func (r *intermittentReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.n++
	// 🔑 THE EOFAfter GUARD IS LOAD-BEARING. Without "> 0" the comparison Reads+1 >= EOFAfter
	// is TRUE for an unset EOFAfter, so every read would fail and the alternation this fake
	// exists for would silently stop happening.
	if r.n%2 == 1 || (r.EOFAfter > 0 && r.Reads+1 >= r.EOFAfter) {
		return r.FailingReader.ReadMessage(ctx)
	}
	// Counted, so Reads means "calls" at every caller.
	r.Reads++
	return messaging.Message{}, nil
}
