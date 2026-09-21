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

// 🔴 WHAT THIS FILE IS FOR. Seven read loops in this service treated every error that was
// not io.EOF the same way: log it, wait a fixed 200ms, read again, forever. They shared one
// constant, which is how they came to share one defect. The fixed pause takes the hot-spin
// off the table and leaves the other failure mode untouched — an error that is never going
// to clear (a 409 from a stream at its MaxAckPending ceiling, a consumer whose leadership
// keeps moving, a subscription the reader hands back rather than rebuilding) now spins
// slowly instead of quickly, and the pod goes on reporting ready while consuming nothing.
//
// 🔑 AND THIS IS THE SERVICE WHERE THAT COSTS THE MOST, because the loops are not
// interchangeable. A wedged rule-updates consumer leaves DETECT evaluating the rule set it
// had at startup — every rule edited, published or retired since is simply not in effect,
// and the console goes on reporting them as live. A wedged roster or entity-deleted
// consumer leaves the armer arming devices that have been removed. None of that reports
// itself: the loop logs a line per retry and the pod stays Ready.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. A
// paced loop and a spinning one process exactly the same nothing against a failing reader,
// so no assertion about the projections can tell them apart. These count the READS, on a
// virtual clock, so the number is the loop's and not the machine's.
//
// The loops are driven whole (they are goroutine bodies, not per-message calls), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.

const readCap = 5000

// pacedReads is the most a paced loop may take to give up. The budget is two minutes of
// virtual time against a backoff starting at 200ms and doubling to a 5s ceiling, which is
// about thirty reads; the margin is for the backoff constants moving without this file
// having to.
const pacedReads = 60

// pacingProcessor assembles a processor with nothing but the parts a read loop touches: a
// live term context and a pacer factory on a virtual clock, so the budget is spent in
// arithmetic rather than in wall-clock seconds.
//
// Microservice is left nil deliberately. A pacer built on it would report an exhausted
// budget by ending the PROCESS, which here is the test binary. That the report happens at
// all is core's to prove and it does, in readpacer_test.go; what belongs here is that the
// loop stops asking.
func pacingProcessor(t *testing.T) *ResolvedEventsProcessor {
	t.Helper()
	rp := &ResolvedEventsProcessor{
		newPacer: func(what string) *core.ReadPacer {
			return core.NewReadPacer(nil, what).UseClock(core.VirtualClock())
		},
	}
	rp.procCtx, rp.procCancel = context.WithCancel(context.Background())
	t.Cleanup(rp.procCancel)
	return rp
}

// pacedConsumers is every read loop on this processor, with what it takes to run one.
// Adding a loop here is how it joins the test below; the repository-wide guard in
// backend/core/test is what makes forgetting to a failure rather than an omission.
var pacedConsumers = []struct {
	name   string
	attach func(*ResolvedEventsProcessor, messaging.MessageReader)
	run    func(*ResolvedEventsProcessor)
}{
	{
		"rule updates",
		func(rp *ResolvedEventsProcessor, r messaging.MessageReader) { rp.RuleUpdatesReader = r },
		func(rp *ResolvedEventsProcessor) { rp.readerWG.Add(1); rp.runRuleConsumer() },
	},
	{
		"device roster",
		func(rp *ResolvedEventsProcessor, r messaging.MessageReader) { rp.RosterReader = r },
		func(rp *ResolvedEventsProcessor) { rp.readerWG.Add(1); rp.runRosterConsumer() },
	},
	{
		"entity deletions",
		func(rp *ResolvedEventsProcessor, r messaging.MessageReader) { rp.EntityDeletedReader = r },
		func(rp *ResolvedEventsProcessor) { rp.readerWG.Add(1); rp.runEntityDeletedConsumer() },
	},
	{
		"device attributes",
		func(rp *ResolvedEventsProcessor, r messaging.MessageReader) { rp.AttributeReader = r },
		func(rp *ResolvedEventsProcessor) { rp.readerWG.Add(1); rp.runAttributeConsumer() },
	},
	{
		"geofence sets",
		func(rp *ResolvedEventsProcessor, r messaging.MessageReader) { rp.FenceSetReader = r },
		(*ResolvedEventsProcessor).drainFenceSetStream,
	},
	{
		// The pump is the odd one: it hands each read (and each error) to the single-writer
		// loop over a channel and retries by falling off the end of its body rather than by
		// `continue`. It is in this table because that difference is exactly what made it
		// the loop a first draft of the repository guard reported as clean.
		"resolved events",
		func(rp *ResolvedEventsProcessor, r messaging.MessageReader) { rp.ResolvedEventsReader = r },
		func(rp *ResolvedEventsProcessor) {
			items, done := make(chan readItem), make(chan struct{})
			go func() {
				for range items { // drain, so the pump is never blocked on the loop
				}
			}()
			rp.readPump(items, done)
			<-done
			close(items)
		},
	},
}

// 🔴 THE NEGATIVE CONTROL. Every loop below retried an unclearable read for as long as the
// pod lived; a check is worth nothing until it has been shown to fail, and deleting the
// PauseAfterError call from any one of these turns its case red here.
func TestEveryEventProcessingReadLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	for _, c := range pacedConsumers {
		t.Run(c.name, func(t *testing.T) {
			reader := &msgtest.FailingReader{EOFAfter: readCap}
			rp := pacingProcessor(t)
			c.attach(rp, reader)

			c.run(rp)

			if reader.Reads >= readCap {
				t.Fatalf("the %s loop read %d times against an error that never clears and only "+
					"stopped because the test's reader ran out: it retries forever behind a ready "+
					"pod that is consuming nothing from this stream", c.name, reader.Reads)
			}
			if reader.Reads > pacedReads {
				t.Fatalf("the %s loop took %d reads to stop, which is too many to be a paced retry",
					c.name, reader.Reads)
			}
			t.Logf("%s stopped after %d reads", c.name, reader.Reads)
		})
	}
}

// 🔑 THE COUNTERWEIGHT, AND IT GUARDS THE RISKIEST LINE IN THE CHANGE. The test above is
// satisfied by a loop that stops on the FIRST error, which would turn every broker hiccup
// into a dead consumer and a restarting pod. What prevents that is the Succeeded() call on
// the success path and nothing else: without it the run of failures is never cleared, so a
// service taking one blip every few minutes accumulates them across an uptime into a
// give-up it never earned.
//
// Delete Succeeded() from any loop and its case here goes red while the test above stays
// green. That pair is the whole measurement.
func TestASuccessfulReadKeepsAnEventProcessingLoopRunning(t *testing.T) {
	const eofAt = 800
	for _, c := range pacedConsumers {
		t.Run(c.name, func(t *testing.T) {
			reader := &intermittentReader{}
			reader.EOFAfter = eofAt
			rp := pacingProcessor(t)
			c.attach(rp, reader)

			c.run(rp)

			if reader.Reads < eofAt {
				t.Fatalf("the %s loop gave up after %d reads while every other read was "+
					"succeeding; a loop that does not clear its failure run tears the process "+
					"down for faults it already recovered from", c.name, reader.Reads)
			}
		})
	}
}

// intermittentReader fails every other read and succeeds in between, which is a stream
// having a bad time rather than a broken one. It ends at EOFAfter so a loop that never
// gives up still terminates the test.
//
// 🔑 THE EOFAfter GUARD IS LOAD-BEARING AND EASY TO DROP. Without the "> 0" the comparison
// Reads+1 >= EOFAfter is TRUE for an unset EOFAfter, so every read would fail and this fake
// would quietly become a second copy of FailingReader — making the counterweight above pass
// for the wrong reason, which is the one outcome a counterweight must not do.
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
