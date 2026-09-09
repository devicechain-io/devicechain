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
// burned a core and wrote one log line per iteration. It never stopped: persistence lost the
// tail of every tenant's history and the anchors of deleted entities piled up, behind a pod
// that reported ready throughout.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. Both
// persist nothing against a failing reader, so no assertion about rows can tell them apart.
// These count the reads, on a virtual clock.

const readCap = 5000

func TestThePersistenceLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{}
	eproc := &EventPersistenceProcessor{
		ResolvedEventsReader: reader,
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}

	stopped := false
	for i := 0; i < readCap && !stopped; i++ {
		stopped = eproc.ProcessMessage(context.Background())
	}
	if !stopped {
		t.Fatalf("the persistence loop read %d times against an error that never clears and was "+
			"still going: it burns a core and logs at full rate forever while every tenant's "+
			"history stops behind a ready pod", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the persistence loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

func TestTheAnchorReconcilerStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{}
	r := &EntityAnchorReconciler{
		Reader:    reader,
		readPacer: core.NewReadPacer(nil, "entity-deleted").UseClock(core.VirtualClock()),
	}

	stopped := false
	for i := 0; i < readCap && !stopped; i++ {
		stopped = r.processOne(context.Background())
	}
	if !stopped {
		t.Fatalf("the anchor reconciler read %d times against an error that never clears and was "+
			"still going: it burns a core and logs at full rate forever while deleted entities "+
			"keep their anchors behind a ready pod", reader.Reads)
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
// itself down for faults it recovered from.
func TestASuccessfulReadKeepsBothLoopsRunning(t *testing.T) {
	eproc := &EventPersistenceProcessor{
		ResolvedEventsReader: &intermittentReader{},
		messages:             make(chan messaging.Message, 1000),
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}
	for i := 0; i < 400; i++ {
		if eproc.ProcessMessage(context.Background()) {
			t.Fatalf("the persistence loop stopped at read %d even though every failure was "+
				"followed by a successful read", i+1)
		}
	}

	r := &EntityAnchorReconciler{
		Reader:    &intermittentReader{},
		readPacer: core.NewReadPacer(nil, "entity-deleted").UseClock(core.VirtualClock()),
	}
	for i := 0; i < 400; i++ {
		if r.processOne(context.Background()) {
			t.Fatalf("the anchor reconciler stopped at read %d even though every failure was "+
				"followed by a successful read", i+1)
		}
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
	if r.n%2 == 1 {
		return r.FailingReader.ReadMessage(ctx)
	}
	return messaging.Message{}, nil
}
