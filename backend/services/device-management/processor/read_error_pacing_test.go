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
// instantly and keeps returning — a broker refusing fetches, a revoked credential, a
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

func TestTheRaiseAlarmLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{}
	rc := &RaiseAlarmConsumer{
		Reader:    reader,
		readPacer: core.NewReadPacer(nil, "raise-alarm").UseClock(core.VirtualClock()),
	}

	stopped := false
	for i := 0; i < readCap && !stopped; i++ {
		stopped = rc.readMessage(context.Background())
	}
	if !stopped {
		t.Fatalf("the raise-alarm loop read %d times against an error that never clears and was "+
			"still going: it burns a core and logs at full rate forever while the pod reports ready",
			reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the raise-alarm loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

func TestTheInboundEventsLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{}
	iproc := &InboundEventsProcessor{
		InboundEventsReader: reader,
		readPacer:           core.NewReadPacer(nil, "inbound events").UseClock(core.VirtualClock()),
	}

	stopped := false
	for i := 0; i < readCap && !stopped; i++ {
		stopped = iproc.ProcessMessage(context.Background())
	}
	if !stopped {
		t.Fatalf("the inbound events loop read %d times against an error that never clears and "+
			"was still going: it burns a core and logs at full rate forever while the pod reports "+
			"ready", reader.Reads)
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
	pacer := core.NewReadPacer(nil, "raise-alarm").UseClock(core.VirtualClock())
	reader := &intermittentReader{}
	rc := &RaiseAlarmConsumer{Reader: reader, Api: nil, readPacer: pacer}

	// Alternating failure and success, far more failures than any single run could absorb.
	for i := 0; i < 400; i++ {
		if rc.readMessage(context.Background()) {
			t.Fatalf("the raise-alarm loop stopped at read %d even though every failure was "+
				"followed by a successful read", i+1)
		}
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
	if r.n%2 == 1 {
		return r.FailingReader.ReadMessage(ctx)
	}
	return messaging.Message{}, nil
}
