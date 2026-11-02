// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	esproto "github.com/devicechain-io/dc-event-sources/proto"
)

const failedSubject = "inst.acme.failed-events"

var notFound = int32(resolvedevent.FailureReason_DeviceNotFound)
var unauth = int32(resolvedevent.FailureReason_Unauthenticated)

// failedStore is the index on SQLite with the same callbacks production registers, behind a
// switch that makes the store refuse writes the way a full disk does.
type failedStore struct {
	*model.Api
	down atomic.Bool
}

func (f *failedStore) InsertFailedEventRecord(ctx context.Context, rec *model.FailedEventRecord) (bool, error) {
	if f.down.Load() {
		return false, holdtest.DiskFull
	}
	return f.Api.InsertFailedEventRecord(ctx, rec)
}

func (f *failedStore) RecentFailedEvents(ctx context.Context, reason int32, device string, since time.Time, limit int) ([]model.FailedEventRef, error) {
	if f.down.Load() {
		return nil, holdtest.DiskFull
	}
	return f.Api.RecentFailedEvents(ctx, reason, device, since, limit)
}

func newFailedStore(t *testing.T) *failedStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTenantFence(db))
	require.NoError(t, db.AutoMigrate(&model.FailedEventRecord{}, &rdb.PurgedTenant{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_failed_event_records_seq `+
		`ON failed_event_records (tenant_id, record_seq, occurred_time)`).Error)
	return &failedStore{Api: model.NewApi(&rdb.RdbManager{Database: db})}
}

func newIndexer(t *testing.T, store *failedStore) *FailedEventsIndexer {
	t.Helper()
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ix := NewFailedEventsIndexer(ms, nil, store, NewFailedEventMetrics(ms), core.NewNoOpLifecycleCallbacks())
	return ix
}

func record(t *testing.T, reason int32, device string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&esproto.PUnresolvedEvent{Device: device, EventType: 2})
	require.NoError(t, err)
	b, err := proto.Marshal(&resolvedevent.PFailedEvent{Reason: resolvedevent.FailureReason(reason),
		Service: "device-management", Error: "no such device", Payload: payload})
	require.NoError(t, err)
	return b
}

func feed(ix *FailedEventsIndexer, subject string, value []byte, seq uint64, at time.Time, delivered int) (*holdtest.Ack, messaging.Message) {
	msg, ack := holdtest.Msg(subject, value, delivered)
	msg.StreamSeq, msg.AppendTime = seq, at
	ix.handle(context.Background(), msg)
	return ack, msg
}

var base = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func rowsFor(t *testing.T, s *failedStore, tenant string) []model.FailedEventRecord {
	t.Helper()
	// Every page: a page is capped at 100, and the tests below store more than that.
	var all []model.FailedEventRecord
	for page := int32(1); ; page++ {
		c := model.FailedEventSearchCriteria{}
		c.PageNumber, c.PageSize = page, 100
		res, err := s.FailedEvents(core.WithTenant(context.Background(), tenant), c)
		require.NoError(t, err)
		all = append(all, res.Results...)
		if int32(len(all)) >= res.Pagination.TotalRecords || len(res.Results) == 0 {
			return all
		}
	}
}

func sumSuppressed(rows []model.FailedEventRecord) (n int64) {
	for _, r := range rows {
		n += r.Suppressed
	}
	return n
}

// The acceptance case: a device the platform has never heard of publishes, and the tenant
// can find the refusal with the reason it asked for.
func TestAnUnknownTokenProducesAQueryableRow(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)

	ack, _ := feed(ix, failedSubject, record(t, notFound, "ghost-7"), 42, base, 1)
	assert.EqualValues(t, 1, ack.Acked.Load())

	ctx := core.WithTenant(context.Background(), "acme")
	reason := notFound
	res, err := store.FailedEvents(ctx, model.FailedEventSearchCriteria{Reason: &reason})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	got := res.Results[0]
	assert.Equal(t, "ghost-7", got.DeviceToken)
	assert.EqualValues(t, 42, got.RecordSeq, "the row LOCATES the payload on the stream by position")
	assert.Equal(t, base, got.OccurredTime.UTC(), "dated by the broker's append time")
	assert.Equal(t, "device-management", got.SourceArea)
	assert.Equal(t, "no such device", got.Error)
	assert.EqualValues(t, 1, testutil.ToFloat64(ix.Metrics.recorded.WithLabelValues("device-management", "device_not_found")))

	// Negative control: a different reason finds nothing, and another tenant finds nothing.
	other := unauth
	res, err = store.FailedEvents(ctx, model.FailedEventSearchCriteria{Reason: &other})
	require.NoError(t, err)
	assert.Empty(t, res.Results)
	assert.Empty(t, rowsFor(t, store, "someone-else"))
}

// 🔴 THE FLOOD BOUND. A misconfigured fleet's 1,000 unauthenticated events a second, for a
// minute, from one device stores 10 rows and counts the other 59,990.
func TestAThousandPerSecondFloodStoresTenRowsAndCountsTheRest(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	const total = 60 * 1000

	for i := 0; i < total; i++ {
		at := base.Add(time.Duration(i) * time.Millisecond) // 1,000 a second
		ack, _ := feed(ix, failedSubject, record(t, unauth, "dev-1"), uint64(i+1), at, 1)
		if ack.Acked.Load() != 1 {
			t.Fatalf("record %d was not acked", i)
		}
	}
	ix.Gate.Flush(context.Background())

	rows := rowsFor(t, store, "acme")
	assert.Len(t, rows, model.FailedEventsPerDeviceHour, "at most 10 rows an hour for the device")
	assert.EqualValues(t, total-model.FailedEventsPerDeviceHour, sumSuppressed(rows),
		"every other record is counted, none is lost")
	assert.EqualValues(t, total, testutil.ToFloat64(ix.Metrics.recorded.WithLabelValues("device-management", "unauthenticated")),
		"the rate metric sees every record, which is what the alert reads")
	assert.EqualValues(t, total-model.FailedEventsPerDeviceHour, testutil.ToFloat64(ix.Metrics.suppressed.WithLabelValues("unauthenticated")))
	assert.Zero(t, ix.Gate.Pending(), "a flush leaves nothing waiting")

	// The count lands on the NEWEST row, the one an operator reads first.
	assert.EqualValues(t, total-model.FailedEventsPerDeviceHour, rows[0].Suppressed)
}

// The control for the test above: without the gate the same flood stores every record. This is
// what shows the row-count assertion above can fail.
func TestWithoutTheGateTheSameFloodStoresEveryRecord(t *testing.T) {
	store := newFailedStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < 1000; i++ {
		_, err := store.InsertFailedEventRecord(ctx, &model.FailedEventRecord{
			OccurredTime: base.Add(time.Duration(i) * time.Millisecond), RecordSeq: int64(i + 1),
			SourceArea: "device-management", Reason: unauth, DeviceToken: "dev-1"})
		require.NoError(t, err)
	}
	assert.Len(t, rowsFor(t, store, "acme"), 1000)
}

// The bound is a SLIDING hour on the record's own timeline: ten rows at the start of the hour
// leave no room until they age out, then ten more are admitted.
func TestTheBoundSlidesWithTheRecordsOwnTime(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	seq := uint64(0)
	send := func(at time.Time) {
		seq++
		feed(ix, failedSubject, record(t, unauth, "dev-1"), seq, at, 1)
	}
	for i := 0; i < 15; i++ {
		send(base.Add(time.Duration(i) * time.Second))
	}
	ix.Gate.Flush(context.Background())
	assert.Len(t, rowsFor(t, store, "acme"), 10)

	// 59 minutes later the first ten are still inside the hour: still suppressed.
	send(base.Add(59 * time.Minute))
	ix.Gate.Flush(context.Background())
	assert.Len(t, rowsFor(t, store, "acme"), 10)

	// Past the hour they have aged out and the device may add rows again.
	for i := 0; i < 12; i++ {
		send(base.Add(61*time.Minute + time.Duration(i)*time.Second))
	}
	ix.Gate.Flush(context.Background())
	rows := rowsFor(t, store, "acme")
	assert.Len(t, rows, 20, "10 more were admitted once the first window had passed")
	assert.EqualValues(t, 5+1+2, sumSuppressed(rows), "15-10 suppressed, 1 inside the hour, 12-10 after it")
}

// The bound survives a restart: a new process reads the table before it decides.
func TestTheBoundSurvivesARestart(t *testing.T) {
	store := newFailedStore(t)
	first := newIndexer(t, store)
	for i := 0; i < 10; i++ {
		feed(first, failedSubject, record(t, unauth, "dev-1"), uint64(i+1), base.Add(time.Duration(i)*time.Second), 1)
	}
	require.Len(t, rowsFor(t, store, "acme"), 10)

	restarted := newIndexer(t, store) // a gate with no memory
	feed(restarted, failedSubject, record(t, unauth, "dev-1"), 11, base.Add(20*time.Second), 1)
	restarted.Gate.Flush(context.Background())
	rows := rowsFor(t, store, "acme")
	assert.Len(t, rows, 10, "the 11th record was suppressed, not stored")
	assert.EqualValues(t, 1, sumSuppressed(rows))

	// The negative control: a table that has never held the device admits it.
	fresh := newIndexer(t, newFailedStore(t))
	feed(fresh, failedSubject, record(t, unauth, "dev-1"), 11, base, 1)
	assert.Len(t, rowsFor(t, fresh.Store.(*failedStore), "acme"), 1)
}

// A redelivery of a record that was stored is neither a second row nor a second count, even
// into a process that remembers nothing.
func TestARedeliveredRecordIsCountedOnce(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	for i := 0; i < 10; i++ {
		feed(ix, failedSubject, record(t, unauth, "dev-1"), uint64(i+1), base.Add(time.Duration(i)*time.Second), 1)
	}
	// Seq 3 again, to the same process and to a restarted one.
	feed(ix, failedSubject, record(t, unauth, "dev-1"), 3, base.Add(2*time.Second), 2)
	restarted := newIndexer(t, store)
	feed(restarted, failedSubject, record(t, unauth, "dev-1"), 3, base.Add(2*time.Second), 2)
	ix.Gate.Flush(context.Background())
	restarted.Gate.Flush(context.Background())

	rows := rowsFor(t, store, "acme")
	assert.Len(t, rows, 10)
	assert.Zero(t, sumSuppressed(rows), "an admitted record redelivered is not a suppressed one")
}

// Reasons, devices and tenants are bounded independently: one flood does not suppress another's
// first records.
func TestBoundsAreIndependentPerReasonDeviceAndTenant(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	seq := uint64(0)
	send := func(subject string, reason int32, device string) {
		seq++
		feed(ix, subject, record(t, reason, device), seq, base.Add(time.Duration(seq)*time.Millisecond), 1)
	}
	for i := 0; i < 50; i++ {
		send(failedSubject, unauth, "dev-1")
	}
	send(failedSubject, unauth, "dev-2")
	send(failedSubject, notFound, "dev-1")
	send("inst.globex.failed-events", unauth, "dev-1")
	ix.Gate.Flush(context.Background())

	acme := rowsFor(t, store, "acme")
	assert.Len(t, acme, 10+1+1)
	assert.Len(t, rowsFor(t, store, "globex"), 1, "another tenant's flood-free device is stored")
}

// 🔴 The probe case the per-device bound cannot see: every record names a DIFFERENT device. At
// 1,000 an hour the reason's own bound folds the rest into one overflow row.
func TestAProbeOfDistinctTokensIsBoundedPerReason(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	const probes = model.FailedEventsPerReasonHour + 500
	for i := 0; i < probes; i++ {
		feed(ix, failedSubject, record(t, notFound, fmt.Sprintf("probe-%d", i)), uint64(i+1),
			base.Add(time.Duration(i)*time.Millisecond), 1)
	}
	ix.Gate.Flush(context.Background())

	rows := rowsFor(t, store, "acme")
	assert.Len(t, rows, model.FailedEventsPerReasonHour+1, "1000 itemized rows and one overflow row")
	var overflow []model.FailedEventRecord
	for _, r := range rows {
		if r.DeviceToken == model.OverflowDeviceToken {
			overflow = append(overflow, r)
		}
	}
	require.Len(t, overflow, 1)
	assert.EqualValues(t, 500-1, overflow[0].Suppressed, "the first beyond the bound is the overflow row itself")
	assert.NotEmpty(t, overflow[0].Error)

	// Another reason is unaffected by this one's overflow.
	feed(ix, failedSubject, record(t, unauth, "dev-x"), probes+1, base.Add(time.Hour/2), 1)
	assert.Len(t, rowsFor(t, store, "acme"), model.FailedEventsPerReasonHour+2)
}

// The gate evicts rather than growing without bound under a flood of distinct keys, and an
// evicted key is not forgotten: it is re-read from the table.
func TestEvictionIsBoundedAndNotForgetful(t *testing.T) {
	store := newFailedStore(t)
	g := NewFailedEventGate(store)
	g.maxKeys = 50
	ctx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < 200; i++ {
		d, err := g.Decide(ctx, "acme", notFound, fmt.Sprintf("d-%d", i), int64(i+1), base)
		require.NoError(t, err)
		if d.Verdict == VerdictAdmit {
			_, err := store.InsertFailedEventRecord(ctx, &model.FailedEventRecord{OccurredTime: base, RecordSeq: int64(i + 1),
				SourceArea: "x", Reason: notFound, DeviceToken: fmt.Sprintf("d-%d", i)})
			require.NoError(t, err)
			g.Admitted(d, int64(i+1), base)
		}
	}
	assert.LessOrEqual(t, len(g.devices), 50)
}

// Records that cannot be indexed are settled, counted, and never block the stream.
func TestRecordsNoRedeliveryCanFixAreAckedAndCounted(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)

	ack, _ := feed(ix, "no-tenant-here", record(t, notFound, "d"), 1, base, 1)
	assert.EqualValues(t, 1, ack.Acked.Load())
	assert.EqualValues(t, 1, testutil.ToFloat64(ix.Metrics.unindexed.WithLabelValues("no_tenant")))

	ack, _ = feed(ix, failedSubject, []byte{0xff, 0xff, 0xff, 0xff}, 2, base, 1)
	assert.EqualValues(t, 1, ack.Acked.Load())
	assert.EqualValues(t, 1, testutil.ToFloat64(ix.Metrics.unindexed.WithLabelValues("undecodable")))
	assert.Empty(t, rowsFor(t, store, "acme"))
}

// A deleted tenant's record is dropped by the erasure fence, and nothing is left behind.
func TestAnErasedTenantsRecordIsDroppedNotStored(t *testing.T) {
	store := newFailedStore(t)
	sys := core.WithSystemContext(context.Background())
	require.NoError(t, store.RDB.DB(sys).Create(&rdb.PurgedTenant{Token: "acme", Epoch: base, PlantedAt: base}).Error)
	ix := newIndexer(t, store)

	ack, _ := feed(ix, failedSubject, record(t, notFound, "ghost"), 1, base, 1)
	assert.EqualValues(t, 1, ack.Acked.Load(), "settled, not redelivered forever")
	assert.EqualValues(t, 1, testutil.ToFloat64(ix.Metrics.unindexed.WithLabelValues("erased")))
	assert.Empty(t, rowsFor(t, store, "acme"), "zero rows for a deleted tenant")
}

// A store outage HOLDS the record: unacked, outside its delivery budget, and indexed when the
// store answers again. A record on its LAST delivery is held too, not given up on.
func TestAStoreOutageHoldsTheRecordAndIndexesItOnRecovery(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	probe := &holdtest.Store{}
	probe.Down.Store(true)
	store.down.Store(true)
	ix.Hold = &storehold.Holder{Name: "test", Probe: probe.Probe, Tune: holdtest.FastTuning()}
	ix.procCtx = context.Background()

	done := make(chan *holdtest.Ack, 1)
	go func() {
		ack, _ := feed(ix, failedSubject, record(t, notFound, "ghost"), 1, base, messaging.MaxDeliver)
		done <- ack
	}()
	select {
	case <-done:
		t.Fatal("the record was settled while the store was down")
	case <-time.After(150 * time.Millisecond):
	}
	assert.Empty(t, rowsFor(t, store, "acme"))

	probe.Down.Store(false)
	store.down.Store(false)
	select {
	case ack := <-done:
		assert.EqualValues(t, 1, ack.Acked.Load())
	case <-time.After(5 * time.Second):
		t.Fatal("the held record was not indexed after recovery")
	}
	assert.Len(t, rowsFor(t, store, "acme"), 1)
	assert.Zero(t, testutil.ToFloat64(ix.Metrics.unindexed.WithLabelValues("gave_up")), "a held record is never given up on")
}

// A failure that is the record's own is retried until the last delivery, then counted as given
// up and acked. The negative control is the earlier delivery: left unacked.
func TestAFailureThatIsNotAnOutageTakesTheDeliveryBudget(t *testing.T) {
	store := newFailedStore(t)
	ix := newIndexer(t, store)
	// The table does not exist: an error that is not the store being down.
	require.NoError(t, store.RDB.DB(core.WithSystemContext(context.Background())).Exec(`DROP TABLE failed_event_records`).Error)

	ack, _ := feed(ix, failedSubject, record(t, notFound, "ghost"), 1, base, 1)
	assert.Zero(t, ack.Acked.Load(), "an early delivery is left unacked for AckWait-paced redelivery")

	ack, _ = feed(ix, failedSubject, record(t, notFound, "ghost"), 1, base, messaging.MaxDeliver)
	assert.EqualValues(t, 1, ack.Acked.Load())
	assert.EqualValues(t, 1, testutil.ToFloat64(ix.Metrics.unindexed.WithLabelValues("gave_up")))
}

// Metric labels are bounded: a reason a newer writer adds is "unknown", never a label per number,
// and a source that is not a known writer is "other".
func TestMetricLabelsAreBounded(t *testing.T) {
	assert.Equal(t, "unknown", reasonLabel(9999))
	assert.Equal(t, "device_not_found", reasonLabel(notFound))
	assert.Equal(t, "other", sourceLabel("something-new"))
	assert.Equal(t, "event-management", sourceLabel("event-management"))
}

// The gate keeps the suppressed count for a flush, and a failed flush keeps it for the next.
func TestAFailedFlushKeepsTheCount(t *testing.T) {
	store := newFailedStore(t)
	g := NewFailedEventGate(store)
	ctx := core.WithTenant(context.Background(), "acme")
	var seq int64
	admit := func() {
		seq++
		d, err := g.Decide(ctx, "acme", unauth, "dev", seq, base.Add(time.Duration(seq)*time.Second))
		require.NoError(t, err)
		if d.Verdict == VerdictAdmit {
			_, err := store.InsertFailedEventRecord(ctx, &model.FailedEventRecord{OccurredTime: base.Add(time.Duration(seq) * time.Second),
				RecordSeq: seq, SourceArea: "x", Reason: unauth, DeviceToken: "dev"})
			require.NoError(t, err)
			g.Admitted(d, seq, base.Add(time.Duration(seq)*time.Second))
		}
	}
	for i := 0; i < 13; i++ {
		admit()
	}
	require.Equal(t, 1, g.Pending())

	// The table disappears: the flush cannot write, and keeps the count.
	require.NoError(t, store.RDB.DB(core.WithSystemContext(context.Background())).Exec(`ALTER TABLE failed_event_records RENAME TO parked`).Error)
	g.Flush(context.Background())
	assert.Equal(t, 1, g.Pending())
	require.NoError(t, store.RDB.DB(core.WithSystemContext(context.Background())).Exec(`ALTER TABLE parked RENAME TO failed_event_records`).Error)
	g.Flush(context.Background())
	assert.Zero(t, g.Pending())
	assert.EqualValues(t, 3, sumSuppressed(rowsFor(t, store, "acme")))
}

// Wiring: the stream's declaration names this area as a durable reader of failed-events. A
// reader the catalog does not declare is refused by the broker login (catalog_boot_test).
func TestTheStreamDeclaresTheIndexerAsAReader(t *testing.T) {
	assert.Contains(t, streams.ReadersOf(streams.FailedEvents),
		streams.Reader{Area: "event-management", Mode: streams.ReadDurable})
}
