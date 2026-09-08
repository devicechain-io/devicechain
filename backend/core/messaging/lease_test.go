// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
)

// TestLeaseAcquireIsMutuallyExclusive pins the core single-owner guarantee: the
// first Acquire wins, a second for the same partition gets ErrLeaseHeld (the warm
// standby signal), and a DIFFERENT partition is independent.
func TestLeaseAcquireIsMutuallyExclusive(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}

	a, err := dl.Acquire("detect:tenant-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, err := dl.Acquire("detect:tenant-1"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second Acquire of a held partition = %v, want ErrLeaseHeld", err)
	}
	// A different partition is not blocked by the first.
	if _, err := dl.Acquire("detect:tenant-2"); err != nil {
		t.Fatalf("Acquire of a distinct partition: %v", err)
	}
	// After release the partition is acquirable again.
	if err := a.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := dl.Acquire("detect:tenant-1"); err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
}

// TestLeaseEpochIsImmutableAcrossRenew is the M1 guard: Renew refreshes the lease
// and must ADVANCE the internal CAS revision (so repeated renewals keep
// succeeding) while keeping the advertised epoch PINNED at the acquire revision.
// A mutation that re-stamped epoch = the new revision, or that failed to advance
// the CAS revision, fails this test.
func TestLeaseEpochIsImmutableAcrossRenew(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("sparkplug")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	epoch := lease.Epoch()
	if epoch == 0 {
		t.Fatal("epoch must be a positive KV revision, got 0")
	}
	// Repeated renewals must each succeed — which they only do if the CAS revision
	// advances every time — and none may move the advertised epoch.
	for i := 0; i < 3; i++ {
		if err := lease.Renew(); err != nil {
			t.Fatalf("Renew #%d: %v (CAS revision did not advance?)", i+1, err)
		}
		if got := lease.Epoch(); got != epoch {
			t.Fatalf("Renew #%d moved the epoch: got %d, want the immutable acquire epoch %d", i+1, got, epoch)
		}
	}
}

// TestLeaseBucketAppliesTTL pins that the TTL actually reaches the bucket. A lease
// bucket created with TTL 0 reads as NO EXPIRY to JetStream, so a crashed owner
// would wedge its partition forever — the exact failure the fencing window
// exists to prevent.
func TestLeaseBucketAppliesTTL(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	ttl := 7 * time.Second
	dl, err := nmgr.NewDistributedLease(ttl)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	status, err := dl.kv.Status()
	if err != nil {
		t.Fatalf("bucket Status: %v", err)
	}
	if status.TTL() != ttl {
		t.Fatalf("lease bucket TTL = %v, want %v (a 0/absent TTL wedges a crashed owner forever)", status.TTL(), ttl)
	}
}

// TestLeaseBucketRejectsDivergentTTL pins the fail-closed guard for the shared
// bucket: the lease bucket has ONE TTL (the fencing window), so a second Class-3
// user requesting a different TTL must fail loudly rather than silently inherit
// the first's window. A same-TTL caller is fine.
func TestLeaseBucketRejectsDivergentTTL(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	if _, err := nmgr.NewDistributedLease(1 * time.Second); err != nil {
		t.Fatalf("first NewDistributedLease: %v", err)
	}
	// A different TTL against the now-existing bucket must be rejected.
	_, err := nmgr.NewDistributedLease(2 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "TTL") {
		t.Fatalf("divergent-TTL NewDistributedLease = %v, want an error naming the TTL mismatch", err)
	}
	// The same TTL is accepted (idempotent open).
	if _, err := nmgr.NewDistributedLease(1 * time.Second); err != nil {
		t.Fatalf("same-TTL NewDistributedLease: %v", err)
	}
}

// TestLeaseHolderFailsAfterTakeover is the self-eviction guard (M3). We simulate an
// expiry-plus-takeover deterministically by deleting the entry out from under the
// owner (what the TTL does on a crash) and letting a standby Acquire. The displaced
// owner's Renew CAS then fails — which is what KeepAlive converts into ErrNotHolder
// — and the standby genuinely owns the higher epoch and can renew.
func TestLeaseHolderFailsAfterTakeover(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	owner, err := dl.Acquire("detect:tenant-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := owner.Renew(); err != nil {
		t.Fatalf("Renew while held: %v", err)
	}

	// Simulate the TTL expiring the owner's entry, then a standby taking over.
	if err := dl.kv.Delete(kvKey("detect:tenant-1")); err != nil {
		t.Fatalf("simulate expiry (delete): %v", err)
	}
	standby, err := dl.Acquire("detect:tenant-1")
	if err != nil {
		t.Fatalf("standby Acquire after expiry: %v", err)
	}
	if standby.Epoch() <= owner.Epoch() {
		t.Fatalf("standby epoch %d must exceed the prior owner's %d (monotonic fence)", standby.Epoch(), owner.Epoch())
	}

	// The entry now carries a different holder uuid, so the displaced owner's CAS
	// renewal can no longer land — the failure KeepAlive turns into ErrNotHolder once
	// the validity window has elapsed.
	if err := owner.Renew(); err == nil {
		t.Fatal("displaced owner Renew succeeded, want a CAS failure")
	}
	// The standby genuinely holds it: the entry is its uuid, and its own CAS renewal
	// still lands (which the displaced owner's failure alone would not prove — a
	// broken KV would fail both).
	entry, err := dl.kv.Get(kvKey("detect:tenant-1"))
	if err != nil {
		t.Fatalf("read the lease entry after takeover: %v", err)
	}
	if string(entry.Value()) != standby.holder {
		t.Fatalf("lease entry holder = %q, want the standby's %q", entry.Value(), standby.holder)
	}
	if err := standby.Renew(); err != nil {
		t.Fatalf("standby Renew after takeover: %v", err)
	}
}

// TestKeepAliveRidesOutTransientFailureWithinWindow pins the ride-out half of the
// validity window, which TestKeepAliveReturnsOnLoss does not reach: it only shows
// that KeepAlive DOES report loss, never that it withholds that report while the
// window is still open.
//
// A NATS blip must not be read as lost ownership inside the window, because no
// standby can Acquire the partition until the server-side entry TTL-expires — so
// self-evicting on the first failed renewal would take a fleet of owners down on a
// shared broker hiccup while their partitions were still theirs. Only past the
// window is a transient failure definitive.
func TestKeepAliveRidesOutTransientFailureWithinWindow(t *testing.T) {
	nmgr, cleanup := newTestManager(t)

	// A long TTL keeps the ride-out assertion structural rather than raced: a correct
	// KeepAlive cannot return anywhere inside a 30s window, however slow the box.
	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		cleanup()
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("sparkplug")
	if err != nil {
		cleanup()
		t.Fatalf("Acquire: %v", err)
	}

	// Total NATS outage: every KV op now errors (a transient failure, not a takeover),
	// so every renewal from here on fails.
	cleanup()

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- lease.KeepAlive(ctx, 20*time.Millisecond) }()

	// Renewals are failing and we are inside the window: ownership is not yet lost.
	select {
	case kerr := <-done:
		t.Fatalf("KeepAlive returned %v while renewals were failing INSIDE the validity window: a "+
			"transient failure is not loss until the window elapses, and self-evicting here would "+
			"drop a partition no standby could have taken", kerr)
	case <-time.After(500 * time.Millisecond):
	}

	// Force the window to have elapsed. Now the same failing renewals ARE definitive.
	lease.mu.Lock()
	lease.lastRenew = time.Now().Add(-31 * time.Second)
	lease.mu.Unlock()

	select {
	case kerr := <-done:
		if !errors.Is(kerr, ErrNotHolder) {
			t.Fatalf("KeepAlive returned %v once the window had elapsed with no successful renewal, "+
				"want ErrNotHolder", kerr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("KeepAlive did not report loss after the validity window elapsed with every renewal failing")
	}
}

// TestLeaseReleaseIsRevisionChecked pins that a displaced owner's Release cannot
// drop the new owner's hold — the revision-checked delete makes it a no-op
// against a takeover, the same guard DistributedLock uses.
func TestLeaseReleaseIsRevisionChecked(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	owner, err := dl.Acquire("sparkplug")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := dl.kv.Delete(kvKey("sparkplug")); err != nil {
		t.Fatalf("simulate expiry: %v", err)
	}
	standby, err := dl.Acquire("sparkplug")
	if err != nil {
		t.Fatalf("standby Acquire: %v", err)
	}

	// The displaced owner releasing must NOT remove the standby's entry.
	_ = owner.Release() // a benign error here is fine; the invariant is the standby survives
	entry, err := dl.kv.Get(kvKey("sparkplug"))
	if err != nil {
		t.Fatalf("read the lease entry after the displaced owner's Release = %v, want the standby's "+
			"entry intact: a revision-unchecked release would have dropped the new owner's hold", err)
	}
	if string(entry.Value()) != standby.holder {
		t.Fatalf("lease entry holder after the displaced owner's Release = %q, want the standby's %q",
			entry.Value(), standby.holder)
	}
}

// TestLeaseReleaseIsIdempotent pins the shutdown/self-eviction ergonomics: Release
// twice is a no-op the second time, and a released lease reports not-held and
// cannot renew.
func TestLeaseReleaseIsIdempotent(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:tenant-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release should be a no-op, got %v", err)
	}
	if err := lease.Renew(); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("Renew after Release = %v, want ErrNotHolder", err)
	}
	if lease.stillValid() {
		t.Fatal("stillValid after Release = true, want false: a released lease is out of its window " +
			"immediately, so Holder.Held() reports not-held without waiting out the TTL")
	}
	// The partition is free again.
	if _, err := dl.Acquire("detect:tenant-1"); err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
}

// TestFenceRejectsStaleAdmitsCurrent is the M5 paired fence test: a stale-epoch
// write is REJECTED and a current-or-newer one ACCEPTED. It covers the equal-epoch
// case (the owner's own in-flight writes at a stable epoch, which MUST be admitted
// — this is why Renew preserves the epoch) and the post-handover case (the old
// owner's writes rejected once the new owner's higher epoch is seen), on
// independent partitions.
func TestFenceRejectsStaleAdmitsCurrent(t *testing.T) {
	f := NewFence()

	// First write from an owner at epoch 5 is admitted and sets the high-water.
	if err := f.RejectIfStale("p", 5); err != nil {
		t.Fatalf("first write at epoch 5 = %v, want admitted", err)
	}
	// The same owner's later write at the SAME epoch must still be admitted.
	if err := f.RejectIfStale("p", 5); err != nil {
		t.Fatalf("write at the current epoch 5 = %v, want admitted (owner's own in-flight stream)", err)
	}
	// A write carrying an OLDER epoch (a lagging duplicate) is rejected.
	if err := f.RejectIfStale("p", 4); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("write at stale epoch 4 = %v, want ErrStaleEpoch", err)
	}
	// A NEW owner at a higher epoch is admitted and advances the high-water.
	if err := f.RejectIfStale("p", 6); err != nil {
		t.Fatalf("write from new owner at epoch 6 = %v, want admitted", err)
	}
	// The OLD owner (epoch 5) waking from a GC pause after handover is now rejected.
	if err := f.RejectIfStale("p", 5); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("old owner write at epoch 5 after handover to 6 = %v, want ErrStaleEpoch", err)
	}
	// A different partition keeps its own high-water — unaffected by "p".
	if err := f.RejectIfStale("q", 1); err != nil {
		t.Fatalf("first write to an independent partition = %v, want admitted", err)
	}
}

// TestLeaseExpiresWithoutRenewal is the negative control that makes the KeepAlive
// test below non-vacuous: it proves the bucket TTL really does expire an
// un-renewed lease, so that "still held" under KeepAlive is a claim that could
// otherwise fail.
func TestLeaseExpiresWithoutRenewal(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(1 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	if _, err := dl.Acquire("detect:tenant-1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Without renewal the lease must age out and become acquirable again.
	time.Sleep(3 * time.Second)
	if _, err := dl.Acquire("detect:tenant-1"); err != nil {
		t.Fatalf("Acquire after TTL should succeed once the un-renewed lease expired, got %v", err)
	}
}

// TestKeepAliveKeepsLeaseAlivePastTTL proves KeepAlive actually renews: with the
// keep-alive running, the lease survives well past its TTL (a competing Acquire
// keeps getting ErrLeaseHeld), and KeepAlive returns nil on ctx cancel. Paired
// with the negative control above, this cannot pass vacuously.
func TestKeepAliveKeepsLeaseAlivePastTTL(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(1 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("sparkplug")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Interval 100ms against a 1s TTL is a 10x renewal margin, robust to CI jitter.
	go func() { done <- lease.KeepAlive(ctx, 100*time.Millisecond) }()

	// Well past the 1s TTL — renewal must have kept the lease held.
	time.Sleep(1500 * time.Millisecond)
	if _, err := dl.Acquire("sparkplug"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("competing Acquire while KeepAlive runs = %v, want ErrLeaseHeld (renewal should hold the lease)", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("KeepAlive returned %v on cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("KeepAlive did not return after ctx cancel")
	}
}

// TestKeepAliveReturnsOnLoss pins that KeepAlive surfaces a definitively lost lease
// (rather than looping forever). After a takeover its renewals keep failing, and
// once the validity window has elapsed it returns ErrNotHolder so the caller can
// self-evict. A short TTL keeps the window (and thus the test) brief.
func TestKeepAliveReturnsOnLoss(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(1 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:tenant-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- lease.KeepAlive(context.Background(), 100*time.Millisecond) }()

	// Force loss: delete our entry and let a standby take the partition, so every
	// subsequent Renew inside KeepAlive fails its CAS.
	if err := dl.kv.Delete(kvKey("detect:tenant-1")); err != nil {
		t.Fatalf("simulate loss: %v", err)
	}
	if _, err := dl.Acquire("detect:tenant-1"); err != nil {
		t.Fatalf("standby Acquire: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrNotHolder) {
			t.Fatalf("KeepAlive returned %v after ownership loss, want ErrNotHolder", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("KeepAlive did not return after the lease was lost")
	}
}

// TestLeaseConcurrentAccessIsRaceFree exercises the documented concurrency posture
// — one KeepAlive renewer alongside a term-gate loop calling Holder.Held() and
// Epoch, plus a Release — so `go test -race` can prove the mutexes actually cover
// the shared fields (the "safe for concurrent use" claim is otherwise pinned only
// by reading).
func TestLeaseConcurrentAccessIsRaceFree(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("sparkplug")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	holder, err := lease.WatchHolder(ctx)
	if err != nil {
		cancel()
		t.Fatalf("WatchHolder: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = lease.KeepAlive(ctx, 20*time.Millisecond) }()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = holder.Held()
				_ = lease.Epoch()
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)
	_ = lease.Release()
	cancel()
	wg.Wait()
}

// leaseRenewGateKV wraps a real KV store so a test can park an in-flight Renew at
// exactly the instant the renew/release ordering matters: the revision-checked
// Update has ALREADY been applied by the server (the entry is at a new revision)
// but Renew has not yet returned, so the Lease still holds the superseded
// revision. Every method other than Update is the real store's.
type leaseRenewGateKV struct {
	nats.KeyValue
	landed      chan struct{} // closed once the real Update has been applied
	landedOnce  sync.Once
	proceed     chan struct{} // closed by the test to let the parked Update return
	proceedOnce sync.Once
}

func (g *leaseRenewGateKV) Update(key string, value []byte, last uint64) (uint64, error) {
	rev, err := g.KeyValue.Update(key, value, last)
	g.landedOnce.Do(func() { close(g.landed) })
	<-g.proceed
	return rev, err
}

// letUpdateReturn unparks the Update. It is idempotent so a failing test can defer
// it without racing the success path's own call.
func (g *leaseRenewGateKV) letUpdateReturn() {
	g.proceedOnce.Do(func() { close(g.proceed) })
}

// TestReleaseWaitsForAnInFlightRenew is the ordering gate. Renew and Release both
// write under a CAS on the lease's own revision, and Renew necessarily performs its
// KV round trip with the revision it read BEFORE the write landed. So a Release
// that runs between the Update landing server-side and the new revision being
// stored would delete against a revision the server has already superseded: the CAS
// fails, the just-renewed entry survives, and the partition stays claimed by nobody
// for a full lease TTL before a standby can take it.
//
// Serializing that is the LEASE's job, not the caller's, and this pins it two ways:
//
//   - STRUCTURALLY — with an Update parked mid-flight, Release must not complete at
//     all. This is a widened window, not a raced one: the correct implementation can
//     never finish while the Update is parked, so the wait cannot flake; the
//     unserialized one finishes in microseconds.
//   - BY OUTCOME — once the renew completes, Release must actually REMOVE the entry,
//     so a successor acquires immediately instead of waiting out the TTL.
func TestReleaseWaitsForAnInFlightRenew(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	const partition = "detect:tenant-1"
	lease, err := dl.Acquire(partition)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	gate := &leaseRenewGateKV{
		KeyValue: lease.kv,
		landed:   make(chan struct{}),
		proceed:  make(chan struct{}),
	}
	lease.kv = gate
	defer gate.letUpdateReturn()

	renewDone := make(chan error, 1)
	go func() { renewDone <- lease.Renew() }()

	select {
	case <-gate.landed:
	case <-time.After(10 * time.Second):
		t.Fatal("the renew Update never reached the store")
	}

	// The entry is now at a revision the Lease has not stored yet — the exact state a
	// concurrent Release must not be allowed to observe.
	releaseDone := make(chan error, 1)
	releaseRunning := make(chan struct{})
	go func() {
		close(releaseRunning)
		releaseDone <- lease.Release()
	}()
	<-releaseRunning

	select {
	case rerr := <-releaseDone:
		t.Fatalf("Release completed (err=%v) while a renew Update was still in flight: it ran its "+
			"revision-checked delete against a revision the landed Update had already superseded, "+
			"which leaves the renewed entry to age out over a full TTL", rerr)
	case <-time.After(500 * time.Millisecond):
	}

	gate.letUpdateReturn()

	if rerr := <-renewDone; rerr != nil {
		t.Fatalf("Renew: %v", rerr)
	}
	if rerr := <-releaseDone; rerr != nil {
		t.Fatalf("Release once the renew had completed: %v (it must delete against the revision that "+
			"renew wrote, not the one it superseded)", rerr)
	}

	// Read through the REAL store, not the gate, and read the outcome rather than the
	// mechanism: the entry is gone.
	if _, gerr := dl.kv.Get(kvKey(partition)); !errors.Is(gerr, nats.ErrKeyNotFound) {
		t.Fatalf("lease entry after Release = %v, want ErrKeyNotFound: the release did not remove the "+
			"renewed entry, so the partition stays claimed until the entry expires", gerr)
	}
	// And the partition is immediately re-acquirable, which is the whole point.
	if _, aerr := dl.Acquire(partition); aerr != nil {
		t.Fatalf("successor Acquire straight after Release: %v", aerr)
	}
}

// TestReleaseWaitsForRenewToStoreItsRevision is the SECOND half of the ordering
// gate, and it exists because the first half cannot reach this interleaving.
//
// TestReleaseWaitsForAnInFlightRenew parks Renew inside the KV Update, which stages
// "Release begins while the Update is in flight". The renewMu span covers more than
// that: it runs from reading rev, through the write, to STORING the new revision. So
// there is a second losing interleaving — Release begins after Update has RETURNED
// but before the new revision has been stored — and a KV interposer structurally
// cannot park there, because by then the store has already handed control back.
//
// Left unpinned, an implementation that releases renewMu the moment Update returns
// and stores rev outside it passes the first test on scheduler luck alone, while
// reopening the original CAS race in a narrower window: a Release landing in it
// still deletes with the pre-renew revision, still loses the CAS, and still leaves
// the entry to age out over a full TTL. Same defect, smaller target.
func TestReleaseWaitsForRenewToStoreItsRevision(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	const partition = "detect:tenant-2"
	lease, err := dl.Acquire(partition)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	parked := make(chan struct{})
	proceed := make(chan struct{})
	var parkOnce, proceedOnce sync.Once
	letRenewStore := func() { proceedOnce.Do(func() { close(proceed) }) }
	defer letRenewStore()

	// Park after the Update has returned and before the new revision is stored — the
	// window the interposer cannot see.
	lease.afterUpdate = func() {
		parkOnce.Do(func() { close(parked) })
		<-proceed
	}

	renewDone := make(chan error, 1)
	go func() { renewDone <- lease.Renew() }()

	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		t.Fatal("Renew never reached the post-Update park point")
	}

	releaseDone := make(chan error, 1)
	releaseRunning := make(chan struct{})
	go func() {
		close(releaseRunning)
		releaseDone <- lease.Release()
	}()
	<-releaseRunning

	// The write has landed but the Lease has not stored its revision. A Release that
	// proceeds here deletes with the superseded revision.
	select {
	case rerr := <-releaseDone:
		t.Fatalf("Release completed (err=%v) after the renew Update returned but before the renew had "+
			"stored its new revision: renewMu must span the store as well as the write, or a Release "+
			"landing in that window deletes with the pre-renew revision", rerr)
	case <-time.After(500 * time.Millisecond):
	}

	letRenewStore()

	if rerr := <-renewDone; rerr != nil {
		t.Fatalf("Renew: %v", rerr)
	}
	if rerr := <-releaseDone; rerr != nil {
		t.Fatalf("Release once the renew had stored its revision: %v", rerr)
	}
	if _, gerr := dl.kv.Get(kvKey(partition)); !errors.Is(gerr, nats.ErrKeyNotFound) {
		t.Fatalf("lease entry after Release = %v, want ErrKeyNotFound: the release did not remove the "+
			"renewed entry, so the partition stays claimed until the entry expires", gerr)
	}
	if _, aerr := dl.Acquire(partition); aerr != nil {
		t.Fatalf("successor Acquire straight after Release: %v", aerr)
	}
}

// leaseDeleteGateKV parks Release inside the KV Delete, so a test can ask what the
// term gate answers while a release round trip is outstanding.
type leaseDeleteGateKV struct {
	nats.KeyValue
	parked      chan struct{}
	parkedOnce  sync.Once
	proceed     chan struct{}
	proceedOnce sync.Once
}

func (g *leaseDeleteGateKV) Delete(key string, opts ...nats.DeleteOpt) error {
	g.parkedOnce.Do(func() { close(g.parked) })
	<-g.proceed
	return g.KeyValue.Delete(key, opts...)
}

func (g *leaseDeleteGateKV) letDeleteReturn() {
	g.proceedOnce.Do(func() { close(g.proceed) })
}

// TestHeldDoesNotBlockOnARenewRoundTrip and TestHeldDoesNotBlockOnAReleaseRoundTrip
// are the pair that ENFORCES the "mu is the read lock" claim, rather than leaving it
// asserted in a comment. Holder.Held() is evaluated for every term-gated message and
// is documented never to block, so no KV round trip may run under mu. A Lease makes
// exactly two of them — Renew's revision-checked Update and Release's
// revision-checked Delete — and there is one test per round trip: park it, then
// require Held() to answer while it is outstanding. A third round trip added to the
// type needs a third case here, or it ships covered by review only.
//
// This one covers Renew, and it is the case the ordering gates cannot reach:
// TestReleaseWaitsForAnInFlightRenew also parks an Update, but the thing it watches
// (Release) is REQUIRED to block there on renewMu, so an Update that had moved under
// mu would still pass it. Held() takes mu and never renewMu, which is what makes it
// the probe that can tell the two apart.
//
// The lease TTL is deliberately long here: the point is that Held() answers while
// the renewal is outstanding, not what it answers.
func TestHeldDoesNotBlockOnARenewRoundTrip(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:tenant-4")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holder, err := lease.WatchHolder(ctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}

	gate := &leaseRenewGateKV{
		KeyValue: lease.kv,
		landed:   make(chan struct{}),
		proceed:  make(chan struct{}),
	}
	lease.kv = gate
	defer gate.letUpdateReturn()

	renewDone := make(chan error, 1)
	go func() { renewDone <- lease.Renew() }()

	select {
	case <-gate.landed:
	case <-time.After(10 * time.Second):
		t.Fatal("the renew Update never reached the store")
	}

	// The renewal is in flight. The gate predicate must still answer.
	held := make(chan bool, 1)
	go func() { held <- holder.Held() }()
	select {
	case <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("Holder.Held() blocked while a Renew was inside its KV Update: the term gate is " +
			"evaluated per message and must never wait on a round trip, so mu must not be held " +
			"across one — and a renewal happens on every KeepAlive tick, so this would stall reads " +
			"repeatedly for the life of the term")
	}

	gate.letUpdateReturn()
	if rerr := <-renewDone; rerr != nil {
		t.Fatalf("Renew: %v", rerr)
	}
}

// TestHeldDoesNotBlockOnAReleaseRoundTrip is the Release half of that pair (see
// above). Holding mu across the revision-checked delete would stall every gated read
// for as long as that request takes, up to the client's request timeout.
//
// The lease TTL is deliberately long here: the point is that Held() answers while
// the delete is outstanding, not what it answers.
func TestHeldDoesNotBlockOnAReleaseRoundTrip(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:tenant-3")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holder, err := lease.WatchHolder(ctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}

	gate := &leaseDeleteGateKV{
		KeyValue: lease.kv,
		parked:   make(chan struct{}),
		proceed:  make(chan struct{}),
	}
	lease.kv = gate
	defer gate.letDeleteReturn()

	releaseDone := make(chan error, 1)
	go func() { releaseDone <- lease.Release() }()

	select {
	case <-gate.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("Release never reached the KV delete")
	}

	// The delete is in flight. The gate predicate must still answer.
	held := make(chan bool, 1)
	go func() { held <- holder.Held() }()
	select {
	case <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("Holder.Held() blocked while a Release was inside its KV delete: the term gate is " +
			"evaluated per message and must never wait on a round trip, so mu must not be held across one")
	}

	gate.letDeleteReturn()
	if rerr := <-releaseDone; rerr != nil {
		t.Fatalf("Release: %v", rerr)
	}
}
