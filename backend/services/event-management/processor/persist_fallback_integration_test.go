// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"gorm.io/gorm"
)

// lockProbeApi records, at the end of every batch transaction and before it commits, the
// locks the transaction holds on the erasure fence table.
type lockProbeApi struct {
	*txApi
	fenceLocks []int
}

func (a *lockProbeApi) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return a.txApi.PersistInTx(ctx, func(tx *gorm.DB) error {
		if err := fn(tx); err != nil {
			return err
		}
		var n int
		if err := tx.Raw(`SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
			WHERE l.pid = pg_backend_pid() AND l.granted AND c.relname LIKE 'purged_tenant%'`).Scan(&n).Error; err != nil {
			return err
		}
		a.fenceLocks = append(a.fenceLocks, n)
		return nil
	})
}

// A batch that falls back to ON CONFLICT still holds the erasure fence's share lock when it
// commits. The fence read inside the rolled-back first pass would have lost it (the memo
// would still have counted the tenant read), so the read is made before the savepoint.
func TestAFallbackBatchStillHoldsTheFenceLockOnPostgres(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	probe := &lockProbeApi{txApi: r.api}
	r.ep.Api = probe
	tenant := fmt.Sprintf("fl%d", time.Now().UnixNano())
	at := time.Now().UTC().Truncate(time.Hour)
	send := func() {
		var msgs []messaging.Message
		for i := 0; i < 4; i++ {
			msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, batchEvent(i, false, at)))
		}
		r.run(msgs)
	}
	send() // stores the events
	send() // redelivers them: plain pass hits 23505, rolls back, writes again
	if len(probe.fenceLocks) != 2 {
		t.Fatalf("observed %d batch transactions; want 2", len(probe.fenceLocks))
	}
	for i, n := range probe.fenceLocks {
		if n == 0 {
			t.Errorf("batch %d (the second is the fallback) committed holding no lock on the fence table", i+1)
		}
	}
}

// The first pass creates a chunk, then meets a duplicate in the same batch: the chunk
// creation is rolled back with the pass and made again by the second. Nothing is lost,
// nothing is stored twice.
func TestAFallbackBatchThatCreatesAChunkOnPostgres(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	tenant := fmt.Sprintf("nc%d", time.Now().UnixNano())
	at := time.Now().UTC().Truncate(time.Hour)
	var first []messaging.Message
	for i := 0; i < 3; i++ {
		first = append(first, consumed(t, r.acks, i, tenant, 1, batchEvent(i, false, at)))
	}
	r.run(first)

	// One event in a chunk that does not exist yet (a year back), ahead of three duplicates.
	old := at.Add(-365 * 24 * time.Hour)
	batch := []messaging.Message{consumed(t, r.acks, 10, tenant, 1, batchEvent(50, false, old))}
	for i := 0; i < 3; i++ {
		batch = append(batch, consumed(t, r.acks, 11+i, tenant, 1, batchEvent(i, false, at)))
	}
	before := r.api.txs.Load()
	r.run(batch)
	if got := r.api.txs.Load() - before; got != 1 {
		t.Errorf("took %d transactions; want 1", got)
	}
	db := r.mgr.DB(core.WithTenant(context.Background(), tenant))
	var e, m int64
	if err := db.Model(&model.Event{}).Count(&e).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.MeasurementEvent{}).Count(&m).Error; err != nil {
		t.Fatal(err)
	}
	if e != 4 || m != 12 {
		t.Errorf("stored %d events / %d measurements; want 4 / 12", e, m)
	}
	var chunks int64
	if err := r.mgr.DB(core.WithSystemContext(context.Background())).Raw(
		`SELECT count(*) FROM timescaledb_information.chunks WHERE hypertable_schema = 'event-management'
		 AND hypertable_name = 'events' AND range_start <= ? AND range_end > ?`, old, old).Scan(&chunks).Error; err != nil {
		t.Fatal(err)
	}
	if chunks != 1 {
		t.Errorf("%d chunks cover the old event; want 1", chunks)
	}
	if len(r.failures) != 0 {
		t.Errorf("reported failures: %+v", r.failures)
	}
}
