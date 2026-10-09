// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// These tests pin the ways the single-writer loop can stop making progress while every health
// signal reads green: a call that never returns, and a park that never ends.

// atomicClock is a settable clock safe to move while the loop goroutine reads it.
type atomicClock struct{ ns atomic.Int64 }

func newAtomicClock(t time.Time) *atomicClock {
	c := &atomicClock{}
	c.set(t)
	return c
}
func (c *atomicClock) Now() time.Time      { return time.Unix(0, c.ns.Load()) }
func (c *atomicClock) set(t time.Time)     { c.ns.Store(t.UnixNano()) }
func (c *atomicClock) add(d time.Duration) { c.ns.Add(int64(d)) }

// newBlockingStore is a snapshot store whose every query waits for its context to end, the way
// a black-holed database socket does.
func newBlockingStore(t *testing.T) (*model.SnapshotStore, *atomic.Int32) {
	t.Helper()
	store, entered, _ := newSlowStore(t, hang)
	return store, entered
}

// A Save that never returns must not hold the loop: the checkpoint gives up at its deadline,
// reports the failure, and leaves the work pending so the next tick retries it.
func TestACheckpointWhoseSaveHangsGivesUpAtItsDeadline(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	g.rp.Store, _ = newBlockingStore(t)
	g.rp.cfg.CheckpointTimeout = 100 * time.Millisecond
	g.rp.handle(hot(t, 1, &fakeAck{}))
	if !g.rp.dirty {
		t.Fatal("the message did not dirty the engine")
	}

	done := make(chan bool, 1)
	go func() { done <- g.rp.checkpoint(context.Background()) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a checkpoint whose Save never returned reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("checkpoint is still blocked in Save well past its deadline")
	}
	equal(t, "save failures", testutil.ToFloat64(g.metrics.checkpointFailures.WithLabelValues(checkpointStageSave)), 1.0)
	if !g.rp.dirty || len(g.rp.pendingAcks) != 1 {
		t.Fatalf("the failed checkpoint dropped its work: dirty=%v pendingAcks=%d", g.rp.dirty, len(g.rp.pendingAcks))
	}
}

// The deadline is a default, not a knob a struct-literal processor has to remember.
func TestTheCheckpointDeadlineDefaults(t *testing.T) {
	rp := &ResolvedEventsProcessor{}
	if got := rp.checkpointTimeout(); got != defaultCheckpointTimeout {
		t.Fatalf("default = %v, want %v", got, defaultCheckpointTimeout)
	}
}

// startLoop runs the live loop on a clock the test moves.
func startLoop(t *testing.T, g *gapRig, clock *atomicClock, reader messaging.MessageReader) {
	t.Helper()
	rp := g.rp
	rp.clock = clock
	rp.ResolvedEventsReader = reader
	rp.cfg.TickInterval = 5 * time.Millisecond
	rp.readerWG.Add(1)
	go rp.run()
	t.Cleanup(func() { rp.pcancel(); rp.readerWG.Wait() })
}

func heartbeat(g *gapRig) int64 { return int64(testutil.ToFloat64(g.metrics.loopHeartbeat)) }

// An idle loop stamps its heartbeat every tick, so the stamp follows the clock.
func TestAnIdleLoopKeepsItsHeartbeatFresh(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	clock := newAtomicClock(testBase)
	startLoop(t, g, clock, &fakeReader{})

	waitFor(t, "the first heartbeat", func() bool { return heartbeat(g) == testBase.Unix() })
	clock.add(90 * time.Second)
	waitFor(t, "the heartbeat to follow the clock", func() bool { return heartbeat(g) == testBase.Add(90*time.Second).Unix() })
}

// A loop hung inside a call stops stamping: that staleness is the only signal it gives, since
// every other gauge is sampled on the loop itself.
func TestALoopHungInsideACheckpointStopsItsHeartbeat(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	var entered *atomic.Int32
	g.rp.Store, entered = newBlockingStore(t)
	g.rp.cfg.CheckpointEvents = 1
	g.rp.cfg.CheckpointTimeout = time.Hour // the hang this test is about
	clock := newAtomicClock(testBase)
	startLoop(t, g, clock, &feedReader{fakeReader: fakeReader{results: []readResult{{msg: hot(t, 1, &fakeAck{})}}}})

	// The message is read, applied and flushed by count, and the flush blocks in Save.
	waitFor(t, "the loop to block in Save", func() bool { return entered.Load() > 0 })
	stamped := heartbeat(g)
	clock.add(10 * time.Minute)
	time.Sleep(200 * time.Millisecond) // forty ticks' worth
	if got := heartbeat(g); got != stamped {
		t.Fatalf("a loop hung in Save kept stamping: %d -> %d", stamped, got)
	}
}

// A fill that fails deterministically ends the term once it has failed past the bound.
func TestAGapFillThatNeverSucceedsEndsTheTerm(t *testing.T) {
	g := newGapRig(t, &gatedOpener{}) // never opens: the same read fails every time
	clock := newAtomicClock(testBase)
	g.rp.clock = clock

	g.rp.handle(hot(t, 3, &fakeAck{})) // 1..2 can never be read
	if g.rp.gapHeld == nil {
		t.Fatal("the loop did not park")
	}
	clock.add(gapParkLimit - time.Second)
	g.rp.retryGapFill()
	if g.rp.pctx().Err() != nil {
		t.Fatal("the term ended before the bound")
	}
	clock.add(2 * time.Second)
	g.rp.retryGapFill()
	if !errors.Is(g.rp.pctx().Err(), context.Canceled) {
		t.Fatal("the term did not end after the fill failed past the bound")
	}
}

// A fill that fails and then recovers restarts the clock: only CONTINUOUS failure ends a term.
func TestATransientGapFailureThatRecoversDoesNotEndTheTerm(t *testing.T) {
	opener := &gatedOpener{}
	opener.msgs = stream(t, 1, 2)
	g := newGapRig(t, opener)
	clock := newAtomicClock(testBase)
	g.rp.clock = clock

	g.rp.handle(hot(t, 3, &fakeAck{})) // parks: the broker is unreachable
	clock.add(gapParkLimit - time.Minute)
	g.rp.retryGapFill() // still failing, but inside the bound
	opener.release()
	g.rp.retryGapFill() // recovers
	if g.rp.gapHeld != nil {
		t.Fatal("the park was not released")
	}

	// A fresh outage begins now; it must be measured from here, not from the first one.
	opener.mu.Lock()
	opener.open = false
	opener.mu.Unlock()
	g.rp.handle(hot(t, 6, &fakeAck{}))
	clock.add(gapParkLimit - time.Minute)
	g.rp.retryGapFill()
	if g.rp.pctx().Err() != nil {
		t.Fatal("a recovered failure counted toward the bound of the next one")
	}
}

// A fact projection write that keeps failing says why, and counts each retry, so a consumer
// blocked head-of-line behind a permanent error is visible rather than silent.
func TestAFailingFactPersistLogsItsErrorAndCountsRetries(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	logged := logSink.Capture(t)
	calls := 0
	ok := g.rp.persistBeforeAck("test fact", func() error {
		if calls++; calls <= 2 {
			return errors.New("constraint violation 42")
		}
		return nil
	})
	if !ok {
		t.Fatal("the persist did not succeed once the write did")
	}
	equal(t, "retries", testutil.ToFloat64(g.metrics.factPersistRetries), 2.0)
	if out := logged.String(); !strings.Contains(out, "constraint violation 42") {
		t.Fatalf("the retry log does not carry the error; logs were:\n%s", out)
	}
}
