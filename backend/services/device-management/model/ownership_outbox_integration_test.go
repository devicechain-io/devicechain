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

// countingOwnership counts the facts published through it for ONE tenant, the rig's.
//
// 🔴 THE DRAIN IS CROSS-TENANT BY DESIGN AND THE DATABASE IS SHARED. Every rig on the real
// engine is a tenant of its own in one database that nothing truncates, and every edge a
// rig writes queues a row that stays until something drains it — so a drain here publishes
// the facts of every earlier rig, and of any test running beside this one, through this
// publisher. Counted without the tenant filter, those made "both facts were published" read
// as three or thirty (the integration job failed on exactly that).
type countingOwnership struct {
	tenant string
	mu     sync.Mutex
	facts  []DeviceOwnershipChangedEvent
}

func (c *countingOwnership) PublishDeviceOwnershipChanged(ctx context.Context, e *DeviceOwnershipChangedEvent) error {
	if tenant, _ := core.TenantFromContext(ctx); tenant != c.tenant {
		return nil
	}
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

// tenantOf is the tenant ctx names.
func tenantOf(ctx context.Context) string {
	tenant, _ := core.TenantFromContext(ctx)
	return tenant
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
	pub := &countingOwnership{tenant: tenantOf(ctx)}
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
	pub := &countingOwnership{tenant: tenantOf(ctx)}
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

// A device named twice in one write is one row on the real engine, whose upsert refuses a
// statement naming the same key twice ("cannot affect row a second time"): the duplicates
// are folded before the statement, not by it.
func TestADeviceQueuedTwiceInOneWriteIsOneRowOnTheRealEngine(t *testing.T) {
	api, ctx, ids := ownershipPgRig(t, 1)
	queueCommitted(t, api, ctx, []uint{ids[0], ids[0]})
	require.Len(t, queuedFor(t, api, ctx), 1)
}

// blockingOwnership holds its FIRST publish until released, so a pass can be caught in
// flight; every later publish returns at once, so a second pass that wrongly runs beside
// the first completes and is seen to, rather than hanging the test on the same gate.
type blockingOwnership struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingOwnership) PublishDeviceOwnershipChanged(context.Context, *DeviceOwnershipChangedEvent) error {
	first := false
	b.once.Do(func() { first = true; close(b.entered) })
	if first {
		<-b.release
	}
	return nil
}

// One replica drains at a time: a pass that finds another replica's pass in flight is
// skipped — its own outcome, neither a success that moves the last-success gauge nor a
// failure that pages — and the facts are published once.
func TestOneReplicaDrainsAtATime(t *testing.T) {
	api, ctx, ids := ownershipPgRig(t, 1)
	queueCommitted(t, api, ctx, ids)
	pub := &blockingOwnership{entered: make(chan struct{}), release: make(chan struct{})}
	api.DeviceOwnershipPublisher = pub
	first := make(chan error, 1)
	go func() { first <- api.DrainOwnershipFactsLocked(context.Background()) }()
	select {
	case <-pub.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the first pass never reached a publish")
	}
	err := api.DrainOwnershipFactsLocked(context.Background())
	require.ErrorIs(t, err, core.ErrPassSkipped, "a second pass ran beside one in flight")
	close(pub.release)
	require.NoError(t, <-first)
	require.Empty(t, queuedFor(t, api, ctx))
}

// 🔴 TWO CONSECUTIVE DRAINS ON REPLICAS WHOSE CLOCKS DISAGREE STAMP NON-DECREASING TIMES. The
// drain lock passes between replicas, and device-state refuses a fact that is not strictly
// later than the one it holds, so a fact stamped from a replica running behind would be
// refused though it is the newer statement. Replica B's process clock is an hour behind A's
// here; both facts must carry the database's time instead, inside the bracket the test reads
// from the database around each pass.
func TestFactsFromReplicasWithSkewedClocksAreOrderedByTheDatabaseClock(t *testing.T) {
	apiA, ctx, ids := ownershipPgRig(t, 1)
	apiB := NewApi(apiA.RDB)
	now := time.Now()
	apiA.ownershipNow = func() time.Time { return now.Add(time.Hour) }
	apiB.ownershipNow = func() time.Time { return now.Add(-time.Hour) }
	pub := &countingOwnership{tenant: tenantOf(ctx)}
	apiA.DeviceOwnershipPublisher, apiB.DeviceOwnershipPublisher = pub, pub

	dbNow := func() time.Time {
		var ts time.Time
		require.NoError(t, apiA.RDB.DB(core.WithSystemContext(context.Background())).Raw("SELECT clock_timestamp()").Scan(&ts).Error)
		return ts.UTC()
	}
	var last time.Time
	for i, replica := range []*Api{apiA, apiB, apiA, apiB} {
		queueCommitted(t, replica, ctx, ids)
		lo := dbNow()
		_, err := replica.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
		require.NoError(t, err)
		hi := dbNow()
		require.Equal(t, i+1, pub.count())
		got := pub.facts[i].ChangedTime
		require.False(t, got.Before(lo) || got.After(hi), "pass %d: fact dated %v, outside the database's %v..%v", i, got, lo, hi)
		require.False(t, got.Before(last), "pass %d: fact dated %v, before the previous pass's %v", i, got, last)
		last = got
	}
}
