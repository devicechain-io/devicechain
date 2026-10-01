// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	emconfig "github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Under a backlog the processor commits many events per transaction, with and without the
// linger.
//
// This is the capacity defect by VALUE: with one transaction per event, the number of
// transactions equals the number of events however deep the backlog, and on a replicated
// event store every one of them waits for a standby. It is built with the constructor's
// existing call shape, so what the "defaults" row measures is what a service that sets
// nothing gets.
//
// Two shapes, one bound. With the linger off, as many events as the processor can hold are
// handed off while every writer is held inside its first transaction of one event, so the
// rest of them are waiting in the buffer when the writers are released and drain in
// batches: this row is the one that exercises the drain of what is already waiting. With
// the default linger, the writers are still gathering when the hand-offs arrive, so most
// events go into the batches they hold at the gate and the buffer stays nearly empty. Each
// writer's first transaction was opened under the gate (at most one per writer); what is
// left drains in batches.
func TestTheDefaultWritersCommitABacklogInFarFewerTransactions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []ProcessorOption
	}{
		{"linger off", []ProcessorOption{WithPersistence(emconfig.PersistenceConfiguration{LingerMillis: millis(0)})}},
		{"defaults", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep, db, _ := newFencedPersistenceWorker(t)
			singleConnection(t, db)
			api := newTxApi(ep)
			api.gate = make(chan struct{})
			acks := &ackLog{api: api}

			ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			proc := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), api, NewPersistMetrics(ms), tc.opts...)
			if err := proc.Initialize(context.Background()); err != nil {
				t.Fatalf("initialize: %v", err)
			}

			// Each writer holds at least one event at the gate and the buffer holds the rest, so
			// this many hand-offs all complete before the gate opens.
			w, b := emconfig.DefaultPersistenceWriters, emconfig.DefaultPersistenceMaxBatch
			n := w + MESSAGE_BACKLOG_SIZE
			t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
			for i := 0; i < n; i++ {
				if !proc.handOff(context.Background(), consumed(t, acks, i, fenceCostTenant, 1, batchEvent(i, true, t0))) {
					t.Fatalf("hand-off %d refused", i)
				}
			}
			close(api.gate)
			acks.waitFor(t, n, 10*time.Second)
			if err := proc.ExecuteStop(context.Background()); err != nil {
				t.Fatalf("stop: %v", err)
			}

			if got := len(acks.acked()); got != n {
				t.Errorf("%d distinct events acknowledged; want %d", got, n)
			}
			if e, m, a := rowCounts(t, db); e != int64(n) || m != int64(3*n) || a != int64(n) {
				t.Errorf("stored %d/%d/%d event/measurement/anchor rows; want %d/%d/%d", e, m, a, n, 3*n, n)
			}
			txs, committed := api.txs.Load(), api.committed.Load()
			// At most one transaction per writer opened under the gate, at most (n-w)/b full
			// batches of what is left, at most one partial batch per writer as the buffer runs
			// dry (nothing refills it once the gate opens), and one for the remainder. One
			// transaction per event would be n.
			if maxTxs := int64(2*w + (n-w)/b + 1); txs > maxTxs {
				t.Errorf("%d events took %d transactions; want at most %d", n, txs, maxTxs)
			}
			// The lower bound and the commit count keep the upper bound honest: a batch path that
			// wrote OUTSIDE PersistInTx would read low on txs, not fail.
			if txs < 2 || committed != txs {
				t.Errorf("%d transactions opened, %d committed; want at least 2, all committed", txs, committed)
			}
		})
	}
}
