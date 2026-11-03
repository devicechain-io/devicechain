// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 With --ha, two replicas read one durable and each keeps its own cache. The bound must hold
// across them, so an admit is confirmed against the table first.
func TestTheDeviceBoundHoldsAcrossTwoReplicas(t *testing.T) {
	run := func(t *testing.T, recount bool) []model.FailedEventRecord {
		store := newFailedStore(t)
		a, b := newIndexer(t, store), newIndexer(t, store)
		a.Gate.recount, b.Gate.recount = recount, recount
		for i := 0; i < 40; i++ {
			ix := a
			if i%2 == 1 {
				ix = b
			}
			feed(ix, failedSubject, record(t, unauth, "dev-1"), uint64(i+1), base.Add(time.Duration(i)*time.Second), 1)
		}
		a.Gate.Flush(context.Background())
		b.Gate.Flush(context.Background())
		return rowsFor(t, store, "acme")
	}

	rows := run(t, true)
	assert.Len(t, rows, model.FailedEventsPerDeviceHour, "two replicas, one key: still at most 10 rows an hour")
	assert.EqualValues(t, 40-model.FailedEventsPerDeviceHour, sumSuppressed(rows), "every other record is counted")

	// NEGATIVE CONTROL: the same two replicas without the re-count each admit their own ten.
	assert.Greater(t, len(run(t, false)), model.FailedEventsPerDeviceHour,
		"without the re-count the bound is per replica, which is what this test exists to catch")
}

func TestTheReasonBoundAndTheOverflowRowHoldAcrossTwoReplicas(t *testing.T) {
	const probes = model.FailedEventsPerReasonHour + 300
	run := func(t *testing.T, recount bool) (itemized, overflow int) {
		store := newFailedStore(t)
		a, b := newIndexer(t, store), newIndexer(t, store)
		a.Gate.recount, b.Gate.recount = recount, recount
		for i := 0; i < probes; i++ {
			ix := a
			if i%2 == 1 {
				ix = b
			}
			feed(ix, failedSubject, record(t, notFound, fmt.Sprintf("probe-%d", i)), uint64(i+1),
				base.Add(time.Duration(i)*time.Millisecond), 1)
		}
		for _, r := range rowsFor(t, store, "acme") {
			if r.DeviceToken == model.OverflowDeviceToken {
				overflow++
			} else {
				itemized++
			}
		}
		return
	}

	itemized, overflow := run(t, true)
	assert.Equal(t, model.FailedEventsPerReasonHour, itemized, "the per-reason bound is a bound on the table, not on each replica")
	assert.Equal(t, 1, overflow, "one overflow row for the reason, not one per replica")

	// NEGATIVE CONTROL.
	itemized, overflow = run(t, false)
	assert.True(t, itemized > model.FailedEventsPerReasonHour || overflow > 1,
		"without the re-count each replica applies its own bound (itemized=%d overflow=%d)", itemized, overflow)
}

// countingStore counts the reads the gate makes of the table.
type countingStore struct {
	*failedStore
	reads atomic.Int64
}

func (c *countingStore) RecentFailedEvents(ctx context.Context, reason int32, device string, since time.Time, limit int) ([]model.FailedEventRef, error) {
	c.reads.Add(1)
	return c.failedStore.RecentFailedEvents(ctx, reason, device, since, limit)
}

func (c *countingStore) CountFailedEvents(ctx context.Context, reason int32, since time.Time) (int64, error) {
	c.reads.Add(1)
	return c.failedStore.CountFailedEvents(ctx, reason, since)
}

// decideAndStore is what the indexer does for one record, without the message around it.
func decideAndStore(t *testing.T, g *FailedEventGate, store *countingStore, reason int32, device string, seq int64, at time.Time) Verdict {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "acme")
	d, err := g.Decide(ctx, "acme", reason, device, seq, at)
	require.NoError(t, err)
	if d.Verdict == VerdictAdmit || d.Verdict == VerdictAdmitOverflow {
		dev := device
		if d.Verdict == VerdictAdmitOverflow {
			dev = model.OverflowDeviceToken
		}
		_, err := store.InsertFailedEventRecord(ctx, &model.FailedEventRecord{OccurredTime: at, RecordSeq: seq,
			SourceArea: "x", Reason: reason, DeviceToken: dev})
		require.NoError(t, err)
		g.Admitted(d, seq, at)
	}
	return d.Verdict
}

// 🔴 A probe of invented tokens must not cost a table read per token once the reason is over its
// bound: the answer is "overflow" whatever the device, so hydrating the device finds out nothing.
func TestAProbePastTheReasonBoundReadsNothing(t *testing.T) {
	store := &countingStore{failedStore: newFailedStore(t)}
	g := NewFailedEventGate(store)
	var seq int64
	next := func() (int64, time.Time) { seq++; return seq, base.Add(time.Duration(seq) * time.Millisecond) }

	for i := 0; i < model.FailedEventsPerReasonHour; i++ {
		s, at := next()
		require.Equal(t, VerdictAdmit, decideAndStore(t, g, store, notFound, fmt.Sprintf("probe-%d", i), s, at))
	}
	// The first past the bound writes the overflow row (and reads the table to learn the row's
	// state, once); from here the process knows everything it needs.
	s, at := next()
	require.Equal(t, VerdictAdmitOverflow, decideAndStore(t, g, store, notFound, "probe-over", s, at))

	before := store.reads.Load()
	for i := 0; i < 500; i++ {
		s, at := next()
		require.Equal(t, VerdictSuppress, decideAndStore(t, g, store, notFound, fmt.Sprintf("fresh-%d", i), s, at))
	}
	assert.Zero(t, store.reads.Load()-before, "500 unseen tokens past the bound made no table read")

	// NEGATIVE CONTROL: under the bound the same unseen tokens each cost a read (the device's
	// hydration), so the counter above would have moved had the path read the table.
	store2 := &countingStore{failedStore: newFailedStore(t)}
	g2 := NewFailedEventGate(store2)
	before = store2.reads.Load()
	for i := 0; i < 50; i++ {
		s, at := next()
		decideAndStore(t, g2, store2, notFound, fmt.Sprintf("under-%d", i), s, at)
	}
	assert.GreaterOrEqual(t, store2.reads.Load()-before, int64(50), "an unseen token under the bound is hydrated from the table")
}

// An event-management Invalid record carries the resolved event the database refused (bad data).
// It keeps its device, and is counted under that device's key like any other record.
func TestADeterministicRefusalKeepsItsDeviceAndEventType(t *testing.T) {
	payload, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "src", SourceDeviceToken: "real-9", EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{}})
	require.NoError(t, err)
	failed, err := resolvedevent.MarshalFailedEvent(resolvedevent.NewFailedEvent(uint(resolvedevent.FailureReason_Invalid),
		"event-management", "event could not be processed", fmt.Errorf("%w: \"abc\" is not numeric", model.ErrDeterministic), payload))
	require.NoError(t, err)

	rec, err := ParseFailedRecord(failed)
	require.NoError(t, err)
	assert.Equal(t, "real-9", rec.DeviceToken)
	require.NotNil(t, rec.EventType)
	assert.EqualValues(t, eventtype.Measurement, *rec.EventType)
	assert.True(t, rec.PayloadRetained)

	store := newFailedStore(t)
	ix := newIndexer(t, store)
	for i := 0; i < 13; i++ {
		feed(ix, failedSubject, failed, uint64(i+1), base.Add(time.Duration(i)*time.Second), 1)
	}
	ix.Gate.Flush(context.Background())
	rows := rowsFor(t, store, "acme")
	assert.Len(t, rows, model.FailedEventsPerDeviceHour, "counted under the device's own gate key")
	assert.Equal(t, "real-9", rows[0].DeviceToken)
	require.NotNil(t, rows[0].EventType)
	assert.EqualValues(t, 3, sumSuppressed(rows))

	// device-management's Invalid record is a different thing: the message would not parse and
	// nothing was retained, so there is no device and the row says no payload was kept.
	none, err := resolvedevent.MarshalFailedEvent(resolvedevent.NewFailedEvent(uint(resolvedevent.FailureReason_Invalid),
		"device-management", "message could not be parsed; payload not retained", fmt.Errorf("bad"), nil))
	require.NoError(t, err)
	rec, err = ParseFailedRecord(none)
	require.NoError(t, err)
	assert.Empty(t, rec.DeviceToken)
	assert.False(t, rec.PayloadRetained)
	feed(ix, failedSubject, none, 100, base.Add(time.Minute), 1)
	var kept *model.FailedEventRecord
	for _, r := range rowsFor(t, store, "acme") {
		if r.RecordSeq == 100 {
			r := r
			kept = &r
		}
	}
	require.NotNil(t, kept)
	assert.False(t, kept.PayloadRetained)
}

// A flush that cannot write logs once for the outage, with the number of rows waiting, and once
// when it recovers - not a line per row per second.
func TestAFlushOutageIsLoggedOnce(t *testing.T) {
	out := logSink.Capture(t)

	store := newFailedStore(t)
	g := NewFailedEventGate(store)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, dev := range []string{"a", "b", "c"} {
		for i := 0; i < 11; i++ {
			seq := int64(len(dev)*1000 + i + int(dev[0])*100)
			at := base.Add(time.Duration(seq) * time.Second)
			d, err := g.Decide(ctx, "acme", unauth, dev, seq, at)
			require.NoError(t, err)
			if d.Verdict == VerdictAdmit {
				_, err := store.InsertFailedEventRecord(ctx, &model.FailedEventRecord{OccurredTime: at, RecordSeq: seq,
					SourceArea: "x", Reason: unauth, DeviceToken: dev})
				require.NoError(t, err)
				g.Admitted(d, seq, at)
			}
		}
	}
	require.Equal(t, 3, g.Pending())

	sys := core.WithSystemContext(context.Background())
	require.NoError(t, store.RDB.DB(sys).Exec(`ALTER TABLE failed_event_records RENAME TO parked`).Error)
	for i := 0; i < 5; i++ {
		g.Flush(context.Background())
	}
	assert.Equal(t, 3, g.Pending(), "nothing is lost while the store is down")
	assert.Equal(t, 1, strings.Count(out.String(), "Could not write failed-event suppressed counts"), out.String())
	assert.Contains(t, out.String(), `"pendingKeys":3`)

	require.NoError(t, store.RDB.DB(sys).Exec(`ALTER TABLE parked RENAME TO failed_event_records`).Error)
	g.Flush(context.Background())
	assert.Zero(t, g.Pending())
	assert.Equal(t, 1, strings.Count(out.String(), "being written again"))
}
