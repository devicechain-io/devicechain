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

// 🔴 WHAT THIS FILE IS FOR. The alarm-events read loop used to treat every error that was not
// io.EOF the same way: log it, read again, immediately. A read error that returns instantly
// and keeps returning — a broker refusing fetches, a subscription the
// reader's own self-heal cannot rebuild — therefore became a spin that burned a core and
// wrote one log line per iteration. It never stopped, and nobody was paged for any alarm in
// the meantime, behind a pod that reported ready.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. Both
// notify nobody against a failing reader, so no assertion about notifications can tell them
// apart. This counts the reads, on a virtual clock.

const readCap = 5000

// The loop is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.
//
// 🔑 WHAT THESE STILL TEST NOW THE LOOP ITSELF LIVES IN messaging.RunConsumer: the WIRING —
// that this consumer drives the shared loop with a pacer of its own, and that an ordinary
// alarm does not end it. That is the "constructed correctly, connected to nothing" class a
// test of the library alone cannot see.
func TestTheAlarmEventsLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	np := &NotificationProcessor{
		Reader:    reader,
		messages:  make(chan messaging.Message, readCap),
		readPacer: core.NewReadPacer(nil, "alarm events").UseClock(core.VirtualClock()),
	}

	np.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the alarm-events loop read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it burns a core and logs at full rate "+
			"forever while no alarm reaches anyone and the pod reports ready", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the alarm-events loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT. The test above is satisfied by a loop that stops on the FIRST error,
// which would turn every transient broker hiccup into a consumer that pages nobody again.
// This pins that a successful read keeps the loop running AND clears the run of failures.
func TestASuccessfulReadKeepsTheAlarmEventsLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	np := &NotificationProcessor{
		Reader:    reader,
		messages:  make(chan messaging.Message, eofAt),
		readPacer: core.NewReadPacer(nil, "alarm events").UseClock(core.VirtualClock()),
	}

	np.readLoop(context.Background())

	if reader.Reads != eofAt {
		t.Fatalf("the alarm-events loop stopped after %d reads even though every failure was "+
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
