// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// The fixture the batching tests share: a REAL model.Api over the fenced sqlite database
// (newFencedPersistenceWorker — the production callback chain and the production ON
// CONFLICT arbiters), wrapped so a test can count, gate and break transactions without
// replacing anything a persist actually runs.

var (
	errInjectedCommit    = errors.New("injected: the commit failed and the transaction rolled back")
	errInjectedAmbiguous = errors.New("injected: the commit succeeded but was reported as failed")
)

// txApi is the real Api with PersistInTx and CreateEventAnchors instrumented.
type txApi struct {
	*model.Api

	// gate, when non-nil, holds every transaction until it is closed. It is taken BEFORE a
	// connection is checked out, so a held writer cannot starve the pool.
	gate chan struct{}

	// txs counts transactions opened; committed counts the ones that committed.
	txs, committed atomic.Int64

	// failCommit makes the next N transactions run every statement and then roll back, as
	// a COMMIT refused by the server does. ambiguousCommit makes the next N COMMIT and then
	// report an error anyway, as a connection lost after the server committed does.
	failCommit, ambiguousCommit atomic.Int32

	// failAnchorsFor refuses the anchor insert of any message from this device with a
	// class-22 error, i.e. AFTER that message's parent and payload rows were written.
	failAnchorsFor string
}

func newTxApi(ep *EventPersistenceWorker) *txApi {
	return &txApi{Api: ep.Api.(*model.Api)}
}

func (a *txApi) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	if a.gate != nil {
		<-a.gate
	}
	a.txs.Add(1)
	if a.failCommit.Load() > 0 {
		a.failCommit.Add(-1)
		err := a.Api.PersistInTx(ctx, func(tx *gorm.DB) error {
			if err := fn(tx); err != nil {
				return err
			}
			return errInjectedCommit
		})
		return err
	}
	err := a.Api.PersistInTx(ctx, fn)
	if err == nil {
		a.committed.Add(1)
		if a.ambiguousCommit.Load() > 0 {
			a.ambiguousCommit.Add(-1)
			return errInjectedAmbiguous
		}
	}
	return err
}

func (a *txApi) CreateEventAnchors(ctx context.Context, db *gorm.DB, anchors []*model.EventAnchor) error {
	if a.failAnchorsFor != "" && len(anchors) > 0 && anchors[0].DeviceToken == a.failAnchorsFor {
		return &pgconn.PgError{Code: "22003", Message: "injected: numeric field overflow"}
	}
	return a.Api.CreateEventAnchors(ctx, db, anchors)
}

// ackEntry is one acknowledgement: which message, and how many transactions had
// committed at the instant it was sent.
type ackEntry struct {
	idx       int
	committed int64
}

// ackLog records every ack the messages it built receive.
type ackLog struct {
	api  *txApi
	mu   sync.Mutex
	acks []ackEntry
}

type recordingAck struct {
	log *ackLog
	idx int
}

func (r recordingAck) Ack() error {
	r.log.mu.Lock()
	defer r.log.mu.Unlock()
	var committed int64
	if r.log.api != nil {
		committed = r.log.api.committed.Load()
	}
	r.log.acks = append(r.log.acks, ackEntry{idx: r.idx, committed: committed})
	return nil
}

func (l *ackLog) snapshot() []ackEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ackEntry(nil), l.acks...)
}

// acked reports how many times each message index was acknowledged.
func (l *ackLog) acked() map[int]int {
	out := map[int]int{}
	for _, a := range l.snapshot() {
		out[a.idx]++
	}
	return out
}

// waitFor waits until n acks have been recorded, or fails the test.
func (l *ackLog) waitFor(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if len(l.snapshot()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waited %s for %d acks; saw %d", within, n, len(l.snapshot()))
}

// batchEvent is a resolved 3-metric measurement from its own device (so a test can single
// one message out by device), with one anchor. withAltId false gives it no alternate id,
// so its content-derived event id and the ON CONFLICT arbiters are the only thing that
// dedupe it — the path a redelivery of such an event actually takes.
func batchEvent(i int, withAltId bool, t0 time.Time) dmodel.ResolvedEvent {
	ev := fenceCostMeasurement(fmt.Sprintf("alt-%d", i), t0.Add(time.Duration(i)*time.Millisecond), 1)
	ev.SourceDeviceToken = fmt.Sprintf("dev-%d", i)
	if !withAltId {
		ev.AltId = nil
	}
	return ev
}

// consumed wraps a resolved event as a message consumed from tenant's subject, with the
// given delivery count and an ack that records idx in log.
func consumed(t testing.TB, log *ackLog, idx int, tenant string, delivered int, ev dmodel.ResolvedEvent) messaging.Message {
	t.Helper()
	bytes, err := dmproto.MarshalResolvedEvent(&ev)
	if err != nil {
		t.Fatalf("marshal a resolved event: %v", err)
	}
	return messaging.NewConsumedMessage("instance1."+tenant+".resolved-events", bytes, delivered, nil,
		recordingAck{log: log, idx: idx})
}

// singleConnection pins the sqlite pool to one connection, so concurrent writers queue for
// it instead of tripping shared-cache SQLITE_LOCKED.
func singleConnection(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqldb, err := db.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	sqldb.SetMaxOpenConns(1)
}
