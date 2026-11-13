// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// A broker that will not take the publish.
type stuckWriter struct{ started chan struct{} }

func (w *stuckWriter) WriteToDevice(ctx context.Context, _ string, _ ...messaging.Message) error {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestASlowSettlerStopsWellBeforeItsLeaseRunsOut(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	w := &stuckWriter{started: make(chan struct{}, 1)}
	r.f = NewAckFeed(&nopReader{}, w, r.pend, r.m, AckFeedOptions{
		Workers: 1, ReadPacer: core.NewReadPacer(nil, "test"), Now: func() time.Time { return r.now },
		ResponseAttempts: 5, ResponseBackoff: time.Millisecond, SettleBudget: 50 * time.Millisecond,
	})
	start := time.Now()
	a := r.deliver(t, report("ack", true), 1)
	require.Less(t, time.Since(start), 2*time.Second, "the answer is bounded by the budget, not by the broker")
	require.False(t, a.acked, "left to redeliver")
	require.Equal(t, 1, r.m.get(AckResponseFail))
	require.Equal(t, 1, r.pend.size())
	f := NewAckFeed(&nopReader{}, w, r.pend, nil, AckFeedOptions{ReadPacer: core.NewReadPacer(nil, "t"), SettleBudget: time.Hour})
	require.Less(t, f.opts.SettleBudget, SettleLease/2+1, "a budget past the lease would let two settlers answer")
}

// A settler overtaken after its lease expired answers (the budget makes that rare) but cannot delete the
// record the overtaker now holds.
func TestAnOvertakenSettlersDeleteLeavesTheNewHoldersRecord(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	r.resp.onWrite = func() { r.pend.lease[1] = r.now.Add(time.Hour) } // someone else took the lease meanwhile
	a := r.deliver(t, report("ack", true), 1)
	require.True(t, a.acked)
	require.Equal(t, 1, r.pend.heldDeleteRefused)
	require.Equal(t, 1, r.pend.size(), "the overtaker's record is not deleted from under it")
}

func TestARedeliveredReportForADownlinkThisReplicaJustSettledIsNotCountedUnmatched(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	r.deliver(t, report("ack", true), 1)
	require.Len(t, r.resp.envs, 1)
	// The JetStream ack was lost, so the same report arrives again.
	a := r.deliver(t, report("ack", true), 2)
	require.True(t, a.acked)
	require.Equal(t, 1, r.m.get(AckRedelivered))
	require.Zero(t, r.m.get(AckUnmatched), "an unmatched alert here would be a false alarm")
	require.Len(t, r.resp.envs, 1, "answered once")

	// A report for a downlink this replica never settled is still unmatched.
	other := report("ack", true)
	other.QueueItemID = "6e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f22"
	r.deliver(t, other, 2)
	require.Equal(t, 1, r.m.get(AckUnmatched))

	// The memory ends: after its window the same report is unmatched like any other.
	r.now = r.now.Add(recentSettledTTL + time.Second)
	r.deliver(t, report("ack", true), 2)
	require.Equal(t, 2, r.m.get(AckUnmatched))
}

// A worker paused between claiming and answering has little or none of the lease left: it must not
// answer, or it could publish a different outcome from the settler that took the lease after it.
func TestAWorkerThatWasPausedPastItsLeaseDoesNotAnswer(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	p, err := r.pend.Lookup(context.Background(), "acme", itemID)
	require.NoError(t, err)
	token := r.now.Add(SettleLease)
	r.now = r.now.Add(SettleLease - settleMargin + time.Second) // the clock moved on while it was paused
	require.False(t, r.f.respond(context.Background(), p, cmddownlinkSuccess(p), token))
	require.Empty(t, r.resp.envs, "an answer past the lease is not published")
	require.Equal(t, 1, r.m.get(AckContended))
}

func cmddownlinkSuccess(p *Pending) cmddownlink.ResponseEnvelope {
	return cmddownlink.ResponseEnvelope{CommandToken: p.CommandToken, DispatchNonce: p.DispatchNonce, Success: true}
}
