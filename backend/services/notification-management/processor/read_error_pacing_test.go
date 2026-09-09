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
// and keeps returning — a broker refusing fetches, a revoked credential, a subscription the
// reader's own self-heal cannot rebuild — therefore became a spin that burned a core and
// wrote one log line per iteration. It never stopped, and nobody was paged for any alarm in
// the meantime, behind a pod that reported ready.
//
// 🔑 HOW OFTEN THE LOOP ASKS IS THE ONLY THING THAT SEPARATES IT FROM A CORRECT ONE. Both
// notify nobody against a failing reader, so no assertion about notifications can tell them
// apart. This counts the reads, on a virtual clock.

const readCap = 5000

func TestTheAlarmEventsLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{}
	np := &NotificationProcessor{
		Reader:    reader,
		readPacer: core.NewReadPacer(nil, "alarm events").UseClock(core.VirtualClock()),
	}

	stopped := false
	for i := 0; i < readCap && !stopped; i++ {
		stopped = np.ProcessMessage(context.Background())
	}
	if !stopped {
		t.Fatalf("the alarm-events loop read %d times against an error that never clears and was "+
			"still going: it burns a core and logs at full rate forever while no alarm reaches "+
			"anyone and the pod reports ready", reader.Reads)
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
	np := &NotificationProcessor{
		Reader:    &intermittentReader{},
		messages:  make(chan messaging.Message, 1000),
		readPacer: core.NewReadPacer(nil, "alarm events").UseClock(core.VirtualClock()),
	}
	for i := 0; i < 400; i++ {
		if np.ProcessMessage(context.Background()) {
			t.Fatalf("the alarm-events loop stopped at read %d even though every failure was "+
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
