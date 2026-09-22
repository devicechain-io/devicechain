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
// burned a core and wrote one log line per iteration. It never stopped: persistence lost the
// tail of every tenant's history and the anchors of deleted entities piled up, behind a pod
// that reported ready throughout.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. Both
// persist nothing against a failing reader, so no assertion about rows can tell them apart.
// These count the reads, on a virtual clock.

const readCap = 5000

// Each loop is driven as a whole (it is a goroutine body, not a per-message call), so the
// reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.
//
// 🔑 WHAT THESE STILL TEST NOW THE LOOP ITSELF LIVES IN messaging.RunConsumer: the WIRING —
// that each consumer drives the shared loop with a pacer of its own. That is the
// "constructed correctly, connected to nothing" class a test of the library alone cannot see.
func TestThePersistenceLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	eproc := &EventPersistenceProcessor{
		ResolvedEventsReader: reader,
		messages:             make(chan messaging.Message, readCap),
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}

	eproc.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the persistence loop read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it burns a core and logs at full rate "+
			"forever while every tenant's history stops behind a ready pod", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the persistence loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

func TestTheAnchorReconcilerStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	r := &EntityAnchorReconciler{
		Reader:    reader,
		readPacer: core.NewReadPacer(nil, "entity-deleted").UseClock(core.VirtualClock()),
	}

	r.readLoop(context.Background())

	if reader.Reads >= readCap {
		t.Fatalf("the anchor reconciler read %d times against an error that never clears and only "+
			"stopped because the test's reader ran out: it burns a core and logs at full rate "+
			"forever while deleted entities keep their anchors behind a ready pod", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the anchor reconciler took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT. Both tests above are satisfied by a loop that stops on the FIRST
// error, which would turn every transient broker hiccup into a dead consumer. This pins that
// a successful read keeps each loop running AND clears the run of failures — the reset
// without which one error an hour accumulates, across a day, into a service that tears
// itself down for faults it recovered from. Each loop here reaches its reader's EOF, which
// is the only way it is allowed to end.
func TestASuccessfulReadKeepsBothLoopsRunning(t *testing.T) {
	const eofAt = 800

	persistReader := &intermittentReader{}
	persistReader.EOFAfter = eofAt
	eproc := &EventPersistenceProcessor{
		ResolvedEventsReader: persistReader,
		messages:             make(chan messaging.Message, eofAt),
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}

	eproc.readLoop(context.Background())

	if persistReader.Reads != eofAt {
		t.Fatalf("the persistence loop stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
			persistReader.Reads, eofAt)
	}

	anchorReader := &intermittentReader{}
	anchorReader.EOFAfter = eofAt
	r := &EntityAnchorReconciler{
		Reader:    anchorReader,
		readPacer: core.NewReadPacer(nil, "entity-deleted").UseClock(core.VirtualClock()),
	}

	r.readLoop(context.Background())

	if anchorReader.Reads != eofAt {
		t.Fatalf("the anchor reconciler stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
			anchorReader.Reads, eofAt)
	}
}

// intermittentReader fails every other read and otherwise returns an empty message, which
// both loops drop as having no parseable tenant.
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
