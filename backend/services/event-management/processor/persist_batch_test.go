// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	emconfig "github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// Batched persistence, by VALUE: how many transactions a set of messages takes, which
// messages were acknowledged and when, what each table holds, and what was reported. Most
// of these drive a worker SYNCHRONOUSLY over a channel that is filled and closed before
// Process runs, so what forms a batch is decided by MaxBatch alone and nothing races.

var batchT0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

// failure is one event the worker reported on the failed-events path.
type failure struct {
	tenant string
	reason uint
	device string
}

// batchRig is one worker over the fenced sqlite database, with everything it reports
// recorded.
type batchRig struct {
	ep      *EventPersistenceWorker
	api     *txApi
	db      *gorm.DB
	counter *rdbtest.StatementCounter
	acks    *ackLog
	reg     *prometheus.Registry

	mu       sync.Mutex
	failures []failure
	invalid  int
}

func newBatchRig(t *testing.T, maxBatch int) *batchRig {
	t.Helper()
	ep, db, counter := newFencedPersistenceWorker(t)
	api := newTxApi(ep)
	r := &batchRig{ep: ep, api: api, db: db, counter: counter, acks: &ackLog{api: api}, reg: prometheus.NewRegistry()}
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(r.reg)
	ep.Api = api
	ep.MaxBatch = maxBatch
	ep.metrics = NewPersistMetrics(ms)
	ep.Invalid = func(error, messaging.Message) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.invalid++
	}
	ep.Failed = func(tenant string, reason uint, ev dmodel.ResolvedEvent, _ error, _ string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.failures = append(r.failures, failure{tenant: tenant, reason: reason, device: ev.SourceDeviceToken})
	}
	return r
}

// run hands msgs to the worker and returns once it has persisted every one of them.
func (r *batchRig) run(msgs []messaging.Message) {
	ch := make(chan messaging.Message, len(msgs))
	for _, m := range msgs {
		ch <- m
	}
	close(ch)
	r.ep.Unpersisted = ch
	r.ep.Process(context.Background())
}

func (r *batchRig) reported() []failure {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]failure(nil), r.failures...)
}

// metric reads a counter's value, or a histogram's observation count, by full name and
// label (label "" for none).
func (r *batchRig) metric(t *testing.T, name, label, value string) float64 {
	t.Helper()
	families, err := r.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			match := label == ""
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					match = true
				}
			}
			if !match {
				continue
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

// histogramSum is the sum of a histogram's observations.
func (r *batchRig) histogramSum(t *testing.T, name string) float64 {
	t.Helper()
	families, err := r.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name && len(f.GetMetric()) == 1 {
			return f.GetMetric()[0].GetHistogram().GetSampleSum()
		}
	}
	return 0
}

const (
	metricFallbacks = "devicechain_eventmanagement_persist_batch_fallbacks_total"
	metricBatchSize = "devicechain_eventmanagement_persist_batch_size"
	metricMessages  = "devicechain_eventmanagement_persist_messages_total"
)

// messages builds n measurement messages for tenant, message i from device dev-i.
func (r *batchRig) messages(t *testing.T, n int, tenant string, withAltId bool) []messaging.Message {
	t.Helper()
	out := make([]messaging.Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, consumed(t, r.acks, i, tenant, 1, batchEvent(i, withAltId, batchT0)))
	}
	return out
}

// poisoned is message i with a measurement value no column can hold.
func poisoned(t *testing.T, log *ackLog, i int, tenant string) messaging.Message {
	t.Helper()
	ev := batchEvent(i, true, batchT0)
	ev.Payload.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value = "not-a-number"
	return consumed(t, log, i, tenant, 1, ev)
}

// eventsFor counts the events rows whose device is dev.
func eventsFor(t *testing.T, db *gorm.DB, dev string) (events, measurements int64) {
	t.Helper()
	if err := sysDB(db).Model(&model.Event{}).Where("device_token = ?", dev).Count(&events).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := sysDB(db).Model(&model.MeasurementEvent{}).Where("device_token = ?", dev).Count(&measurements).Error; err != nil {
		t.Fatalf("count measurements: %v", err)
	}
	return events, measurements
}

func tenantEvents(t *testing.T, db *gorm.DB, tenant string) int64 {
	t.Helper()
	var n int64
	if err := sysDB(db).Model(&model.Event{}).Where("tenant_id = ?", tenant).Count(&n).Error; err != nil {
		t.Fatalf("count tenant events: %v", err)
	}
	return n
}

// A batch is bounded by MaxBatch: 100 messages at 32 are four transactions (32+32+32+4),
// and every message is stored and acknowledged once.
func TestABatchHoldsAtMostMaxBatchMessages(t *testing.T) {
	r := newBatchRig(t, 32)
	r.run(r.messages(t, 100, fenceCostTenant, true))

	if got := r.api.txs.Load(); got != 4 {
		t.Errorf("100 messages at a batch of 32 took %d transactions; want 4", got)
	}
	if e, m, a := rowCounts(t, r.db); e != 100 || m != 300 || a != 100 {
		t.Errorf("stored %d/%d/%d rows; want 100/300/100", e, m, a)
	}
	acked := r.acks.acked()
	if len(acked) != 100 {
		t.Errorf("%d messages acknowledged; want 100", len(acked))
	}
	for i, n := range acked {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
	if got, sum := r.metric(t, metricBatchSize, "", ""), r.histogramSum(t, metricBatchSize); got != 4 || sum != 100 {
		t.Errorf("batch-size histogram holds %v observations summing to %v; want 4 summing to 100", got, sum)
	}
}

// Every acknowledgement is sent after the transaction holding the message committed.
func TestABatchIsAcknowledgedOnlyAfterItCommits(t *testing.T) {
	r := newBatchRig(t, 8)
	r.run(r.messages(t, 8, fenceCostTenant, true))

	if got := r.api.txs.Load(); got != 1 {
		t.Fatalf("8 messages at a batch of 8 took %d transactions; want 1", got)
	}
	acks := r.acks.snapshot()
	if len(acks) != 8 {
		t.Fatalf("%d acks; want 8", len(acks))
	}
	for _, a := range acks {
		if a.committed != 1 {
			t.Errorf("message %d was acknowledged when %d transactions had committed; want 1 (after the commit)",
				a.idx, a.committed)
		}
	}
}

// A message whose value cannot be stored is reported and acknowledged exactly as it was
// before batching; the seven around it are stored and acknowledged, committed together
// without it. The clean row is the negative control: with nothing refused there is one
// transaction and no fallback.
func TestARefusedMessageDoesNotFailItsBatchMates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		poison bool
	}{{"one message is refused", true}, {"control: nothing is refused", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			msgs := r.messages(t, 8, fenceCostTenant, true)
			if tc.poison {
				msgs[3] = poisoned(t, r.acks, 3, fenceCostTenant)
			}
			r.run(msgs)

			wantTxs, wantFallbacks, wantEvents := int64(1), 0.0, int64(8)
			if tc.poison {
				// The batch that failed, the refused message on its own, the other seven.
				wantTxs, wantFallbacks, wantEvents = 3, 1, 7
			}
			if got := r.api.txs.Load(); got != wantTxs {
				t.Errorf("took %d transactions; want %d", got, wantTxs)
			}
			if got := r.metric(t, metricFallbacks, "", ""); got != wantFallbacks {
				t.Errorf("fallbacks = %v; want %v", got, wantFallbacks)
			}
			if e, _, _ := rowCounts(t, r.db); e != wantEvents {
				t.Errorf("stored %d events; want %d", e, wantEvents)
			}
			if got := len(r.acks.acked()); got != 8 {
				t.Errorf("%d messages acknowledged; want all 8", got)
			}
			var want []failure
			if tc.poison {
				want = []failure{{tenant: fenceCostTenant, reason: uint(dmproto.FailureReason_Invalid), device: "dev-3"}}
			}
			if got := r.reported(); len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
				t.Errorf("reported %+v; want %+v", got, want)
			}
			if tc.poison {
				if e, m := eventsFor(t, r.db, "dev-3"); e != 0 || m != 0 {
					t.Errorf("the refused message left %d/%d event/measurement rows; want none", e, m)
				}
			}
		})
	}
}

// A message refused AFTER some of its own rows were written — its anchors, after its event
// and measurements — leaves none of them behind, and the messages before it in the batch
// are stored, not just acknowledged.
func TestAMessageRefusedPartWayLeavesNoRowsAndItsBatchMatesAreStored(t *testing.T) {
	r := newBatchRig(t, 8)
	r.api.failAnchorsFor = "dev-3"
	r.run(r.messages(t, 8, fenceCostTenant, true))

	if e, m := eventsFor(t, r.db, "dev-3"); e != 0 || m != 0 {
		t.Errorf("the refused message left %d/%d event/measurement rows; want none", e, m)
	}
	for _, dev := range []string{"dev-0", "dev-1", "dev-2", "dev-4", "dev-7"} {
		if e, m := eventsFor(t, r.db, dev); e != 1 || m != 3 {
			t.Errorf("%s has %d/%d event/measurement rows; want 1/3", dev, e, m)
		}
	}
	if got := r.reported(); len(got) != 1 || got[0].device != "dev-3" ||
		got[0].reason != uint(dmproto.FailureReason_Invalid) {
		t.Errorf("reported %+v; want dev-3 as invalid", got)
	}
	if got := len(r.acks.acked()); got != 8 {
		t.Errorf("%d messages acknowledged; want 8", got)
	}
}

// A steady stream of refused messages does not push every message back to a transaction of
// its own: each refusal costs its batch one failed transaction and one transaction for the
// refused message, and the rest still commit together. 64 messages, every eighth refused,
// in batches of 32: 2 x (4 failed + 4 alone + 1) = 18. Falling back to one transaction per
// message would take 2 x (1 + 32) = 66.
func TestAStreamOfRefusedMessagesStillCommitsInBatches(t *testing.T) {
	r := newBatchRig(t, 32)
	msgs := r.messages(t, 64, fenceCostTenant, true)
	for i := 7; i < 64; i += 8 {
		msgs[i] = poisoned(t, r.acks, i, fenceCostTenant)
	}
	r.run(msgs)

	if got := r.api.txs.Load(); got != 18 {
		t.Errorf("took %d transactions; want 18", got)
	}
	if e, _, _ := rowCounts(t, r.db); e != 56 {
		t.Errorf("stored %d events; want 56", e)
	}
	if got := len(r.reported()); got != 8 {
		t.Errorf("reported %d failures; want 8", got)
	}
	if got := len(r.acks.acked()); got != 64 {
		t.Errorf("%d messages acknowledged; want 64", got)
	}
}

// The erasure fence still refuses a purged tenant's messages inside a batch that carries
// other tenants' messages. The purged tenant's messages are left for redelivery exactly as
// they were before batching (not acknowledged, recorded retry, no rows) and their
// batch-mates are stored. The lifted-fence row is the negative control: the same batch
// commits whole, so the refusals above came from the fence.
func TestAFencedTenantInABatchIsRefusedAndItsBatchMatesAreStored(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fenced    bool
		delivered int
	}{
		{"fenced, first delivery", true, 1},
		{"fenced, last delivery", true, messaging.MaxDeliver},
		{"control: fence lifted", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			plantFence(t, r.db, "gone")
			if !tc.fenced {
				liftFence(t, r.db, "gone")
			}
			var msgs []messaging.Message
			for i := 0; i < 8; i++ {
				tenant := fenceCostTenant
				if i%2 == 1 {
					tenant = "gone"
				}
				delivered := 1
				if tenant == "gone" {
					delivered = tc.delivered
				}
				msgs = append(msgs, consumed(t, r.acks, i, tenant, delivered, batchEvent(i, true, batchT0)))
			}
			r.run(msgs)

			acked := r.acks.acked()
			if !tc.fenced {
				if got := r.api.txs.Load(); got != 1 || len(acked) != 8 || tenantEvents(t, r.db, "gone") != 4 {
					t.Errorf("with the fence lifted: %d transactions, %d acked, %d rows for the tenant; want 1, 8, 4",
						got, len(acked), tenantEvents(t, r.db, "gone"))
				}
				if got := r.metric(t, metricFallbacks, "", ""); got != 0 {
					t.Errorf("fallbacks = %v; want 0", got)
				}
				return
			}
			if got := tenantEvents(t, r.db, fenceCostTenant); got != 4 {
				t.Errorf("stored %d events for the live tenant; want 4", got)
			}
			if got := tenantEvents(t, r.db, "gone"); got != 0 {
				t.Errorf("stored %d events for the purged tenant; want none", got)
			}
			// 4 failed batches, each refused message on its own, and the live four together.
			if got := r.api.txs.Load(); got != 9 {
				t.Errorf("took %d transactions; want 9", got)
			}
			if got := r.metric(t, metricFallbacks, "", ""); got != 4 {
				t.Errorf("fallbacks = %v; want 4", got)
			}
			for i := 0; i < 8; i += 2 {
				if acked[i] != 1 {
					t.Errorf("live message %d acknowledged %d times; want 1", i, acked[i])
				}
			}
			if tc.delivered < messaging.MaxDeliver {
				for i := 1; i < 8; i += 2 {
					if acked[i] != 0 {
						t.Errorf("purged tenant's message %d was acknowledged; want it left for redelivery", i)
					}
				}
				if got := r.metric(t, metricMessages, "result", core.ResultRetry); got != 4 {
					t.Errorf("recorded %v retries; want 4", got)
				}
				if got := len(r.reported()); got != 0 {
					t.Errorf("reported %d failures on a first delivery; want none", got)
				}
				return
			}
			// On the last delivery each is given up on as a downstream failure and acked.
			got := r.reported()
			if len(got) != 4 {
				t.Fatalf("reported %+v; want the purged tenant's 4 messages", got)
			}
			for _, f := range got {
				if f.tenant != "gone" || f.reason != uint(dmproto.FailureReason_ApiCallFailed) {
					t.Errorf("reported %+v; want tenant gone with reason ApiCallFailed", f)
				}
			}
			for i := 1; i < 8; i += 2 {
				if acked[i] != 1 {
					t.Errorf("purged tenant's message %d acknowledged %d times on its last delivery; want 1", i, acked[i])
				}
			}
		})
	}
}

// A purged tenant still sending costs its batch-mates one transaction, not their batching:
// 31 live messages and 1 refused take 3 transactions, not 33.
func TestASinglePurgedTenantMessageCostsItsBatchTwoTransactions(t *testing.T) {
	r := newBatchRig(t, 32)
	plantFence(t, r.db, "gone")
	msgs := r.messages(t, 32, fenceCostTenant, true)
	msgs[10] = consumed(t, r.acks, 10, "gone", 1, batchEvent(10, true, batchT0))
	r.run(msgs)

	if got := r.api.txs.Load(); got != 3 {
		t.Errorf("took %d transactions; want 3", got)
	}
	if got := tenantEvents(t, r.db, fenceCostTenant); got != 31 {
		t.Errorf("stored %d live events; want 31", got)
	}
	if got := len(r.acks.acked()); got != 31 {
		t.Errorf("%d acknowledged; want the 31 live messages", got)
	}
}

// One transaction carries several tenants' messages, each written under its own tenant.
func TestABatchMixingTenantsWritesEachUnderItsOwnTenant(t *testing.T) {
	r := newBatchRig(t, 8)
	var msgs []messaging.Message
	for i := 0; i < 8; i++ {
		tenant := fenceCostTenant
		if i%2 == 1 {
			tenant = "globex"
		}
		msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, batchEvent(i, true, batchT0)))
	}
	r.run(msgs)

	if got := r.api.txs.Load(); got != 1 {
		t.Errorf("took %d transactions; want 1", got)
	}
	if got := r.metric(t, metricFallbacks, "", ""); got != 0 {
		t.Errorf("fallbacks = %v; want 0", got)
	}
	for i := 0; i < 8; i++ {
		want := fenceCostTenant
		if i%2 == 1 {
			want = "globex"
		}
		var ev model.Event
		if err := sysDB(r.db).Where("device_token = ?", batchEvent(i, true, batchT0).SourceDeviceToken).
			Take(&ev).Error; err != nil {
			t.Fatalf("read message %d's event: %v", i, err)
		}
		if ev.TenantId != want {
			t.Errorf("message %d was stored under tenant %q; want %q", i, ev.TenantId, want)
		}
	}
	if a, g := tenantEvents(t, r.db, fenceCostTenant), tenantEvents(t, r.db, "globex"); a != 4 || g != 4 {
		t.Errorf("tenant event counts %d/%d; want 4/4", a, g)
	}
}

// Idempotency. Each case runs with and without an alternate id: with none, the altId
// probe cannot short-circuit, so the content-derived event id and the ON CONFLICT
// arbiters are the only thing that keeps a replay from writing twice.
func TestReplayingMessagesWritesNothingTwice(t *testing.T) {
	for _, alt := range []bool{true, false} {
		name := "without an alternate id"
		if alt {
			name = "with an alternate id"
		}
		t.Run("the same message twice in one batch, "+name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			msgs := []messaging.Message{
				consumed(t, r.acks, 0, fenceCostTenant, 1, batchEvent(0, alt, batchT0)),
				consumed(t, r.acks, 1, fenceCostTenant, 1, batchEvent(0, alt, batchT0)),
			}
			r.run(msgs)
			if e, m, a := rowCounts(t, r.db); e != 1 || m != 3 || a != 1 {
				t.Errorf("stored %d/%d/%d rows; want 1/3/1", e, m, a)
			}
			if got, fb := r.api.txs.Load(), r.metric(t, metricFallbacks, "", ""); got != 1 || fb != 0 {
				t.Errorf("%d transactions, %v fallbacks; want 1, 0", got, fb)
			}
			if got := len(r.acks.snapshot()); got != 2 {
				t.Errorf("%d acks; want 2", got)
			}
		})
		t.Run("a batch persisted again, "+name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			r.run(r.messages(t, 8, fenceCostTenant, alt))
			r.counter.Reset()
			r.run(r.messages(t, 8, fenceCostTenant, alt))
			all, fence := r.counter.Counts()
			if e, m, a := rowCounts(t, r.db); e != 8 || m != 24 || a != 8 {
				t.Errorf("stored %d/%d/%d rows; want 8/24/8", e, m, a)
			}
			if got := len(r.acks.snapshot()); got != 16 {
				t.Errorf("%d acks; want 16", got)
			}
			if fb := r.metric(t, metricFallbacks, "", ""); fb != 0 {
				t.Errorf("fallbacks = %v; want 0", fb)
			}
			// What the replay actually executed — the proof the no-alternate-id row reached
			// the arbiters rather than stopping at the probe. With an alternate id: one
			// probe per message and nothing else. Without: the event, measurement and
			// anchor inserts of all eight, and one fence read for the transaction.
			wantAll, wantFence := int64(8), int64(0)
			if !alt {
				wantAll, wantFence = 25, 1
			}
			if all != wantAll || fence != wantFence {
				t.Errorf("the replay made %d statements with %d fence reads; want %d with %d",
					all, fence, wantAll, wantFence)
			}
		})
	}
}

// A COMMIT that fails rolls the whole batch back and every message is written again on its
// own; a COMMIT that succeeded but was reported as failed — a connection lost at the
// commit — is written again too, and adds nothing twice.
func TestAFailedCommitWritesEveryMessageAgainOnItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ambiguous bool
		alt       bool
	}{
		{"rolled back", false, true},
		{"committed but reported failed, with an alternate id", true, true},
		{"committed but reported failed, without an alternate id", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			if tc.ambiguous {
				r.api.ambiguousCommit.Store(1)
			} else {
				r.api.failCommit.Store(1)
			}
			r.run(r.messages(t, 8, fenceCostTenant, tc.alt))

			if got := r.api.txs.Load(); got != 9 {
				t.Errorf("took %d transactions; want 9 (the batch, then each message)", got)
			}
			if got := r.metric(t, metricFallbacks, "", ""); got != 1 {
				t.Errorf("fallbacks = %v; want 1", got)
			}
			if e, m, a := rowCounts(t, r.db); e != 8 || m != 24 || a != 8 {
				t.Errorf("stored %d/%d/%d rows; want 8/24/8", e, m, a)
			}
			acked := r.acks.acked()
			if len(acked) != 8 {
				t.Errorf("%d messages acknowledged; want 8", len(acked))
			}
			for i, n := range acked {
				if n != 1 {
					t.Errorf("message %d acknowledged %d times; want 1", i, n)
				}
			}
			if got := r.metric(t, metricMessages, "result", core.ResultOK); got != 8 {
				t.Errorf("recorded %v ok; want 8", got)
			}
		})
	}
}

// A batch size of 1 is one transaction per message — the path every message took before
// batching, and the negative control for the capacity test's transaction count.
func TestABatchSizeOfOneIsOneTransactionPerMessage(t *testing.T) {
	r := newBatchRig(t, 1)
	r.run(r.messages(t, 64, fenceCostTenant, true))
	if got := r.api.txs.Load(); got != 64 {
		t.Errorf("64 messages at a batch of 1 took %d transactions; want 64", got)
	}
	if got := len(r.acks.acked()); got != 64 {
		t.Errorf("%d acknowledged; want 64", got)
	}
}

// The per-message results and the batch signals: 8 clean messages, then a batch of 8 with
// one refused. ok counts every stored message, failed the refused one, and the histogram
// sees the two commits of 8 and 7.
func TestBatchPersistenceRecordsEveryMessagesResult(t *testing.T) {
	r := newBatchRig(t, 8)
	r.run(r.messages(t, 8, fenceCostTenant, true))
	var msgs []messaging.Message
	for i := 8; i < 16; i++ {
		msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0)))
	}
	msgs[3] = poisoned(t, r.acks, 11, fenceCostTenant)
	r.run(msgs)

	if got := r.metric(t, metricMessages, "result", core.ResultOK); got != 15 {
		t.Errorf("ok = %v; want 15", got)
	}
	if got := r.metric(t, metricMessages, "result", core.ResultFailed); got != 1 {
		t.Errorf("failed = %v; want 1", got)
	}
	if got := r.metric(t, metricFallbacks, "", ""); got != 1 {
		t.Errorf("fallbacks = %v; want 1", got)
	}
	if got, sum := r.metric(t, metricBatchSize, "", ""), r.histogramSum(t, metricBatchSize); got != 2 || sum != 15 {
		t.Errorf("batch sizes: %v commits summing to %v; want 2 summing to 15", got, sum)
	}
}

// With a linger a writer waits for more messages before committing a batch that is not
// full; without one it commits what is waiting. Two messages 20ms apart: one transaction
// with a linger, two without.
func TestLingerGathersMessagesThatArriveWithinIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		linger  time.Duration
		wantTxs int64
	}{{"with a linger", 5 * time.Second, 1}, {"control: no linger", 0, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			r.ep.Linger = tc.linger
			ch := make(chan messaging.Message, 2)
			ch <- consumed(t, r.acks, 0, fenceCostTenant, 1, batchEvent(0, true, batchT0))
			r.ep.Unpersisted = ch
			go func() {
				time.Sleep(20 * time.Millisecond)
				ch <- consumed(t, r.acks, 1, fenceCostTenant, 1, batchEvent(1, true, batchT0))
				time.Sleep(30 * time.Millisecond)
				close(ch)
			}()
			r.ep.Process(context.Background())
			if got := r.api.txs.Load(); got != tc.wantTxs {
				t.Errorf("took %d transactions; want %d", got, tc.wantTxs)
			}
			if got := len(r.acks.acked()); got != 2 {
				t.Errorf("%d acknowledged; want 2", got)
			}
		})
	}
}

// A linger ends: a lone message is committed once it runs out, without waiting for a
// full batch or for the channel to close.
func TestLingerIsBounded(t *testing.T) {
	r := newBatchRig(t, 8)
	r.ep.Linger = 100 * time.Millisecond
	ch := make(chan messaging.Message, 1)
	ch <- consumed(t, r.acks, 0, fenceCostTenant, 1, batchEvent(0, true, batchT0))
	r.ep.Unpersisted = ch
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ep.Process(context.Background())
	}()
	r.acks.waitFor(t, 1, time.Second)
	close(ch)
	<-done
}

// Shutdown persists the last, partial batch: ExecuteStop returns only once every message
// handed off has been stored and acknowledged.
func TestShutdownPersistsThePartialBatch(t *testing.T) {
	ep, db, _ := newFencedPersistenceWorker(t)
	singleConnection(t, db)
	api := newTxApi(ep)
	api.gate = make(chan struct{})
	acks := &ackLog{api: api}
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	proc := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), api, NewPersistMetrics(ms),
		WithPersistence(emconfig.PersistenceConfiguration{Writers: 1, MaxBatch: 8}))
	if err := proc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	for i := 0; i < 20; i++ {
		if !proc.handOff(context.Background(), consumed(t, acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0))) {
			t.Fatalf("hand-off %d refused", i)
		}
	}
	stopped := make(chan int)
	go func() {
		_ = proc.ExecuteStop(context.Background())
		stopped <- len(acks.snapshot())
	}()
	close(api.gate)
	select {
	case n := <-stopped:
		if n != 20 {
			t.Errorf("ExecuteStop returned with %d of 20 messages acknowledged", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteStop did not return")
	}
	if e, _, _ := rowCounts(t, db); e != 20 {
		t.Errorf("stored %d events; want 20", e)
	}
}

// The configured writer count and batch settings reach the writers Initialize starts.
func TestTheWritersRunTheConfiguredSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []ProcessorOption
		want emconfig.PersistenceConfiguration
	}{
		{"configured", []ProcessorOption{WithPersistence(emconfig.PersistenceConfiguration{Writers: 3, MaxBatch: 4, LingerMillis: 7})},
			emconfig.PersistenceConfiguration{Writers: 3, MaxBatch: 4, LingerMillis: 7}},
		{"defaults", nil, emconfig.PersistenceConfiguration{Writers: 5, MaxBatch: 32}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			proc := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), nil,
				NewPersistMetrics(ms), tc.opts...)
			if err := proc.Initialize(context.Background()); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			defer func() { _ = proc.ExecuteStop(context.Background()) }()
			writers := proc.Writers()
			if len(writers) != tc.want.Writers {
				t.Fatalf("%d writers started; want %d", len(writers), tc.want.Writers)
			}
			for i, w := range writers {
				if w.MaxBatch != tc.want.MaxBatch || w.Linger != tc.want.Linger() {
					t.Errorf("writer %d runs batch %d linger %s; want %d and %s",
						i, w.MaxBatch, w.Linger, tc.want.MaxBatch, tc.want.Linger())
				}
			}
		})
	}
}
