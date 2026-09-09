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
// instantly and keeps returning — a broker refusing fetches, a revoked credential, a
// subscription the reader's own self-heal cannot rebuild — therefore became a spin that
// burned a core and wrote one log line per iteration. It never stopped, and the projection
// went stale behind a pod that reported ready throughout.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. A paced
// loop and a spinning one both merge nothing against a failing reader, so no assertion about
// state can tell them apart. This counts the reads, on a virtual clock, so the number is the
// loop's and not the machine's.

const readCap = 5000

func TestTheResolvedEventsLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{}
	sp := &StateProcessor{
		ResolvedEventsReader: reader,
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}

	stopped := false
	for i := 0; i < readCap && !stopped; i++ {
		stopped = sp.ProcessMessage(context.Background())
	}
	if !stopped {
		t.Fatalf("the resolved-events loop read %d times against an error that never clears and "+
			"was still going: it burns a core and logs at full rate forever while the projection "+
			"goes stale behind a ready pod", reader.Reads)
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
// for faults it recovered from.
func TestASuccessfulReadKeepsTheResolvedEventsLoopRunning(t *testing.T) {
	sp := &StateProcessor{
		ResolvedEventsReader: &intermittentReader{},
		messages:             make(chan messaging.Message, 1000),
		readPacer:            core.NewReadPacer(nil, "resolved events").UseClock(core.VirtualClock()),
	}
	for i := 0; i < 400; i++ {
		if sp.ProcessMessage(context.Background()) {
			t.Fatalf("the resolved-events loop stopped at read %d even though every failure was "+
				"followed by a successful read", i+1)
		}
	}
}

// intermittentReader fails every other read and otherwise returns an empty message.
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
