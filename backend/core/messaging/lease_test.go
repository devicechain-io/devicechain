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
// owner (what the TTL does on a crash) and letting a standby Acquire. A LIVE
// AmITheHolder read then shows the owner it no longer holds it, its Renew CAS
// fails, and the standby genuinely owns the higher epoch.
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
	if held, err := owner.AmITheHolder(); err != nil || !held {
		t.Fatalf("AmITheHolder while held = (%v, %v), want (true, nil)", held, err)
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

	// A live read shows the displaced owner it is out (definitive, not the transient
	// ride-out): the entry now carries a different holder uuid.
	if held, err := owner.AmITheHolder(); err != nil || held {
		t.Fatalf("displaced owner AmITheHolder = (%v, %v), want (false, nil)", held, err)
	}
	// And its CAS renewal can no longer land.
	if err := owner.Renew(); err == nil {
		t.Fatal("displaced owner Renew succeeded, want a CAS failure")
	}
	// The standby genuinely holds it.
	if held, err := standby.AmITheHolder(); err != nil || !held {
		t.Fatalf("standby AmITheHolder = (%v, %v), want (true, nil)", held, err)
	}
}

// TestAmITheHolderRidesOutTransientErrorWithinWindow is the finding-3 fix: a NATS
// blip must NOT be read as lost ownership while we are still inside the validity
// window (no standby can Acquire until the server-side entry TTL-expires). Only
// past the window is a transient failure definitive. Without this, a shared broker
// hiccup evicts every owner at once.
func TestAmITheHolderRidesOutTransientErrorWithinWindow(t *testing.T) {
	nmgr, cleanup := newTestManager(t)

	dl, err := nmgr.NewDistributedLease(1 * time.Second)
	if err != nil {
		cleanup()
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("sparkplug")
	if err != nil {
		cleanup()
		t.Fatalf("Acquire: %v", err)
	}

	// Total NATS outage: every KV op now errors (a transient failure, not a takeover).
	cleanup()

	// Within the validity window (just acquired), we still hold the lease.
	if held, err := lease.AmITheHolder(); err != nil || !held {
		t.Fatalf("AmITheHolder during a blip within the window = (%v, %v), want (true, nil)", held, err)
	}
	// Force the window to have elapsed; now a transient failure IS definitive loss.
	lease.mu.Lock()
	lease.lastRenew = time.Now().Add(-2 * time.Second)
	lease.mu.Unlock()
	if held, err := lease.AmITheHolder(); held || err == nil {
		t.Fatalf("AmITheHolder past the window during a blip = (%v, %v), want (false, non-nil)", held, err)
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
	if held, err := standby.AmITheHolder(); err != nil || !held {
		t.Fatalf("standby AmITheHolder after displaced owner's Release = (%v, %v), want (true, nil): "+
			"a revision-unchecked release would have dropped the new owner's hold", held, err)
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
	if held, err := lease.AmITheHolder(); err != nil || held {
		t.Fatalf("AmITheHolder after Release = (%v, %v), want (false, nil)", held, err)
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
// — one KeepAlive renewer alongside a loop calling AmITheHolder and Epoch, plus a
// Release — so `go test -race` can prove the mutex actually covers the shared
// fields (the "safe for concurrent use" claim is otherwise pinned only by reading).
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
				_, _ = lease.AmITheHolder()
				_ = lease.Epoch()
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)
	_ = lease.Release()
	cancel()
	wg.Wait()
}
