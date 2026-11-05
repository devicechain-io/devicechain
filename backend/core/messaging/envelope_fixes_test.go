// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
)

// One tenant's stalled PubAck must never delay another tenant's answer: each closed batch is
// flushed on a goroutine of its own.
//
// GUARD: kills a GroupCommit that flushes batches one after another on one goroutine (the timer's,
// or the adding request's).
func TestGroupCommitAStalledTenantDoesNotDelayAnother(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	stall := make(chan struct{})
	var bDone atomic.Int32
	g := NewGroupCommit(f, func(b FormedBatch) {
		if b.Tenant == "a" {
			<-stall
			return
		}
		bDone.Add(int32(len(b.Members)))
	})
	defer func() { close(stall); g.Stop() }()

	// Both tenants are quiet, so each Add closes its own group at once. A's flush blocks.
	if err := g.Add("a", fm(1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = g.Add("b", fm(1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tenant b's Add was held behind tenant a's stalled flush")
	}
	deadline := time.Now().Add(2 * time.Second)
	for bDone.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bDone.Load() != 1 {
		t.Fatal("tenant b's group was never flushed while tenant a's was stalled")
	}
}

// A message that bypasses the former drains its tenant's open group first, so it cannot overtake an
// earlier event of that tenant that is still waiting for company.
func TestDrainTenantReturnsOnlyThatTenantsOpenGroup(t *testing.T) {
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	now := time.Unix(1000, 0)
	for i := 0; i < 10; i++ { // learn a fast rate for both
		f.Add(now, "a", fm(1))
		f.Add(now, "b", fm(1))
		now = now.Add(time.Millisecond)
		f.Expired(now)
	}
	f.Drain(now)
	f.Add(now, "a", fm(1))
	f.Add(now, "a", fm(1))
	f.Add(now, "b", fm(1))
	got := f.DrainTenant(now, "a")
	if len(got) != 1 || got[0].Tenant != "a" || len(got[0].Members) != 2 {
		t.Fatalf("DrainTenant(a): %+v", got)
	}
	if again := f.DrainTenant(now, "a"); len(again) != 0 {
		t.Fatalf("a drained tenant has nothing open: %+v", again)
	}
	if rest := f.Drain(now); len(rest) != 1 || rest[0].Tenant != "b" {
		t.Fatalf("b's group must be untouched: %+v", rest)
	}
}

// The walk bound is the engine's configuration: a bucket that needs more rounds than it allows is
// an error that leaves its sources unacknowledged, never a loop; with room it settles.
func TestRoundEngineMaxRoundsIsConfigurable(t *testing.T) {
	build := func(maxRounds int) (*RoundEngine, *fakeBroker) {
		b := newFakeBroker()
		e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C",
			Writer: fakeWriter{b}, Rounds: fakeRoundPub{b}, MaxRounds: maxRounds, ChunkMax: func() int { return 1 }})
		if err != nil {
			t.Fatal(err)
		}
		return e, b
	}
	k, _, _ := RoundKeyOf("acme", 1, time.Time{})
	members := func(o *roundOutcomes, redelivered bool) []RoundMember {
		var ms []RoundMember
		for i := uint32(0); i < 12; i++ {
			ms = append(ms, o.member(i, redelivered))
		}
		return ms
	}
	// 12 members stored one per round.
	first, b := build(0)
	first.Publish(context.Background(), k, members(newRoundOutcomes(), false))
	first.Wait()
	if len(b.bySeq) != 12 {
		t.Fatalf("stored %d, want 12 rounds", len(b.bySeq))
	}
	// A restarted pod walks them as one chunk: with a bound of 4 it fails, with room it settles.
	for _, tc := range []struct {
		max     int
		wantErr bool
	}{{4, true}, {64, false}} {
		e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C",
			Writer: fakeWriter{b}, Rounds: fakeRoundPub{b}, MaxRounds: tc.max})
		if err != nil {
			t.Fatal(err)
		}
		o := newRoundOutcomes()
		e.Publish(context.Background(), k, members(o, true))
		e.Wait()
		failed := 0
		for i := uint32(0); i < 12; i++ {
			if o.errs[i] != nil {
				failed++
			}
			if o.n[i] != 1 {
				t.Fatalf("max %d: member %d settled %d times", tc.max, i, o.n[i])
			}
		}
		if (failed > 0) != tc.wantErr {
			t.Fatalf("max %d: %d members failed, wantErr=%v", tc.max, failed, tc.wantErr)
		}
		if len(b.bySeq) != 12 {
			t.Fatalf("max %d: the walk stored a message (%d)", tc.max, len(b.bySeq))
		}
	}
}

// A refused read-back is a missing grant, counted and logged apart from a flaky one, and never
// settles the sources as stored.
func TestProbeDeniedIsToldApartFromAFailedProbe(t *testing.T) {
	for _, denied := range []string{"nats: Permissions Violation for Publish to \"$JS.API.STREAM.MSG.GET.x\"", "authorization violation"} {
		if !probeDenied(errors.New(denied)) {
			t.Errorf("%q not classified as denied", denied)
		}
	}
	for _, ok := range []string{"nats: timeout", "no message at 4", ""} {
		if probeDenied(errors.New(ok)) {
			t.Errorf("%q classified as denied", ok)
		}
	}
	if probeDenied(nil) {
		t.Error("nil is not a denial")
	}

	b := newFakeBroker()
	k, _, _ := RoundKeyOf("acme", 3, time.Time{})
	newTestEngine(t, b).Publish(context.Background(), k, []RoundMember{newRoundOutcomes().member(3, false)})
	e, err := NewRoundEngine(RoundEngineConfig{Kind: eventenvelope.KindUnresolved, DedupKind: "C",
		Writer: fakeWriter{b}, Rounds: deniedRounds{fakeRoundPub{b}}})
	if err != nil {
		t.Fatal(err)
	}
	o := newRoundOutcomes()
	e.Publish(context.Background(), k, []RoundMember{o.member(3, true)})
	e.Wait()
	if o.n[3] != 1 || o.errs[3] == nil || !probeDenied(o.errs[3]) {
		t.Fatalf("a denied read-back must fail the source loudly, got n=%d err=%v", o.n[3], o.errs[3])
	}
}

type deniedRounds struct{ fakeRoundPub }

func (d deniedRounds) Get(context.Context, string, uint64) (StoredMessage, error) {
	return StoredMessage{}, fmt.Errorf("reading stream: nats: Permissions Violation for Publish to %q", "$JS.API.STREAM.MSG.GET.x")
}

var _ = sync.Mutex{}

// gcSeam holds a flush between mu being released and the flush starting, once armed. It is
// installed straight after NewGroupCommit, before anything can read the hook concurrently.
type gcSeam struct {
	armed   atomic.Bool
	atSeam  chan struct{}
	release chan struct{}
}

func installSeam(g *GroupCommit) *gcSeam {
	s := &gcSeam{atSeam: make(chan struct{}), release: make(chan struct{})}
	g.afterUnlock = func() {
		if s.armed.CompareAndSwap(true, false) {
			close(s.atSeam)
			<-s.release
		}
	}
	return s
}

// stopMustWaitFor arms the seam, runs closeBatch (which must make g close a batch), and once the
// batch is closed but its flush not started, starts Stop. It fails if Stop returns before the
// flush has run: the window the old code left a closed batch uncounted in.
func stopMustWaitFor(t *testing.T, g *GroupCommit, s *gcSeam, flushed *atomic.Int32, closeBatch func()) {
	t.Helper()
	s.armed.Store(true)
	go closeBatch()
	<-s.atSeam

	stopped := make(chan struct{})
	go func() { g.Stop(); close(stopped) }()
	// Stop takes mu first thing; once it has set stopped it is on its way to Wait.
	for {
		g.mu.Lock()
		st := g.stopped
		g.mu.Unlock()
		if st {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopped:
		t.Fatalf("Stop returned with a closed batch not yet published (flushed=%d)", flushed.Load())
	case <-time.After(100 * time.Millisecond): // Stop is correctly waiting
	}
	close(s.release)
	<-stopped
	if flushed.Load() != 1 {
		t.Fatalf("the batch closed before Stop must be published before Stop returns, flushed=%d", flushed.Load())
	}
}

// GUARD: a batch Add closed (under mu) is counted on the wait group before mu is released, so a
// Stop that slips in right after cannot return ahead of its publish.
func TestGroupCommitStopWaitsForABatchAddJustClosed(t *testing.T) {
	var flushed atomic.Int32
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	g := NewGroupCommit(f, func(FormedBatch) { flushed.Add(1) })
	seam := installSeam(g)
	stopMustWaitFor(t, g, seam, &flushed, func() {
		_ = g.Add("quiet", fm(1)) // a quiet tenant's lone event closes at once
	})
}

// GUARD: the same for a batch the linger timer closed.
func TestGroupCommitStopWaitsForABatchTheTimerJustClosed(t *testing.T) {
	var flushed atomic.Int32
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	g := NewGroupCommit(f, func(FormedBatch) { flushed.Add(1) })
	seam := installSeam(g)
	// Teach the former that the tenant is busy, so the next event waits for company.
	for i := 0; i < 4; i++ {
		_ = g.Add("busy", fm(1))
	}
	for { // let the warm-up groups close (by the timer) and publish
		g.mu.Lock()
		open := len(g.f.open)
		g.mu.Unlock()
		if open == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	g.wg.Wait()
	flushed.Store(0)
	stopMustWaitFor(t, g, seam, &flushed, func() {
		// The tenant is busy, so this event waits for company and the linger timer closes it.
		_ = g.Add("busy", fm(1))
	})
}

// Policy for work that arrives once Stop has begun: nothing is queued and nothing is dropped. Add
// is refused with ErrGroupCommitStopped, so its caller knows; a timer tick that runs after Stop
// finds no open group (Stop drained them all under mu), so no batch closes after Stop.
func TestGroupCommitNothingClosesAfterStopAndLateAddsAreRefused(t *testing.T) {
	var flushed atomic.Int32
	f := newTestFormer(t, FormerConfig{Linger: 5 * time.Millisecond})
	g := NewGroupCommit(f, func(b FormedBatch) { flushed.Add(int32(len(b.Members))) })
	for i := 0; i < 4; i++ {
		_ = g.Add("busy", fm(1))
	}
	g.Stop()
	before := flushed.Load()
	if err := g.Add("busy", fm(1)); err != ErrGroupCommitStopped {
		t.Fatalf("an Add after Stop must be refused with ErrGroupCommitStopped, got %v", err)
	}
	g.tick() // a tick that was already running when Stop stopped the timer
	g.wg.Wait()
	if got := flushed.Load(); got != before {
		t.Fatalf("a member that arrived after Stop was published (flushed %d -> %d)", before, got)
	}
}
