// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Under a backlog the default processor commits many events per transaction.
//
// This is the capacity defect by VALUE: with one transaction per event, the number of
// transactions equals the number of events however deep the backlog, and on a replicated
// event store every one of them waits for a standby. It is built with the constructor's
// existing call shape and the default settings, so what it measures is what a service
// that sets nothing gets.
//
// The shape: 64 events are handed off while every writer is held inside its first
// transaction, so the rest of them are waiting when the writers are released. Each
// writer's first transaction was opened under the gate (at most one per writer); what is
// left drains in batches.
func TestFiveWritersCommitSixtyFourEventsInFarFewerTransactions(t *testing.T) {
	ep, db, _ := newFencedPersistenceWorker(t)
	singleConnection(t, db)
	api := newTxApi(ep)
	api.gate = make(chan struct{})
	acks := &ackLog{api: api}

	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	proc := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), api, NewPersistMetrics(ms))
	if err := proc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	const n = 64
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
	if e, m, a := rowCounts(t, db); e != n || m != 3*n || a != n {
		t.Errorf("stored %d/%d/%d event/measurement/anchor rows; want %d/%d/%d", e, m, a, n, 3*n, n)
	}
	txs, committed := api.txs.Load(), api.committed.Load()
	// At most one transaction per writer opened under the gate, at most one full batch of
	// the 59 left, and at most one partial batch per writer as the buffer runs dry.
	if txs > 12 {
		t.Errorf("%d events took %d transactions; want at most 12", n, txs)
	}
	// The lower bound and the commit count keep the upper bound honest: a batch path that
	// wrote OUTSIDE PersistInTx would read low on txs, not fail.
	if txs < 2 || committed != txs {
		t.Errorf("%d transactions opened, %d committed; want at least 2, all committed", txs, committed)
	}
}
