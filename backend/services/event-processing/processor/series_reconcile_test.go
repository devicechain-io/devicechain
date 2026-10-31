// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
)

// restoredWithLatch returns a processor whose engine was RESTORED from a snapshot taken while
// "dev1" held a raised latch at testBase: the state a crash between a delete fact's ack and the next
// checkpoint, a leadership change with a dropped signal, or a delete while down leaves behind.
func restoredWithLatch(t *testing.T) (*ResolvedEventsProcessor, string) {
	t.Helper()
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rules := []detectcore.Rule{{ID: id, Kind: detectcore.Threshold, Op: detectcore.LT, Thresh: 20}}
	before := detectcore.NewEngine(rules, 0)
	before.ProcessEvent(detectcore.Event{Seq: 1, Key: detectcore.SeriesKey{Rule: id, Series: "dev1"},
		Time: testBase, Value: 12, HasValue: true, Match: true})
	if got := before.Drain(); len(got) != 1 {
		t.Fatalf("setup: first reading must raise: %+v", got)
	}
	payload, err := before.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored, err := detectcore.Restore(rules, 0, payload)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = restored
	rp.RosterStore = newProcRosterStore(t)
	return rp, id
}

func reads(rp *ResolvedEventsProcessor, id string, seq uint64, at time.Time) []detectcore.Detection {
	rp.engine.ProcessEvent(detectcore.Event{Seq: seq, Key: detectcore.SeriesKey{Rule: id, Series: "dev1"},
		Time: at, Value: 12, HasValue: true, Match: true})
	return rp.engine.Drain()
}

// A device deleted while the latch was checkpointed (delete while down, or a lost signal): the
// roster tombstone alone makes the restored latch stale.
func TestReconcileDetectionStateDropsLatchOfDeletedDevice(t *testing.T) {
	rp, id := restoredWithLatch(t)
	if err := rp.RosterStore.Delete(context.Background(), "acme", "dev1", testBase.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := rp.reconcileDetectionState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !rp.dirty {
		t.Fatal("a drop must dirty the loop so the next checkpoint persists it")
	}
	if got := reads(rp, id, 2, testBase.Add(2*time.Minute)); len(got) != 1 || got[0].Edge != detectcore.EdgeRaised {
		t.Fatalf("a reading under the reused token must raise: %+v", got)
	}
}

// Delete AND recreate while down: the roster row is live again, but its incarnation began after the
// latch was raised, so the latch belongs to the previous device.
func TestReconcileDetectionStateDropsLatchPredatingRecreate(t *testing.T) {
	rp, id := restoredWithLatch(t)
	if err := rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
		Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := rp.reconcileDetectionState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := reads(rp, id, 2, testBase.Add(time.Hour+time.Minute)); len(got) != 1 || got[0].Edge != detectcore.EdgeRaised {
		t.Fatalf("the recreated device must raise on its first matching reading: %+v", got)
	}
}

// The negative control: a live device whose incarnation PREDATES the latch keeps it (no re-raise),
// and a series the roster never heard of is left alone.
func TestReconcileDetectionStateKeepsCurrentIncarnationLatch(t *testing.T) {
	rp, id := restoredWithLatch(t)
	if err := rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
		Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := rp.reconcileDetectionState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rp.dirty {
		t.Fatal("nothing was stale, nothing should be dirtied")
	}
	if got := reads(rp, id, 2, testBase.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("a steady breach on the same incarnation must not re-raise: %+v", got)
	}
}

// A delete fact redelivered after the device was recreated is rejected by the roster's monotonic
// guard, so the loop's drop finds a live row of a newer incarnation and must leave that device's
// own latch alone.
func TestApplySeriesDropIgnoresStaleDeleteAfterRecreate(t *testing.T) {
	rp, id := restoredWithLatch(t)
	ctx := context.Background()
	// Old incarnation tombstoned, then recreated BEFORE the latch below was raised.
	if err := rp.RosterStore.Delete(ctx, "acme", "dev1", testBase.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := rp.RosterStore.Upsert(ctx, &model.DeviceRoster{
		Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// The stale delete (older than the recreate) is redelivered: the guard rejects it.
	if err := rp.RosterStore.Delete(ctx, "acme", "dev1", testBase.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rp.procCtx = ctx
	rp.applySeriesDrop(seriesDrop{tenant: "acme", deviceToken: "dev1"})
	if rp.dirty {
		t.Fatal("a stale delete must not drop the newer incarnation's state")
	}
	if got := reads(rp, id, 2, testBase.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("the new device's latch must survive a stale delete: %+v", got)
	}
}
