// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Batched persistence against a REAL TimescaleDB: what sqlite cannot show — a statement
// refused by the server mid-transaction, after which Postgres refuses every later
// statement in it (25P02), and the erasure fence's read on a real transaction — plus the
// benchmark that measures what batching buys.
//
// Run the tests with hack/integration-tests.sh. Run the benchmark against the operand image
// (trust auth, so the password is ignored):
//
//	. deploy/images/timescaledb/standalone.sh
//	PORT=$(dc_operand_start ghcr.io/devicechain-io/postgresql-timescaledb:17.10-ts2.28.3-r1 \
//	         dc-batchbench "127.0.0.1::5432")
//	cd backend/services/event-management
//	DC_IT_PGPORT=$PORT go test -tags integration -run '^$' -bench PersistBatch \
//	  -benchtime=2048x -count=3 -p 1 ./processor/
//
// That server flushes its WAL locally and nothing more. On a replicated event store each
// COMMIT also waits for a synchronous standby, which is where batching matters most; to
// measure that, give the server a streaming standby and set
// synchronous_standby_names on it before running the same command.
package processor

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog"
)

// pgBatchRig is a batching worker over the real server, with what it reports recorded.
type pgBatchRig struct {
	ep   *EventPersistenceWorker
	api  *txApi
	mgr  *rdb.RdbManager
	acks *ackLog

	mu       sync.Mutex
	failures []failure
}

func newPgBatchRig(t *testing.T, instance string, maxBatch int) *pgBatchRig {
	t.Helper()
	mgr, _ := newFenceBenchManager(t, instance, true)
	api := &txApi{Api: model.NewApi(mgr)}
	r := &pgBatchRig{api: api, mgr: mgr, acks: &ackLog{api: api}}
	r.ep = &EventPersistenceWorker{Api: api, MaxBatch: maxBatch,
		Invalid: func(err error, _ messaging.Message) { t.Errorf("unexpected invalid message: %v", err) },
		Failed: func(tenant string, reason uint, ev dmodel.ResolvedEvent, _ error, _ string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.failures = append(r.failures, failure{tenant: tenant, reason: reason, device: ev.SourceDeviceToken})
		}}
	return r
}

func (r *pgBatchRig) run(msgs []messaging.Message) {
	ch := make(chan messaging.Message, len(msgs))
	for _, m := range msgs {
		ch <- m
	}
	close(ch)
	r.ep.Unpersisted = ch
	r.ep.Process(context.Background())
}

func (r *pgBatchRig) count(t *testing.T, tenant, device string) int64 {
	t.Helper()
	var n int64
	q := r.mgr.DB(core.WithTenant(context.Background(), tenant)).Model(&model.Event{})
	if device != "" {
		q = q.Where("device_token = ?", device)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// A value the column cannot hold is refused BY THE SERVER, part-way through the batch's
// transaction. The seven around it are stored, the refused one is reported as invalid and
// stored nowhere, and it costs four transactions: the batch, written grouped, whose
// measurement insert carried all eight messages, so the server's refusal names none of
// them; the same batch one message at a time, which finds it; the refused message alone;
// and the other seven.
func TestAValueTheServerRefusesInABatchOnPostgres(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	tenant := fmt.Sprintf("ov%d", time.Now().UnixNano())
	base := time.Now().UTC().Truncate(time.Hour)
	var msgs []messaging.Message
	for i := 0; i < 8; i++ {
		ev := batchEvent(i, true, base)
		if i == 3 {
			// numeric(20,8) holds 12 integer digits; this parses and overflows at the INSERT.
			ev.Payload.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value = "1e13"
		}
		msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, ev))
	}
	r.run(msgs)

	if got := r.api.txs.Load(); got != 4 {
		t.Errorf("took %d transactions; want 4", got)
	}
	if got := r.count(t, tenant, ""); got != 7 {
		t.Errorf("stored %d events; want 7", got)
	}
	if got := r.count(t, tenant, "dev-3"); got != 0 {
		t.Errorf("the refused message left %d event rows; want none", got)
	}
	if len(r.failures) != 1 || r.failures[0].device != "dev-3" ||
		r.failures[0].reason != uint(dmproto.FailureReason_Invalid) {
		t.Errorf("reported %+v; want dev-3 as invalid", r.failures)
	}
	if got := len(r.acks.acked()); got != 8 {
		t.Errorf("%d messages acknowledged; want 8", got)
	}
}

// The erasure fence refuses a purged tenant's messages inside a mixed batch on the real
// server, and their batch-mates are stored. Lifting the fence is the negative control.
func TestAFencedTenantInABatchOnPostgres(t *testing.T) {
	for _, fenced := range []bool{true, false} {
		t.Run(fmt.Sprintf("fenced=%v", fenced), func(t *testing.T) {
			r := newPgBatchRig(t, "embatchtest", 8)
			live := fmt.Sprintf("lv%d", time.Now().UnixNano())
			gone := fmt.Sprintf("gn%d", time.Now().UnixNano())
			plantOn(t, r.mgr, gone)
			if !fenced {
				liftOn(t, r.mgr, gone)
			}
			base := time.Now().UTC().Truncate(time.Hour)
			var msgs []messaging.Message
			for i := 0; i < 8; i++ {
				tenant := live
				if i%2 == 1 {
					tenant = gone
				}
				msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, batchEvent(i, true, base)))
			}
			r.run(msgs)

			// One failed batch sets the purged tenant's four aside together; each is then
			// refused on its own, and the live four commit together.
			wantGone, wantAcks, wantTxs := int64(0), 4, int64(6)
			if !fenced {
				wantGone, wantAcks, wantTxs = 4, 8, 1
			}
			if got := r.count(t, live, ""); got != 4 {
				t.Errorf("stored %d live events; want 4", got)
			}
			if got := r.count(t, gone, ""); got != wantGone {
				t.Errorf("stored %d events for the fenced tenant; want %d", got, wantGone)
			}
			if got := len(r.acks.acked()); got != wantAcks {
				t.Errorf("%d acknowledged; want %d", got, wantAcks)
			}
			if got := r.api.txs.Load(); got != wantTxs {
				t.Errorf("took %d transactions; want %d", got, wantTxs)
			}
		})
	}
}

// An alternate id is matched at the event store's resolution, which is the microsecond: an
// event stored at base+1500ns is stored at base+1us, and a batch re-sending its key at
// base+1500ns with other content (so another event id) must find it and skip it, or the
// alternate-id index refuses the batch. The control row re-sends at the whole microsecond,
// which matches with or without any reduction, so a failure of the first row is about
// resolution and nothing else.
func TestAnAlternateIdIsMatchedAtTheStoresResolutionOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
	}{{"a sub-microsecond instant", 1500 * time.Nanosecond}, {"control: a whole microsecond", time.Microsecond}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPgBatchRig(t, "embatchtest", 8)
			tenant := fmt.Sprintf("res%d", time.Now().UnixNano())
			at := time.Now().UTC().Truncate(time.Hour).Add(tc.offset)
			alt := fmt.Sprintf("z-%d", time.Now().UnixNano())
			r.run([]messaging.Message{consumed(t, r.acks, 100, tenant, 1, measurementOf("dev-z", at, alt, 1, "21.5"))})
			r.api.txs.Store(0)

			msgs := []messaging.Message{consumed(t, r.acks, 0, tenant, 1, measurementOf("dev-z2", at, alt, 1, "99"))}
			for i := 1; i < 4; i++ {
				msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, batchEvent(i, true, at.Truncate(time.Hour))))
			}
			r.run(msgs)

			if got := r.api.txs.Load(); got != 1 {
				t.Errorf("took %d transactions; want 1", got)
			}
			if got := len(r.acks.acked()); got != 5 {
				t.Errorf("%d messages acknowledged; want 5", got)
			}
			if len(r.failures) != 0 {
				t.Errorf("reported %+v; want nothing", r.failures)
			}
			db := r.mgr.DB(core.WithTenant(context.Background(), tenant))
			var n int64
			if err := db.Model(&model.Event{}).Where("alt_id = ?", alt).Count(&n).Error; err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != 1 {
				t.Errorf("%d events stored for the alternate id; want 1", n)
			}
			var values []float64
			if err := db.Model(&model.MeasurementEvent{}).Where("device_token IN ?", []string{"dev-z", "dev-z2"}).
				Pluck("value", &values).Error; err != nil {
				t.Fatalf("read values: %v", err)
			}
			if len(values) != 1 || values[0] != 21.5 {
				t.Errorf("stored values %v for the key; want [21.5], the first delivery's", values)
			}
		})
	}
}

// The same event twice in one batch with no alternate id: its rows are written twice IN ONE
// INSERT, and ON CONFLICT DO NOTHING must absorb a duplicate key inside a single statement
// on the real server — which sqlite proves nothing about.
func TestTheSameEventTwiceInOneStatementOnPostgres(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	tenant := fmt.Sprintf("dup%d", time.Now().UnixNano())
	at := time.Now().UTC().Truncate(time.Hour)
	r.run([]messaging.Message{
		consumed(t, r.acks, 0, tenant, 1, batchEvent(0, false, at)),
		consumed(t, r.acks, 1, tenant, 1, batchEvent(0, false, at)),
	})
	if got := r.api.txs.Load(); got != 1 {
		t.Errorf("took %d transactions; want 1", got)
	}
	db := r.mgr.DB(core.WithTenant(context.Background(), tenant))
	var e, m, a int64
	for _, c := range []struct {
		model any
		n     *int64
	}{{&model.Event{}, &e}, {&model.MeasurementEvent{}, &m}, {&model.EventAnchor{}, &a}} {
		if err := db.Model(c.model).Count(c.n).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
	}
	if e != 1 || m != 3 || a != 1 {
		t.Errorf("stored %d/%d/%d event/measurement/anchor rows; want 1/3/1", e, m, a)
	}
	if got := len(r.acks.acked()); got != 2 {
		t.Errorf("%d messages acknowledged; want 2", got)
	}
}

// A purged tenant's grouped write stores nothing, even when its group carries an event too
// large for one statement, and its batch-mates are stored. The group's first write is its
// events insert (the alternate-id probe before it is a read, which the fence does not
// check), which reads the fence and is refused before the 6,000-reading insert
// is sent, so this does NOT exercise the fence on a split statement (that is
// TestCreateChunkedRefusesAPurgedTenant); it pins the grouped path's whole-tenant set-aside
// on the real server: one failed batch, each of the purged tenant's four alone, the live
// four together.
func TestAPurgedTenantsGroupedWriteStoresNothingOnPostgres(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	live := fmt.Sprintf("lv%d", time.Now().UnixNano())
	gone := fmt.Sprintf("gn%d", time.Now().UnixNano())
	plantOn(t, r.mgr, gone)
	base := time.Now().UTC().Truncate(time.Hour)
	var msgs []messaging.Message
	for i := 0; i < 8; i++ {
		tenant, ev := live, batchEvent(i, true, base)
		if i%2 == 1 {
			tenant = gone
			if i == 1 {
				ev = largeEvent(base.Add(time.Second), measurementsOf(6000, base.Add(time.Second)))
			}
		}
		msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, ev))
	}
	r.run(msgs)

	if got := r.count(t, gone, ""); got != 0 {
		t.Errorf("stored %d events for the purged tenant; want none", got)
	}
	var m int64
	if err := r.mgr.DB(core.WithTenant(context.Background(), gone)).Model(&model.MeasurementEvent{}).
		Count(&m).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if m != 0 {
		t.Errorf("stored %d readings for the purged tenant; want none", m)
	}
	if got := r.count(t, live, ""); got != 4 {
		t.Errorf("stored %d live events; want 4", got)
	}
	if got := r.api.txs.Load(); got != 6 {
		t.Errorf("took %d transactions; want 6", got)
	}
}

// BenchmarkPersistBatch measures events stored per second with `writers` workers draining
// one buffer, committing up to `batch` events per transaction. batch=1 is one transaction
// per event — the path every event took before batching — and is the before leg.
//
// b.N events are built and buffered before the timer starts, so what is timed is the
// writers alone. It reports events/s, transactions per event, and statements per event. The
// statements are counted on an untimed pass of two batches before the timed one, because
// counting renders every statement's SQL, which the timed pass must not pay for.
//
// The `plain` variants carry no alternate id and no anchors, which is the shape of the
// benchmark fleet's traffic: one events row and one measurement row per reading.
func BenchmarkPersistBatch(b *testing.B) {
	// zerolog logs every level by default, and the writer renders each event it is handed
	// as indented JSON at debug level: left on, that rendering is a large share of what the
	// benchmark would time. A service runs at info.
	level := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(level) })
	mgr, counter := newFenceBenchManager(b, "embatchbench", true)
	truncateEvents(b, mgr)
	base := time.Now().UTC().Truncate(time.Hour)
	var invocation int

	type shape struct {
		writers, batch int
		plain          bool
	}
	var shapes []shape
	for _, writers := range []int{1, 5, 10} {
		for _, batch := range []int{1, 8, 32, 64} {
			shapes = append(shapes, shape{writers, batch, false})
		}
	}
	shapes = append(shapes, shape{10, 1, true}, shape{10, 64, true})
	for _, sh := range shapes {
		writers, batch := sh.writers, sh.batch
		name := fmt.Sprintf("writers=%d/batch=%d", writers, batch)
		if sh.plain {
			name += "/plain"
		}
		b.Run(name, func(b *testing.B) {
			invocation++
			api := &txApi{Api: model.NewApi(mgr)}
			log := &ackLog{api: api}
			tenant := fenceCostTenant
			var failed sync.Map
			newWorker := func(ch <-chan messaging.Message) *EventPersistenceWorker {
				return &EventPersistenceWorker{Api: api, Unpersisted: ch, MaxBatch: batch,
					Invalid: func(err error, _ messaging.Message) { failed.Store("invalid", err) },
					Failed: func(_ string, _ uint, _ dmodel.ResolvedEvent, err error, _ string) {
						failed.Store("failed", err)
					}}
			}
			// Distinct content per op, so nothing is deduplicated away.
			op := 0
			next := func() messaging.Message {
				op++
				ev := batchEvent(op, true, base.Add(time.Duration(invocation)*time.Minute))
				alt := fmt.Sprintf("pb-%d-%d-%d", invocation, time.Now().UnixNano(), op)
				ev.AltId = &alt
				ev.OccurredTime = ev.OccurredTime.Add(time.Duration(op) * time.Microsecond)
				if sh.plain {
					ev.AltId, ev.Anchors = nil, nil
				}
				return consumed(b, log, op, tenant, 1, ev)
			}
			// Warm-up: the chunk exists and the driver's statement cache is primed.
			warm := make(chan messaging.Message, 1)
			warm <- next()
			close(warm)
			newWorker(warm).Process(context.Background())

			// The untimed counting pass: two batches through one writer.
			counted := make(chan messaging.Message, 2*batch)
			for i := 0; i < 2*batch; i++ {
				counted <- next()
			}
			close(counted)
			counter.Reset()
			counter.Arm(true)
			newWorker(counted).Process(context.Background())
			counter.Arm(false)
			statements, _ := counter.Counts()

			ch := make(chan messaging.Message, b.N)
			for i := 0; i < b.N; i++ {
				ch <- next()
			}
			close(ch)
			before := api.txs.Load()
			var wg sync.WaitGroup
			b.ResetTimer()
			start := time.Now()
			for w := 0; w < writers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					newWorker(ch).Process(context.Background())
				}()
			}
			wg.Wait()
			elapsed := time.Since(start)
			b.StopTimer()
			failed.Range(func(k, v any) bool {
				b.Fatalf("a benchmark event was %s: %v", k, v)
				return false
			})
			if got := len(log.snapshot()); got != b.N+1+2*batch {
				b.Fatalf("%d of %d events acknowledged", got, b.N+1+2*batch)
			}
			b.ReportMetric(float64(b.N)/elapsed.Seconds(), "events/s")
			b.ReportMetric(float64(api.txs.Load()-before)/float64(b.N), "txs/event")
			b.ReportMetric(float64(statements)/float64(2*batch), "statements/event")
		})
	}
}
