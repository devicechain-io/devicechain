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

// 🔴 WHAT THIS FILE IS FOR. The gateway capture read loop named the hot-spin hazard in its
// own comment, on the EOF branch — and then, one branch further down, logged a non-EOF error
// and read again immediately. The reader's self-heal covers empty fetches and a deleted
// consumer; anything else (a broker refusing fetches, a revoked credential, a subscription
// it cannot rebuild) arrives unchanged on every iteration and returns instantly while it
// does, which is the same spin the branch above refuses to allow.
//
// 🔑 A COMMENT THAT ASSERTS THE INVARIANT IS NOT THE THING THAT HOLDS IT. What holds it is
// the pacer, and what pins the pacer is a count: a paced loop and a spinning one both ingest
// nothing against a failing reader, so the read RATE is the only observable difference.
//
// The loop here is driven as a whole (it is a goroutine body, not a per-message call), so
// the reader is given an EOF far past any paced loop's reach. A paced loop never sees it; an
// unpaced one ends there, and the assertion reports the number instead of hanging the run.

const readCap = 5000

func TestTheGatewayCaptureLoopStopsInsteadOfSpinningOnAnUnclearableError(t *testing.T) {
	reader := &msgtest.FailingReader{EOFAfter: readCap}
	es := &GatewayJetStreamSource{
		Id:        "gw-pacing",
		reader:    reader,
		readPacer: core.NewReadPacer(nil, "gateway capture").UseClock(core.VirtualClock()),
	}

	es.readLoop(context.Background(), make(chan struct{}))

	if reader.Reads >= readCap {
		t.Fatalf("the gateway capture loop read %d times against an error that never clears and "+
			"only stopped because the test's reader ran out: it burns a core and logs at full "+
			"rate while the source ingests nothing behind a ready pod", reader.Reads)
	}
	if reader.Reads > 40 {
		t.Fatalf("the gateway capture loop took %d reads to stop; that is too many to be a paced "+
			"retry", reader.Reads)
	}
	t.Logf("stopped after %d reads", reader.Reads)
}

// 🔑 THE COUNTERWEIGHT. The test above is satisfied by a loop that stops on the FIRST error,
// which would turn every transient broker hiccup into a source that ingests nothing again.
// This pins that a successful read keeps the loop running AND clears the run of failures —
// the reset without which one error an hour accumulates, across a day, into a service that
// tears itself down for faults it recovered from. The loop here reaches its reader's EOF,
// which is the only way it is allowed to end.
func TestASuccessfulReadKeepsTheGatewayCaptureLoopRunning(t *testing.T) {
	const eofAt = 800
	reader := &intermittentReader{}
	reader.EOFAfter = eofAt
	es := &GatewayJetStreamSource{
		Id:        "gw-pacing",
		reader:    reader,
		readPacer: core.NewReadPacer(nil, "gateway capture").UseClock(core.VirtualClock()),
	}

	es.readLoop(context.Background(), make(chan struct{}))

	if reader.Reads != eofAt {
		t.Fatalf("the gateway capture loop stopped after %d reads even though every failure was "+
			"followed by a successful read; it should have run to its reader's EOF at %d",
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
