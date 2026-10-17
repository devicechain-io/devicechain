// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The ownership outbox on the real engine: the upsert's conflict arm, the drain's SKIP LOCKED
// delete against a writer holding the row, and what queueing and draining cost.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Ownership -v
package model

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// countingOwnership counts the facts published through it.
type countingOwnership struct {
	mu    sync.Mutex
	facts []DeviceOwnershipChangedEvent
}

func (c *countingOwnership) PublishDeviceOwnershipChanged(_ context.Context, e *DeviceOwnershipChangedEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.facts = append(c.facts, *e)
	return nil
}

func (c *countingOwnership) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.facts)
}

// ownershipPgRig is a fresh tenant on the real engine with n devices.
func ownershipPgRig(t *testing.T, n int) (*Api, context.Context, []uint) {
	t.Helper()
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("ownfact-%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "pump"})
	require.NoError(t, err)
	types, err := api.DeviceTypesByToken(ctx, []string{"pump"})
	require.NoError(t, err)
	rows := make([]Device, n)
	for i := range rows {
		rows[i] = Device{TokenReference: rdb.TokenReference{Token: fmt.Sprintf("pump-%05d", i)}, DeviceTypeId: types[0].ID}
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&rows, 1000).Error)
	ids := make([]uint, n)
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return api, ctx, ids
}

func queueCommitted(t *testing.T, api *Api, ctx context.Context, ids []uint) {
	t.Helper()
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := queueOwnershipOnTx(tx, ids)
		return err
	}))
}

func queuedFor(t *testing.T, api *Api, ctx context.Context) []PendingOwnershipFact {
	t.Helper()
	rows := make([]PendingOwnershipFact, 0)
	require.NoError(t, api.RDB.DB(ctx).Order("device_id").Find(&rows).Error)
	return rows
}

// 🔴 THE DRAIN NEVER WAITS ON A WRITER. A writer re-queueing a device holds the row until it
// commits; the drain publishing that device's older request must skip the row rather than
// wait on it — a wait would hold the rows it had already reached, and a writer queueing two
// of them could close a cycle the engine resolves by failing one side, possibly the request.
// The skipped row survives, carrying the writer's generation, and the next pass announces it.
func TestTheOwnershipDrainSkipsARowAWriterHolds(t *testing.T) {
	api, ctx, ids := ownershipPgRig(t, 2)
	pub := &countingOwnership{}
	api.DeviceOwnershipPublisher = pub
	queueCommitted(t, api, ctx, ids)
	before := queuedFor(t, api, ctx)
	require.Len(t, before, 2)

	held := make(chan struct{})
	release := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := queueOwnershipOnTx(tx, ids[:1]); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	drained := make(chan error, 1)
	go func() {
		_, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
		drained <- err
	}()
	select {
	case err := <-drained:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the drain waited on a row an open writer holds")
	}
	require.Equal(t, 2, pub.count(), "both committed requests were published")
	rows := queuedFor(t, api, ctx)
	require.Len(t, rows, 1, "the row the writer holds was deleted, or the free one was kept")
	require.Equal(t, ids[0], rows[0].DeviceId)

	close(release)
	require.NoError(t, <-writerDone)
	after := queuedFor(t, api, ctx)
	require.Len(t, after, 1)
	require.NotEqual(t, before[0].Generation, after[0].Generation, "the writer's re-queue did not replace the generation")

	_, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, 3, pub.count(), "the re-queued device was not announced again")
	require.Empty(t, queuedFor(t, api, ctx))
}

// TestOwnershipOutboxCost measures what the outbox adds: the statements and time of queueing
// one device (a relationship change) and the 10 000 members of a large access group (a toggle),
// and how fast the drain publishes them through a publisher that costs nothing. Logged, with
// bounds generous enough to hold on a loaded CI runner and tight enough to catch a per-row
// statement.
func TestOwnershipOutboxCost(t *testing.T) {
	const members = 10_000
	api, ctx, ids := ownershipPgRig(t, members)
	pub := &countingOwnership{}
	api.DeviceOwnershipPublisher = pub

	counter := rdbtest.NewStatementCounter("pending_ownership_facts")
	counter.Record(true)
	counted := api.RDB.Database.Session(&gorm.Session{Logger: counter})
	measure := func(n int) (time.Duration, int64) {
		counter.Reset()
		start := time.Now()
		require.NoError(t, counted.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			_, err := queueOwnershipOnTx(tx, ids[:n])
			return err
		}))
		_, marked := counter.Counts()
		return time.Since(start), marked
	}
	// The baseline a queue is added to: a transaction with one trivial statement, so the
	// difference is the queue's own statement rather than the transaction around it.
	empty := time.Duration(1 << 62)
	for range 5 {
		start := time.Now()
		require.NoError(t, counted.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return tx.Exec("SELECT 1").Error
		}))
		empty = min(empty, time.Since(start))
	}
	t.Logf("baseline: a transaction with one trivial statement, %v", empty)
	// Warm the connection and the plan, then measure each size twice and keep the faster.
	measure(1)
	for _, n := range []int{1, 100, members} {
		best, stmts := time.Duration(1<<62), int64(0)
		for range 3 {
			d, s := measure(n)
			best, stmts = min(best, d), s
		}
		t.Logf("queue %5d device(s): %d statement(s) on the outbox, %v (one transaction)", n, stmts, best)
		require.EqualValues(t, 1, stmts, "queueing %d devices took more than one statement: %v", n, counter.MarkedSQL())
	}

	start := time.Now()
	passes := 0
	for len(queuedFor(t, api, ctx)) > 0 {
		_, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
		require.NoError(t, err)
		passes++
		require.Less(t, passes, 100, "the drain is not making progress")
	}
	elapsed := time.Since(start)
	t.Logf("drain %d queued facts: %d passes of up to %d, %v (%.0f facts/s, publisher excluded)",
		members, passes, OwnershipDrainPageSize, elapsed, float64(members)/elapsed.Seconds())
	require.GreaterOrEqual(t, pub.count(), members)
}
