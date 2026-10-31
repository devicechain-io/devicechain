// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package storehold_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
)

func newHolder(store *holdtest.Store, halt <-chan struct{}) (*storehold.Holder, *holdtest.Gauge) {
	g := &holdtest.Gauge{}
	return &storehold.Holder{Name: "t", Probe: store.Probe, Halt: halt, Held: g, Tune: holdtest.FastTuning()}, g
}

func TestOutageClassification(t *testing.T) {
	store := &holdtest.Store{}
	h, _ := newHolder(store, nil)
	ctx := context.Background()
	if !h.Outage(ctx, holdtest.DiskFull) {
		t.Error("a server-stated refusal is an outage without a probe")
	}
	if store.Probes.Load() != 0 {
		t.Error("a server-stated refusal must not probe")
	}
	if h.Outage(ctx, holdtest.ConnLost) {
		t.Error("a connection error with a live store is a poison message, not an outage")
	}
	store.Down.Store(true)
	if !h.Outage(ctx, holdtest.ConnLost) {
		t.Error("a connection error with a dead store is an outage")
	}
	if h.Outage(ctx, errors.New("bad payload")) {
		t.Error("an ordinary error is never an outage")
	}
}

func TestRunHoldsThenRetriesAndNeverReturnsTheOutage(t *testing.T) {
	store := &holdtest.Store{}
	store.Down.Store(true)
	h, gauge := newHolder(store, nil)
	msg, ack := holdtest.Msg("s", nil, messaging.MaxDeliver) // last delivery
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- h.Run(context.Background(), []messaging.Message{msg}, func() error {
			if calls.Add(1) < 3 {
				return holdtest.DiskFull
			}
			return nil
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for (gauge.Value() != 1 || ack.Progress.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if gauge.Value() != 1 || ack.Progress.Load() == 0 {
		t.Fatalf("held gauge %d, progress beats %d; want 1 and at least one", gauge.Value(), ack.Progress.Load())
	}
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 1 {
		t.Error("the operation must not run again while the store is down")
	}
	if ack.Progress.Load() < 2 {
		t.Errorf("a held message must keep being told it is in progress; got %d beats", ack.Progress.Load())
	}
	store.Down.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v after recovery; want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not finish after the store recovered")
	}
	if gauge.Value() != 0 {
		t.Errorf("held gauge %d after recovery; want 0", gauge.Value())
	}
	if ack.Acked.Load() != 0 {
		t.Error("Run must not ack; the caller acks after success")
	}
}

func TestRunReturnsAPoisonMessageErrorWithoutHolding(t *testing.T) {
	store := &holdtest.Store{} // up
	h, gauge := newHolder(store, nil)
	msg, _ := holdtest.Msg("s", nil, 1)
	var calls int
	err := h.Run(context.Background(), []messaging.Message{msg}, func() error { calls++; return holdtest.ConnLost })
	if !errors.Is(err, holdtest.ConnLost) || calls != 1 {
		t.Fatalf("err=%v calls=%d; want the error back after one call", err, calls)
	}
	if gauge.Value() != 0 {
		t.Error("a poison message must not register as held")
	}
}

func TestRunReleasesOnHalt(t *testing.T) {
	store := &holdtest.Store{}
	store.Down.Store(true)
	halt := make(chan struct{})
	h, gauge := newHolder(store, halt)
	msg, ack := holdtest.Msg("s", nil, messaging.MaxDeliver)
	done := make(chan error, 1)
	go func() {
		done <- h.Run(context.Background(), []messaging.Message{msg}, func() error { return holdtest.DiskFull })
	}()
	time.Sleep(50 * time.Millisecond)
	close(halt)
	select {
	case err := <-done:
		if !errors.Is(err, storehold.ErrReleased) {
			t.Fatalf("Run = %v; want ErrReleased", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on halt")
	}
	if gauge.Value() != 0 || ack.Acked.Load() != 0 {
		t.Errorf("gauge %d acked %d; want the gauge cleared and the message left unacked", gauge.Value(), ack.Acked.Load())
	}
}

// A probe taken BEFORE the operation began says nothing about why it failed. With a non-zero
// ProbeFresh (holdtest.FastTuning sets none, which hides this), a cached "answered" must not
// turn a re-run's connection error into a poison message.
func TestACachedProbeFromBeforeTheOperationIsNotBelieved(t *testing.T) {
	store := &holdtest.Store{}
	h, _ := newHolder(store, nil)
	h.Tune.ProbeFresh = time.Hour
	ctx := context.Background()
	if !h.StoreAnswers(ctx) { // caches an answer
		t.Fatal("store should answer")
	}
	store.Down.Store(true) // the store drops after that probe
	began := time.Now()
	if !h.OutageSince(ctx, holdtest.ConnLost, began) {
		t.Fatal("a connection error after a stale cached probe must re-probe and see the outage")
	}
	// Control: the same cache IS believed for an operation that began before it.
	store.Down.Store(false)
	h.StoreAnswers(ctx)
	store.Down.Store(true)
	if h.OutageSince(ctx, holdtest.ConnLost, time.Now().Add(-time.Hour)) {
		t.Fatal("control: a probe taken after the operation began is believed")
	}
}

// A full disk ANSWERS the probe and refuses every write. The wait between re-runs must grow
// anyway, not restart at ProbeMin each time.
func TestTheWaitGrowsWhileTheStoreAnswersButKeepsRefusingWrites(t *testing.T) {
	store := &holdtest.Store{} // up: SELECT 1 succeeds
	h, _ := newHolder(store, nil)
	h.Tune.ProbeMin, h.Tune.ProbeMax = 20*time.Millisecond, 160*time.Millisecond
	msg, _ := holdtest.Msg("s", nil, messaging.MaxDeliver)
	var times []time.Time
	err := h.Run(context.Background(), []messaging.Message{msg}, func() error {
		times = append(times, time.Now())
		if len(times) < 6 {
			return holdtest.DiskFull
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first := times[1].Sub(times[0])
	last := times[5].Sub(times[4])
	if last < 3*first/2 || last < 80*time.Millisecond {
		t.Fatalf("waits did not grow: first %v, last %v", first, last)
	}
}

// While a re-run is in flight the held message keeps being beaten, and the op is handed a
// context with a deadline under AckWait.
func TestARerunIsBeatenWhileItBlocksAndIsBounded(t *testing.T) {
	store := &holdtest.Store{}
	h, _ := newHolder(store, nil)
	msg, ack := holdtest.Msg("s", nil, messaging.MaxDeliver)
	calls := 0
	var hadDeadline bool
	err := h.RunCtx(context.Background(), context.Background(), []messaging.Message{msg}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return holdtest.DiskFull
		}
		_, hadDeadline = ctx.Deadline()
		before := ack.Progress.Load()
		time.Sleep(400 * time.Millisecond) // ProgressEvery is 60 ms
		if got := ack.Progress.Load() - before; got < 3 {
			t.Errorf("only %d beats during a 400 ms re-run", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hadDeadline {
		t.Error("a re-run's context must carry a deadline when the caller's has none")
	}
}

// Settle holds a write that follows an irreversible step, and re-runs ONLY the write: the step
// the enclosing Run operation took is not repeated.
func TestSettleRetriesOnlyTheWriteAfterAnIrreversibleStep(t *testing.T) {
	store := &holdtest.Store{}
	h, _ := newHolder(store, nil)
	msg, _ := holdtest.Msg("s", nil, 1)
	var sends, writes atomic.Int32
	err := h.RunCtx(context.Background(), context.Background(), []messaging.Message{msg},
		func(ctx context.Context) error {
			sends.Add(1)
			return storehold.Settle(ctx, func(context.Context) error {
				if writes.Add(1) <= 2 {
					return holdtest.DiskFull
				}
				return nil
			})
		})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sends.Load() != 1 || writes.Load() != 3 {
		t.Fatalf("sends=%d writes=%d, want 1 and 3: only the write is retried", sends.Load(), writes.Load())
	}
}

// Outside a Run there is nothing to hold on: the write runs once and its error is returned.
func TestSettleWithoutAScopeRunsOnce(t *testing.T) {
	var n int
	err := storehold.Settle(context.Background(), func(context.Context) error { n++; return holdtest.DiskFull })
	if err != holdtest.DiskFull || n != 1 {
		t.Fatalf("err=%v runs=%d, want the refusal after one run", err, n)
	}
}
