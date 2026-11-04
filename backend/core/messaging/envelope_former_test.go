// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
)

func newTestFormer(t *testing.T, cfg FormerConfig) *Former {
	t.Helper()
	f, err := NewFormer(cfg, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fm(n int) FormedMember { return FormedMember{Bytes: make([]byte, n)} }

// A tenant that sends an event a second never waits: the event closes its own group at once, in
// the legacy shape (k = 1).
func TestQuietTenantNeverWaits(t *testing.T) {
	f := newTestFormer(t, FormerConfig{})
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		got := f.Add(now, "acme", fm(10))
		if len(got) != 1 || len(got[0].Members) != 1 || got[0].Waited != 0 {
			t.Fatalf("event %d: %+v, want one group of one that waited 0", i, got)
		}
		if _, open := f.Deadline(); open {
			t.Fatal("a quiet tenant left a group open")
		}
		now = now.Add(time.Second)
	}
}

// A tenant at 1 event per 100 ms (10/s) is also not waited for: its predicted gap is longer
// than the linger.
func TestSlowTenantBelowLingerRateNeverWaits(t *testing.T) {
	f := newTestFormer(t, FormerConfig{})
	now := time.Unix(1000, 0)
	for i := 0; i < 20; i++ {
		got := f.Add(now, "acme", fm(10))
		if len(got) != 1 || len(got[0].Members) != 1 {
			t.Fatalf("event %d waited: %+v", i, got)
		}
		now = now.Add(10 * time.Millisecond)
	}
}

// A busy tenant is batched: after the rate is learned, events join an open group until it is
// full or the linger runs out, and the group's wait never exceeds the linger.
func TestBusyTenantBatchesUntilFullOrLinger(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond, MaxMembers: 8})
	now := time.Unix(1000, 0)
	var sizes []int
	var maxWait time.Duration
	collect := func(bs []FormedBatch) {
		for _, b := range bs {
			sizes = append(sizes, len(b.Members))
			if b.Waited > maxWait {
				maxWait = b.Waited
			}
		}
	}
	for i := 0; i < 400; i++ { // one event every 500 us: 2,000 events/s
		collect(f.Add(now, "acme", fm(10)))
		now = now.Add(500 * time.Microsecond)
		collect(f.Expired(now))
	}
	if maxWait > 5*time.Millisecond {
		t.Fatalf("a group waited %s, past the 5ms linger", maxWait)
	}
	big := 0
	for _, s := range sizes {
		if s > 8 {
			t.Fatalf("a group of %d exceeds the cap of 8", s)
		}
		if s >= 6 {
			big++
		}
	}
	if big < len(sizes)/2 {
		t.Fatalf("a busy tenant did not batch: sizes %v", sizes)
	}
}

// The linger is a hard bound measured from the FIRST member: a steady trickle just inside the
// linger does not extend it.
func TestLingerIsMeasuredFromTheFirstMember(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 4 * time.Millisecond})
	now := time.Unix(1000, 0)
	// Teach the former a fast rate.
	for i := 0; i < 10; i++ {
		f.Add(now, "acme", fm(1))
		now = now.Add(time.Millisecond)
		f.Expired(now)
	}
	f.Drain(now)
	start := now
	if got := f.Add(now, "acme", fm(1)); len(got) != 0 {
		t.Fatalf("the first event of a busy tenant was not held: %+v", got)
	}
	d, ok := f.Deadline()
	if !ok || !d.Equal(start.Add(4*time.Millisecond)) {
		t.Fatalf("deadline %v ok=%v, want first+linger", d, ok)
	}
	f.Add(start.Add(3*time.Millisecond), "acme", fm(1))
	if d2, _ := f.Deadline(); !d2.Equal(d) {
		t.Fatal("a later arrival moved the deadline")
	}
	got := f.Expired(start.Add(4 * time.Millisecond))
	if len(got) != 1 || len(got[0].Members) != 2 || got[0].Waited != 4*time.Millisecond {
		t.Fatalf("expiry: %+v", got)
	}
}

// Groups never mix tenants, and one tenant's open group neither closes nor delays another's.
func TestTenantsNeverMix(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	now := time.Unix(1000, 0)
	for i := 0; i < 10; i++ { // both learn a fast rate
		f.Add(now, "a", fm(1))
		f.Add(now, "b", fm(1))
		now = now.Add(time.Millisecond)
		f.Expired(now)
	}
	f.Drain(now)
	f.Add(now, "a", fm(1))
	f.Add(now, "a", fm(1))
	f.Add(now, "b", fm(1))
	// A quiet tenant arriving meanwhile is published at once and does not touch the others.
	quiet := f.Add(now, "quiet", fm(1))
	if len(quiet) != 1 || quiet[0].Tenant != "quiet" {
		t.Fatalf("quiet tenant: %+v", quiet)
	}
	out := f.Drain(now)
	if len(out) != 2 {
		t.Fatalf("drain: %+v", out)
	}
	for _, b := range out {
		want := map[string]int{"a": 2, "b": 1}[b.Tenant]
		if len(b.Members) != want {
			t.Fatalf("tenant %s group has %d members, want %d", b.Tenant, len(b.Members), want)
		}
	}
}

// A member that does not fit beside the open group closes it first; a member over the byte
// bound alone is a group of its own.
func TestByteBoundAndOversizeMember(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	now := time.Unix(1000, 0)
	for i := 0; i < 10; i++ {
		f.Add(now, "a", fm(1))
		now = now.Add(time.Millisecond)
		f.Expired(now)
	}
	f.Drain(now)
	f.Add(now, "a", fm(40<<10))
	got := f.Add(now, "a", fm(40<<10)) // 80 KiB > 64 KiB: the first closes alone
	if len(got) != 1 || len(got[0].Members) != 1 {
		t.Fatalf("second member should have closed the first: %+v", got)
	}
	got = f.Add(now, "a", fm(eventenvelope.MaxRawBytes+1))
	if len(got) != 2 {
		t.Fatalf("oversize member: %+v", got)
	}
}

func TestFormerConfigRefusesOutOfRange(t *testing.T) {
	for _, c := range []FormerConfig{{Linger: 11 * time.Millisecond}, {Linger: -1}, {MaxMembers: 65}, {MaxBytes: eventenvelope.MaxRawBytes + 1}} {
		if _, err := NewFormer(c, "x", nil); err == nil {
			t.Fatalf("%+v accepted", c)
		}
	}
	f, err := NewFormer(FormerConfig{}, "x", nil)
	if err != nil || f.Linger() != DefaultFormerLinger {
		t.Fatalf("defaults: %v %v", f, err)
	}
}

// GroupCommit: a quiet tenant's Add flushes on the caller's goroutine before returning; a busy
// tenant's members are flushed together by the timer, within the linger.
func TestGroupCommitFlushesQuietNowAndBusyByTimer(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	var mu sync.Mutex
	var batches []FormedBatch
	g := NewGroupCommit(f, func(b FormedBatch) {
		mu.Lock()
		batches = append(batches, b)
		mu.Unlock()
	})
	defer g.Stop()
	if err := g.Add("quiet", fm(1)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(batches) != 1 || len(batches[0].Members) != 1 {
		t.Fatalf("a quiet tenant's Add did not flush inline: %+v", batches)
	}
	mu.Unlock()

	// Teach a fast rate, then add two and wait for the timer.
	for i := 0; i < 12; i++ {
		_ = g.Add("busy", fm(1))
		time.Sleep(time.Millisecond / 2)
	}
	mu.Lock()
	batches = nil
	mu.Unlock()
	var flushed atomic.Int32
	f2 := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	g2 := NewGroupCommit(f2, func(b FormedBatch) { flushed.Add(int32(len(b.Members))) })
	for i := 0; i < 20; i++ {
		_ = g2.Add("busy", fm(1))
		time.Sleep(200 * time.Microsecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for flushed.Load() < 20 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if flushed.Load() != 20 {
		t.Fatalf("flushed %d of 20 members; the timer must flush what the linger holds", flushed.Load())
	}
	g2.Stop()
	if err := g2.Add("busy", fm(1)); err != ErrGroupCommitStopped {
		t.Fatalf("Add after Stop: %v", err)
	}
}
