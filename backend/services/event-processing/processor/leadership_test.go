// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// leasedProcessor is a processor wired for leadership but with no broker behind it:
// the lease value is never dialled on any path these tests take, and the gate is
// driven directly. That is the point of the gate being a predicate rather than the
// lease's own signal — what a reader needs to know is "am I inside a term", and
// nothing about that question requires NATS. The lease MECHANICS (a watch-silent
// expiry, a takeover, the reader parking) are pinned against a real broker in
// core/messaging.
func leasedProcessor(t *testing.T) *ResolvedEventsProcessor {
	t.Helper()
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.Lease = &messaging.DistributedLease{}
	rp.Gate = NewTermGate()
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return rp
}

// 🔴 THE CHECKPOINT IS WHERE OWNERSHIP HAS TO BE CHECKED, and this is the assertion
// that says so.
//
// checkpoint() publishes derived events, commits the snapshot and ACKS. Doing any of
// those outside a held term is the split-brain write the lease exists to prevent —
// and the acks are the irreversible half: an acked message is gone from a stream
// nobody will re-read, whether or not the engine ever applied it.
//
// The refusal must leave the messages unacked so they redeliver to whoever does own
// the partition.
func TestACheckpointIsRefusedOutsideAHeldTerm(t *testing.T) {
	rp := leasedProcessor(t)
	ack := &fakeAck{}
	rp.handle(msgAt(t, 1, ack))

	if rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint() succeeded while no leadership term was held")
	}
	if ack.acks != 0 {
		t.Fatalf("a message was acked outside a held term (%d acks); it is now gone from a stream nobody re-reads", ack.acks)
	}
	snap, found, err := rp.Store.Load(context.Background(), rp.cfg.PartitionId)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if found {
		t.Fatalf("a snapshot was committed outside a held term: %+v", snap)
	}
	// The buffered ack must still be there: the refusal defers the checkpoint, it does
	// not discard the work. Dropping it here would lose the message just as surely as
	// acking it.
	if len(rp.pendingAcks) != 1 {
		t.Fatalf("pendingAcks = %d after a refused checkpoint, want the message still buffered for the retry", len(rp.pendingAcks))
	}
}

// The counterweight, and it is not optional: without it a checkpoint() hard-wired to
// return false would pass the test above and every other guard in this file.
func TestACheckpointIsAllowedInsideAHeldTerm(t *testing.T) {
	rp := leasedProcessor(t)
	rp.Gate.Enter(func() bool { return true })

	ack := &fakeAck{}
	rp.handle(msgAt(t, 1, ack))

	if !rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint() was refused while the term WAS held")
	}
	if ack.acks != 1 {
		t.Fatalf("acks = %d inside a held term, want 1", ack.acks)
	}
	if _, found, err := rp.Store.Load(context.Background(), rp.cfg.PartitionId); err != nil || !found {
		t.Fatalf("no snapshot was committed inside a held term (found=%v err=%v)", found, err)
	}
}

// A processor with no lease is not gated at all, and must checkpoint exactly as it
// did before ADR-070. This is the path every unit test and the scaffold take, so a
// gate that engaged by default would stop all of them — and, more to the point, a
// deployment that has not opted into leadership.
func TestAnUnleasedProcessorCheckpointsUngated(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	ack := &fakeAck{}
	rp.handle(msgAt(t, 1, ack))

	if !rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint() was refused on a processor that takes no lease")
	}
	if ack.acks != 1 {
		t.Fatalf("acks = %d without leadership, want 1", ack.acks)
	}
}

// 🔴 A LEASE WITHOUT A GATE IS THE FAIL-OPEN SHAPE THIS WHOLE CHANGE REMOVES,
// arrived at by a wiring slip rather than a design one: leadershipEnabled() is an
// AND, so the half-wired processor would take the partition and then consume,
// checkpoint and ack without ever checking ownership — behaving exactly like the
// code before the lease existed, while looking wired.
func TestALeaseWithNoGateRefusesToStart(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.Lease = &messaging.DistributedLease{}
	// Gate deliberately left nil.
	err := rp.ExecuteStart(context.Background())
	if err == nil {
		t.Fatal("ExecuteStart accepted a lease with no TermGate; the readers would have consumed ungoverned")
	}
	if rp.Gate != nil {
		t.Fatal("ExecuteStart invented a gate rather than refusing")
	}
}

// A gate is CLOSED before any term is entered, and closed again after one ends.
//
// The first half is what stops a reader draining messages in the window between
// process start and the first acquisition — a window that is not theoretical, since
// the readers are constructed several steps before the lease is.
func TestTheGateIsClosedOutsideATerm(t *testing.T) {
	g := NewTermGate()
	if g.Held() {
		t.Fatal("a freshly built gate reports a held term; readers would consume before the first acquisition")
	}
	held := true
	g.Enter(func() bool { return held })
	if !g.Held() {
		t.Fatal("the gate stayed closed after entering a term")
	}
	// The gate reports the TERM's own answer, not a snapshot of it taken at Enter: a
	// lease can lapse in the middle of a term and the gate has to say so.
	held = false
	if g.Held() {
		t.Fatal("the gate kept reporting held after the term's own predicate went false")
	}
	held = true
	g.Exit()
	if g.Held() {
		t.Fatal("the gate stayed open after the term ended")
	}
}

// 🔴 A TERM MUST NOT INHERIT THE PREVIOUS TERM'S BUFFERS.
//
// A term that ends holding buffered acks and undelivered detections runs
// finalCheckpoint — and that checkpoint can be REFUSED, by a lost holder or by the
// very outage that ended the term. What is left behind is then work belonging to a
// partition this replica no longer owns. Carrying it into the next term would ack,
// under a new ownership epoch, messages fetched under an old one, and would publish
// detections derived from a window the new term is about to replay for itself.
//
// Dropping them loses nothing: they were never acked, so they redeliver.
func TestATermStartsFromCleanBuffers(t *testing.T) {
	rp := leasedProcessor(t)
	rp.handle(msgAt(t, 1, &fakeAck{}))
	rp.handle(msgAt(t, 2, &fakeAck{}))
	rp.idleUncommitted = true
	// newTestProcessor builds by struct literal, so the fact channels are nil there.
	// Give them the production buffering: a nil channel would swallow the sends below
	// forever and this test would prove nothing about draining them.
	rp.ruleUpdates = make(chan ruleUpdate, 64)
	rp.armUpdates = make(chan armUpdate, 256)
	rp.attrUpdates = make(chan attrUpdate, 256)
	rp.fenceUpdates = make(chan fenceUpdate, 32)
	rp.ruleUpdates <- ruleUpdate{}
	rp.armUpdates <- armUpdate{}
	rp.attrUpdates <- attrUpdate{}
	rp.fenceUpdates <- fenceUpdate{}

	// Confirm the state really is dirty first, or the assertions below hold vacuously.
	if len(rp.pendingAcks) == 0 || !rp.dirty {
		t.Fatalf("the fixture did not leave residue behind (pendingAcks=%d dirty=%v); this test would prove nothing",
			len(rp.pendingAcks), rp.dirty)
	}

	rp.resetForTerm()

	if len(rp.pendingAcks) != 0 {
		t.Fatalf("pendingAcks = %d at term start; the new term would ack messages fetched under the old one", len(rp.pendingAcks))
	}
	if len(rp.pendingDets) != 0 {
		t.Fatalf("pendingDets = %d at term start; the new term would publish detections from a window it is about to replay", len(rp.pendingDets))
	}
	if rp.dirty {
		t.Fatal("dirty carried into the new term, so its first checkpoint would commit the old term's engine state")
	}
	if rp.idleUncommitted {
		t.Fatal("idleUncommitted carried into the new term, which would park its loop against an inflated frontier it never set")
	}
	for name, n := range map[string]int{
		"ruleUpdates":  len(rp.ruleUpdates),
		"armUpdates":   len(rp.armUpdates),
		"attrUpdates":  len(rp.attrUpdates),
		"fenceUpdates": len(rp.fenceUpdates),
	} {
		if n != 0 {
			t.Fatalf("%s still holds %d queued updates at term start; the new term reads the same durable projections from scratch", name, n)
		}
	}
}

// 🔴 A STALE CHECKPOINT MUST END LEADERSHIP, NOT MERELY THE TERM.
//
// The stale flag is never cleared — a writer whose checkpoint another has already
// passed has no legitimate future — so a supervisor that treated it as an ordinary
// term loss would go straight back and re-acquire the partition, then run a term
// whose every checkpoint is refused before it even starts. That is a pod reporting
// is_leader=1 and detect_live=1 while committing nothing and acking nothing, which
// is strictly worse than not holding the partition at all: it holds it AGAINST a
// healthy standby.
//
// Ending the supervisor drops both gauges, so the leaderless alert fires and the pod
// is replaced.
func TestAStaleCheckpointEndsLeadershipRatherThanJustTheTerm(t *testing.T) {
	ctx := context.Background()
	rp := leasedProcessor(t)
	rp.Gate.Enter(func() bool { return true })
	rp.supCtx, rp.supCancel = context.WithCancel(context.Background())
	rp.newTermContext()

	// Another writer is already ahead of us in the same partition.
	if err := rp.Store.Save(ctx, &model.DetectSnapshot{
		PartitionId: rp.cfg.PartitionId, StreamSeq: 100, Watermark: testBase, Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed the leading snapshot: %v", err)
	}
	rp.engine = detectcore.NewEngine(nil, 0)
	rp.handle(msgAt(t, 1, &fakeAck{}))

	if rp.checkpoint(ctx) {
		t.Fatal("a checkpoint behind another writer's was accepted")
	}
	if !rp.stale {
		t.Fatal("the refusal did not latch this writer as stale")
	}
	select {
	case <-rp.supCtx.Done():
	default:
		t.Fatal("leadership survived a stale checkpoint; the supervisor would re-acquire the partition and " +
			"run a term that can never commit, holding it against a healthy standby")
	}
	// And the term is over too, so the loops unwind rather than spinning inside a
	// supervisor that has given up.
	select {
	case <-rp.pctx().Done():
	default:
		t.Fatal("the term context survived a stale checkpoint")
	}
}

// 🔴 THERE ARE TWO PLACES THAT DISCOVER A STALE CHECKPOINT AND THE FIRST VERSION OF
// THIS WORK ONLY FIXED ONE.
//
// `Save` returning ErrStaleCheckpoint is one; `detectStaleOwner`'s idle-advance
// fence, which reads the committed sequence directly, is the other. Fixing only the
// first left the fence ending the TERM but not leadership — and because the flag
// never clears, the supervisor would re-acquire the partition, pay a full rebuild
// (binds, restore, catch-up, three views, a whole replay), have replay's own
// checkpoint refused on the flag, and count one term-build failure. Five of those,
// with detect_is_leader reading 1 throughout.
//
// This asserts the fence site ends leadership, which is what pins the two together.
func TestTheIdleAdvanceFenceAlsoEndsLeadership(t *testing.T) {
	ctx := context.Background()
	rp := leasedProcessor(t)
	rp.Gate.Enter(func() bool { return true })
	rp.supCtx, rp.supCancel = context.WithCancel(context.Background())
	rp.newTermContext()

	// Another writer has committed further than this engine has applied — the exact
	// condition the fence reads.
	if err := rp.Store.Save(ctx, &model.DetectSnapshot{
		PartitionId: rp.cfg.PartitionId, StreamSeq: 500, Watermark: testBase, Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed the leading snapshot: %v", err)
	}
	rp.engine = detectcore.NewEngine(nil, 0)

	if !rp.detectStaleOwner(ctx) {
		t.Fatal("the idle-advance fence did not detect a writer ahead of this one")
	}
	if !rp.stale {
		t.Fatal("the fence did not latch this writer as stale")
	}
	select {
	case <-rp.supCtx.Done():
	default:
		t.Fatal("the idle-advance fence ended the term but not leadership; the supervisor would re-acquire " +
			"and rebuild a term whose every checkpoint is refused before it starts")
	}
}

// The unleased path keeps its original behaviour: a stale refusal halts the loop and
// leaves the process alone. Nothing there took a partition, so nothing is being held
// from anyone — and every unit test and the scaffold run this way.
func TestAnUnleasedStaleWriterHaltsWithoutEndingTheProcess(t *testing.T) {
	ctx := context.Background()
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.supCtx, rp.supCancel = context.WithCancel(context.Background())
	rp.newTermContext()
	if err := rp.Store.Save(ctx, &model.DetectSnapshot{
		PartitionId: rp.cfg.PartitionId, StreamSeq: 500, Watermark: testBase, Payload: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rp.engine = detectcore.NewEngine(nil, 0)

	rp.haltStaleWriter()

	if !rp.stale {
		t.Fatal("the unleased path did not latch stale")
	}
	select {
	case <-rp.supCtx.Done():
		t.Fatal("a processor that takes no lease ended its own process on a stale refusal; it holds no " +
			"partition, so there is nothing for a replacement to take over")
	default:
	}
}

// heldLease is a partition that is permanently owned by SOMEBODY ELSE — the answer a warm
// standby gets, and the one answer a single process cannot produce against a real broker
// without a second process racing it for the same key.
type heldLease struct{ acquires atomic.Int64 }

func (l *heldLease) Acquire(string) (*messaging.Lease, error) {
	l.acquires.Add(1)
	return nil, messaging.ErrLeaseHeld
}

// Unclean, which is what an absent-but-unexplained key reads as. It is never reached here
// (nothing is acquired) but it is the honest answer for a partition somebody holds.
func (l *heldLease) PriorOwnerReleasedCleanly(string) bool { return false }

// TestAStandbyIsReadyWithoutHoldingThePartition is the slice's headline behaviour: a
// replica whose partition is taken reports STARTED and stands by, rather than blocking
// its own startup until the partition frees up.
//
// 🔴 THE ASSERTION IS NOT MERELY "START RETURNED". A start that returned because it never
// launched the supervisor would satisfy that, and would produce the worst possible pod:
// Ready, serving, and permanently incapable of taking the partition — which no alert can
// see, because detect_is_leader is read as a MAX across pods and the real leader keeps it
// at 1. So this also asserts that the standby is still ASKING (more than one acquisition
// attempt, i.e. the retry loop is alive) and that the term gate is still CLOSED, which is
// what keeps its readers parked and its checkpoint refused meanwhile.
func TestAStandbyIsReadyWithoutHoldingThePartition(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	lease := &heldLease{}
	rp.Lease = lease
	rp.Gate = NewTermGate()
	if err := rp.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	t.Cleanup(func() { _ = rp.ExecuteStop(context.Background()) })

	started := time.Now()
	if err := rp.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart on a replica whose partition is held: %v", err)
	}
	startCost := time.Since(started)

	// acquireBackoffMax is 5s and acquisition retries FOREVER, so a start that waits for
	// the partition does not return at all. A second is generous room for a start that
	// does not wait.
	if startCost > time.Second {
		t.Fatalf("ExecuteStart took %v on a replica whose partition is held by another; a "+
			"warm standby must report started and take the partition when it frees up", startCost)
	}
	if rp.Gate.Held() {
		t.Fatal("the term gate is OPEN on a replica that holds no lease; its readers would " +
			"consume from the leader's durables and its checkpoints would be admitted")
	}

	// Still asking. One attempt would be a supervisor that gave up; zero would be one that
	// never ran. acquireBackoffMin is 500ms, so a live loop reaches two well inside this.
	deadline := time.Now().Add(3 * time.Second)
	for lease.acquires.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := lease.acquires.Load(); got < 2 {
		t.Fatalf("the standby made %d acquisition attempts in 3s; it is not standing by, it "+
			"has stopped trying, and this pod will never take the partition no matter what "+
			"happens to the leader", got)
	}
}
