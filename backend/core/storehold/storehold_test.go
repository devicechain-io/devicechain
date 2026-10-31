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
