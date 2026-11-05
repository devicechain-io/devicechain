// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// orderWriter records each derived edge together with whether the DETECT snapshot was already
// committed when it was written, which is the deliver-before-checkpoint order under test.
type orderWriter struct {
	edgeWriter
	store interface {
		LoadCommittedSeq(context.Context, string) (int64, bool, error)
	}
	partition    string
	savedAtWrite []bool
}

func (w *orderWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	_, saved, _ := w.store.LoadCommittedSeq(ctx, w.partition)
	for range msgs {
		w.savedAtWrite = append(w.savedAtWrite, saved)
	}
	return w.edgeWriter.WriteMessages(ctx, msgs...)
}
func (w *orderWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

// disarmedByRecheckProc is raisedAbsenceProc after the device's roster row is gone and the loop has
// run the REAL applyArmRecheck: the engine holds an undrained Resolved, no message has arrived since,
// and nothing else is pending.
func disarmedByRecheckProc(t *testing.T) (*ResolvedEventsProcessor, *orderWriter) {
	t.Helper()
	rp, _ := raisedAbsenceProc(t)
	w := &orderWriter{store: rp.Store, partition: rp.cfg.PartitionId}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	rp.RosterStore = newProcRosterStore(t) // no row: the device reads as departed
	rp.dirty = false
	rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"})
	if len(rp.pendingDets) != 0 || len(w.got) != 0 {
		t.Fatalf("setup: the recheck must leave the Resolved in the engine, got pending=%d published=%v", len(rp.pendingDets), w.got)
	}
	return rp, w
}

func assertResolvedBeforeSnapshot(t *testing.T, rp *ResolvedEventsProcessor, w *orderWriter) {
	t.Helper()
	if len(w.got) != 1 || w.got[0] != "dev1:resolved" {
		t.Fatalf("the disarm's Resolved was not published, got %v", w.got)
	}
	if w.savedAtWrite[0] {
		t.Fatal("the Resolved was published AFTER the snapshot committed")
	}
	if _, saved, _ := rp.Store.LoadCommittedSeq(context.Background(), rp.cfg.PartitionId); !saved {
		t.Fatal("the checkpoint committed no snapshot of the cleared latch")
	}
}

// TestCheckpointPublishesDisarmResolvedWithNoMessage: a disarm queues a Resolved in the engine; with
// no message after it, a checkpoint must drain and publish it before the snapshot that carries the
// cleared latch, or the alarm stays raised forever.
func TestCheckpointPublishesDisarmResolvedWithNoMessage(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	rp.dirty = true // other state changed, so the checkpoint commits
	if !rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint deferred")
	}
	assertResolvedBeforeSnapshot(t, rp, w)
}

// TestFinalCheckpointPublishesDisarmResolved: the term-end flush, which used to return early when
// the loop was clean and nothing was buffered.
func TestFinalCheckpointPublishesDisarmResolved(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	rp.finalCheckpoint()
	assertResolvedBeforeSnapshot(t, rp, w)
}

// TestCheckpointDrainDoesNotRepublish: a second checkpoint publishes nothing further.
func TestCheckpointDrainDoesNotRepublish(t *testing.T) {
	rp, w := disarmedByRecheckProc(t)
	rp.finalCheckpoint()
	rp.finalCheckpoint()
	rp.checkpoint(context.Background())
	if len(w.got) != 1 {
		t.Fatalf("want exactly one Resolved, got %v", w.got)
	}
}
