// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
)

// 🔴 WHAT THIS FILE IS FOR. The dead-letter store loop used to treat every error that was not
// io.EOF the same way: log it, pause a fixed second, read again, forever. The fixed pause
// takes the hot-spin off the table and leaves the other failure mode untouched — an error
// that is never going to clear (a consumer deleted and un-recreatable, a revoked credential,
// a subscription the reader's own self-heal does not cover) now spins slowly instead of
// quickly, and the pod goes on reporting ready while consuming nothing.
//
// 🔑 AND THIS IS THE CONSUMER WHERE THAT COSTS THE MOST. It is the one that records
// everybody else's give-ups, so a copy of it that has quietly stopped is the single failure
// with nothing behind it: every service's dead letters age off the stream's seven-day window
// unstored, and the losses they describe end up recorded nowhere. That is precisely the
// outcome this consumer exists to prevent, arrived at from the inside.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. A paced
// loop and a spinning one store exactly the same nothing against a failing reader, so no
// assertion about the store can tell them apart. These count the reads, on a virtual clock,
// so the number is the loop's and not the machine's.
//
// The loop here is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.

const readCap = 5000

// pacingConsumer assembles a consumer with nothing but the parts the read loop touches: a
// reader, a pacer on a virtual clock, and real counters, since handle() increments one on
// every message and a zero Metrics holds nil interfaces.
func pacingConsumer(t *testing.T, reader messaging.MessageReader) *Consumer {
	t.Helper()
	c := &Consumer{
		reader: reader,
		Metrics: Metrics{
			stored:     prometheus.NewCounter(prometheus.CounterOpts{Name: "stored_total"}),
			unstorable: prometheus.NewCounter(prometheus.CounterOpts{Name: "unstorable_total"}),
			unstored:   prometheus.NewCounter(prometheus.CounterOpts{Name: "unstored_total"}),
		},
		readPacer: core.NewReadPacer(nil, "dead letters").UseClock(core.VirtualClock()),
	}
	c.procCtx, c.procCancel = context.WithCancel(context.Background())
	t.Cleanup(c.procCancel)
	c.wg.Add(1)
	return c
}

func TestTheDeadLetterStoreLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	c := pacingConsumer(t, reader)

	c.loop()

	if reader.Reads >= readCap {
		t.Fatalf("the dead-letter store loop read %d times against an error that never clears "+
			"and only stopped because the test's reader ran out: it retries forever behind a "+
			"ready pod while every service's dead letters age off the stream unstored",
			reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the dead-letter store loop took %d reads to stop; that is too many to be a "+
			"paced retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT, AND IT IS THE ONE THAT GUARDS THE RISKIEST LINE IN THE CHANGE. The
// test above is satisfied by a loop that stops on the FIRST error, which would turn every
// broker hiccup into a dead consumer. What keeps that from happening is the Succeeded() call
// on the success path, and nothing else does: without it the run of failures is never
// cleared, so a service hitting one blip every few minutes accumulates them across an uptime
// into a give-up it never earned — and it tears itself down for faults it recovered from.
//
// Delete that one line and this test goes red; the test above stays green.
func TestASuccessfulReadKeepsTheDeadLetterStoreLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	c := pacingConsumer(t, reader)

	c.loop()

	if reader.Reads != eofAt {
		t.Fatalf("the dead-letter store loop stopped after %d reads even though every failure "+
			"was followed by a successful read; it should have run to its reader's EOF at %d",
			reader.Reads, eofAt)
	}
}

// intermittentReader fails every other read and otherwise returns an empty message, which
// handle() drops as having no parseable tenant.
type intermittentReader struct {
	msgtest.FailingReader
	n int
}

func (r *intermittentReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.n++
	if r.n%2 == 1 || r.Reads+1 >= r.EOFAfter {
		return r.FailingReader.ReadMessage(ctx)
	}
	r.Reads++
	return messaging.Message{}, nil
}
