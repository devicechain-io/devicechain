// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"database/sql/driver"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/jackc/pgx/v5/pgconn"
)

const metricHeld = "devicechain_eventmanagement_persist_store_held_events"

// gauge reads an unlabelled gauge, or -1 when it is not registered.
func (r *batchRig) gauge(t *testing.T, name string) float64 {
	t.Helper()
	families, err := r.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name && len(f.GetMetric()) == 1 && f.GetMetric()[0].GetGauge() != nil {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return -1
}

func fastProbes(t *testing.T) {
	t.Helper()
	min, max := heldProbeMin, heldProbeMax
	heldProbeMin, heldProbeMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { heldProbeMin, heldProbeMax = min, max })
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// THE REGRESSION THIS FILE EXISTS FOR: a store outage longer than the delivery budget used
// to turn every in-flight event into an acknowledged-then-reported-failed one. Here every
// message arrives on its LAST delivery (NumDelivered == MaxDeliver), the store refuses
// writes for a while, and nothing may be acked or reported failed until the store takes
// them -- then every message is stored and acked exactly once.
func TestAStoreOutageHoldsEventsEvenOnTheirLastDeliveryAndStoresThemOnRecovery(t *testing.T) {
	fastProbes(t)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"disk full, SQLSTATE 53100", &pgconn.PgError{Code: "53100", Message: "could not extend file: No space left on device"}},
		{"out of memory, SQLSTATE 53200", &pgconn.PgError{Code: "53200", Message: "out of memory"}},
		{"read-only transaction, SQLSTATE 25006", &pgconn.PgError{Code: "25006", Message: "cannot execute INSERT in a read-only transaction"}},
		{"io error, SQLSTATE 58030", &pgconn.PgError{Code: "58030", Message: "could not write block"}},
		{"connection exception, SQLSTATE 08006", &pgconn.PgError{Code: "08006", Message: "connection failure"}},
		{"a bad connection", fmt.Errorf("injected: %w", driver.ErrBadConn)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			r.api.storeDown.Store(true)
			r.api.storeErr = tc.err
			msgs := make([]messaging.Message, 0, 8)
			for i := 0; i < 8; i++ {
				msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, messaging.MaxDeliver, batchEvent(i, true, batchT0)))
			}
			done := make(chan struct{})
			go func() { defer close(done); r.run(msgs) }()

			waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 8 })
			// Several probes against the down store: none may ack, store or report.
			txs := r.api.txs.Load()
			waitUntil(t, "probes against the down store", func() bool { return r.api.txs.Load() >= txs+3 })
			if n := len(r.acks.snapshot()); n != 0 {
				t.Fatalf("%d events acknowledged while the store was down; want none", n)
			}
			if f := r.reported(); len(f) != 0 {
				t.Fatalf("%d events reported failed while the store was down; want none", len(f))
			}

			r.api.storeDown.Store(false) // the volume was expanded
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the writer did not finish after the store recovered")
			}
			if e, m, _ := rowCounts(t, r.db); e != 8 || m != 24 {
				t.Errorf("stored %d events / %d measurements; want 8 / 24", e, m)
			}
			acked := r.acks.acked()
			for i := 0; i < 8; i++ {
				if acked[i] != 1 {
					t.Errorf("message %d acknowledged %d times; want 1", i, acked[i])
				}
			}
			if f := r.reported(); len(f) != 0 {
				t.Errorf("reported %d failures; want none", len(f))
			}
			if g := r.gauge(t, metricHeld); g != 0 {
				t.Errorf("held gauge = %v after recovery; want 0", g)
			}
			if got := r.metric(t, metricMessages, "result", core.ResultOK); got != 0 && got != 8 {
				t.Errorf("recorded %v ok; want 8 (or 0 for a held message already settled as retry)", got)
			}
		})
	}
}

// The control: a failure that is NOT a store outage still gives up on the last delivery,
// reporting the event and acking it, as before. Holding must not turn a poison message into
// one that is held forever.
func TestAnErrorThatIsNotAStoreOutageStillGivesUpOnTheLastDelivery(t *testing.T) {
	fastProbes(t)
	r := newBatchRig(t, 1)
	r.api.storeDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "40001", Message: "injected: could not serialize"}
	r.run([]messaging.Message{consumed(t, r.acks, 0, fenceCostTenant, messaging.MaxDeliver, batchEvent(0, true, batchT0))})
	if got := r.acks.acked()[0]; got != 1 {
		t.Errorf("acknowledged %d times; want 1 (given up on at the last delivery)", got)
	}
	if f := r.reported(); len(f) != 1 {
		t.Errorf("reported %d failures; want 1", len(f))
	}
}

// Shutdown must not hang behind a store that never recovers: closing Halt ends the hold,
// leaving the events UNACKED (redelivered to the next pod) and unreported.
func TestHaltEndsTheHoldAndLeavesEventsUnacked(t *testing.T) {
	fastProbes(t)
	r := newBatchRig(t, 4)
	r.api.storeDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	halt := make(chan struct{})
	r.ep.Halt = halt
	msgs := make([]messaging.Message, 0, 4)
	for i := 0; i < 4; i++ {
		msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0)))
	}
	done := make(chan struct{})
	go func() { defer close(done); r.run(msgs) }()
	waitUntil(t, "the writer to hold the batch", func() bool { return r.gauge(t, metricHeld) == 4 })
	close(halt)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the writer did not stop after Halt")
	}
	if n := len(r.acks.snapshot()); n != 0 {
		t.Errorf("%d events acknowledged on halt; want none", n)
	}
	if f := r.reported(); len(f) != 0 {
		t.Errorf("%d events reported failed on halt; want none", len(f))
	}
	if g := r.gauge(t, metricHeld); g != 0 {
		t.Errorf("held gauge = %v after halt; want 0", g)
	}
}
