// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"
)

// TestHolderIsFalseOnceTheWindowElapsesWithNoWatchEvent is the fail-open guard, and
// it is the reason Held is an AND rather than an OR.
//
// A JetStream KV entry that ages out emits NO watch update. With no successor pod
// to overwrite the key there is nothing for the watch to see at all, so a
// watch-only holder flag stays "mine" indefinitely after the server has already
// forgotten us. This test takes a short-TTL lease, never renews it, and asserts the
// signal closes anyway — which only the validity-window half can do.
//
// The negative control is the first assertion: the signal must be TRUE immediately
// after Acquire, or "false at the end" would prove nothing.
func TestHolderIsFalseOnceTheWindowElapsesWithNoWatchEvent(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:window")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := lease.WatchHolder(ctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}
	if !h.Held() {
		t.Fatal("Held() is false immediately after Acquire; the signal never opened, so its later closing would prove nothing")
	}

	// No renewal, so the window elapses at Acquire+TTL. Nothing writes the key, so
	// the watch stays silent throughout.
	waitFor(t, "the holder signal to close on window expiry alone", func() bool { return !h.Held() })

	// And the watch really did stay silent: an expiry is not a "definitive loss"
	// event, so Lost must NOT have closed. If it had, this test would be passing on
	// the wrong mechanism.
	select {
	case <-h.Lost():
		t.Fatal("Lost() closed on a TTL expiry; expiry is watch-silent, so this signal came from somewhere unintended")
	default:
	}
}

// TestHolderFlipsOnATakeover pins the other half: a successor that acquires the
// partition after our entry expired writes a foreign uuid, and the watch must see
// it — promptly, without waiting for our own window to run down.
func TestHolderFlipsOnATakeover(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	first, err := dl.Acquire("detect:takeover")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := first.WatchHolder(ctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}
	if !h.Held() {
		t.Fatal("Held() is false immediately after Acquire")
	}

	// Let the entry expire so a successor can Create, then take it over.
	var second *Lease
	waitFor(t, "the successor to acquire the expired partition", func() bool {
		l, aerr := dl.Acquire("detect:takeover")
		if aerr != nil {
			return false
		}
		second = l
		return true
	})
	t.Cleanup(func() { _ = second.Release() })

	select {
	case <-h.Lost():
	case <-time.After(5 * time.Second):
		t.Fatal("Lost() did not close after a successor took the partition; the watch is not seeing foreign writes")
	}
	if h.Held() {
		t.Fatal("Held() is still true after a takeover")
	}
}

// TestHolderFlipsOnADelete covers the third flip source. A Delete leaves no value
// to compare against our uuid, so a holder that only tested "value == mine" on Put
// operations would sail past it — and a released partition is immediately
// acquirable by anyone.
func TestHolderFlipsOnADelete(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(30 * time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:deleted")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := lease.WatchHolder(ctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}
	if !h.Held() {
		t.Fatal("Held() is false immediately after Acquire")
	}

	// The TTL here is 30s, so the window stays open for the whole test: only the
	// watch can produce this flip, which is the point.
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case <-h.Lost():
	case <-time.After(5 * time.Second):
		t.Fatal("Lost() did not close after the entry was deleted")
	}
	if h.Held() {
		t.Fatal("Held() is still true after the entry was deleted")
	}
}

// TestHolderKeepsOwnershipAcrossRenewals is the counterweight to all three tests
// above: a lease that IS being renewed must keep reporting held for longer than its
// TTL. Without this, a Holder hard-wired to report false would pass every other
// test in this file.
func TestHolderKeepsOwnershipAcrossRenewals(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	dl, err := nmgr.NewDistributedLease(time.Second)
	if err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	lease, err := dl.Acquire("detect:renewed")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := lease.WatchHolder(ctx)
	if err != nil {
		t.Fatalf("WatchHolder: %v", err)
	}
	go func() { _ = lease.KeepAlive(ctx, 200*time.Millisecond) }()

	// Two and a half TTLs. A renewal also writes our OWN uuid to the key, so this
	// additionally pins that a self-Put is not read as a foreign one.
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !h.Held() {
			t.Fatal("Held() went false while the lease was being renewed on schedule")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-h.Lost():
		t.Fatal("Lost() closed while the lease was being renewed; a self-Put is being read as a takeover")
	default:
	}
}
