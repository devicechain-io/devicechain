// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	emconfig "github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// Serialized collection (messaging.Collector): a pod's writers take turns receiving a batch,
// so a trickle fills ONE batch instead of being split among every writer that is lingering.

// histogramSum reads the sum and observation count of the histogram whose name ends with
// suffix.
func histogramSum(t *testing.T, reg *prometheus.Registry, suffix string) (sum float64, count uint64) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if strings.HasSuffix(f.GetName(), suffix) {
			for _, m := range f.GetMetric() {
				sum += m.GetHistogram().GetSampleSum()
				count += m.GetHistogram().GetSampleCount()
			}
		}
	}
	return
}

// siblings returns n workers over the rig's database, sharing one channel and one collector,
// each with its own buffers (a copy of the rig's worker, taken before it ran).
func (r *batchRig) siblings(t *testing.T, n int, in <-chan messaging.Message) []*EventPersistenceWorker {
	t.Helper()
	singleConnection(t, r.db)
	collector := &messaging.Collector{}
	out := make([]*EventPersistenceWorker, 0, n)
	for i := 0; i < n; i++ {
		w := *r.ep
		w.WorkerId = i + 1
		w.Unpersisted = in
		w.collector = collector
		out = append(out, &w)
	}
	return out
}

func runAll(workers []*EventPersistenceWorker) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *EventPersistenceWorker) {
			defer wg.Done()
			w.Process(context.Background())
		}(w)
	}
	return &wg
}

func waitGroup(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// The processor's writers share ONE collector, and at a trickle that makes the batches big:
// ten writers, a 10 ms linger and a message a millisecond come out as batches of about ten,
// where ten writers lingering on their own would see about two.
func TestTheWritersShareOneCollector(t *testing.T) {
	r := newBatchRig(t, 32)
	singleConnection(t, r.db)
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(reg)
	proc := NewEventPersistenceProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(), r.api, NewPersistMetrics(ms),
		WithPersistence(emconfig.PersistenceConfiguration{Writers: 10, MaxBatch: 32, LingerMillis: millis(10)}))
	if err := proc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	for i, w := range proc.workers {
		if w.collector != &proc.collector {
			t.Fatalf("writer %d has collector %p; want the processor's one, %p", i, w.collector, &proc.collector)
		}
	}

	const total = 300
	start := time.Now()
	for i := 0; i < total; i++ {
		if d := time.Until(start.Add(time.Duration(i) * time.Millisecond)); d > 0 {
			time.Sleep(d)
		}
		if !proc.handOff(context.Background(), consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0))) {
			t.Fatalf("hand-off %d refused", i)
		}
	}
	if err := proc.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if n := len(r.acks.acked()); n != total {
		t.Fatalf("%d of %d acknowledged", n, total)
	}
	sum, count := histogramSum(t, reg, "persist_batch_size")
	if mean := sum / float64(count); mean < 8 {
		t.Errorf("mean persist_batch_size = %.1f over %d batches; want at least 8 at a trickle", mean, count)
	}
}

// A burst of undecodable messages must not stop the collection. Admission is where a bad
// message is acked and reported, and reporting BLOCKS when the failed-events channel is
// full: that stalls the one writer admitting it. Were admission done under the collector's
// token, the stalled writer would hold it and no writer could collect.
func TestAPoisonBurstDoesNotStopCollection(t *testing.T) {
	r := newBatchRig(t, 4)
	release := make(chan struct{})
	var inInvalid atomic.Int64
	r.ep.Invalid = func(error, messaging.Message) {
		inInvalid.Add(1)
		<-release // the failed-events channel is full: reporting blocks
	}
	in := make(chan messaging.Message, 32)
	workers := r.siblings(t, 3, in)
	wg := runAll(workers)

	const poison, good = 2, 6
	for i := 0; i < poison; i++ {
		// One at a time, so each is taken by a writer of its own: two together would be
		// one batch, admitted by one writer.
		in <- messaging.NewConsumedMessage("instance1."+fenceCostTenant+".resolved-events", []byte("not a resolved event"), 1, nil,
			recordingAck{log: r.acks, idx: 100 + i})
		want := int64(i + 1)
		waitUntil(t, "the poison message to reach the blocked report", func() bool { return inInvalid.Load() == want })
	}
	for i := 0; i < good; i++ {
		in <- consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0))
	}
	// Two of the three writers are blocked reporting; the third must still collect and commit.
	waitUntil(t, "the free writer to collect and commit while the others are blocked", func() bool {
		a := r.acks.acked()
		for i := 0; i < good; i++ {
			if a[i] != 1 {
				return false
			}
		}
		return true
	})
	close(release)
	close(in)
	waitGroup(t, wg, "the writers")
	a := r.acks.acked()
	for i := 0; i < poison; i++ {
		if a[100+i] != 1 {
			t.Errorf("poison message %d acknowledged %d times; want 1", i, a[100+i])
		}
	}
	if e, _, _ := rowCounts(t, r.db); e != good {
		t.Errorf("stored %d events; want %d", e, good)
	}
}

// A writer that is held — the store refused its batch and it is waiting, beating its
// messages — holds no token, so the others keep collecting. Three writers, a store that is
// down, and one message at a time: each is collected by a different writer, which could not
// happen were the token kept across the hold. Every held message is still told it is in
// progress, and once the store is back all of them are stored once, with the writers'
// buffers reused for the messages that follow.
func TestAHeldWriterDoesNotStopTheOthersCollecting(t *testing.T) {
	fastProbes(t)
	r := newBatchRig(t, 4)
	r.api.storeDown.Store(true)
	r.api.storeErr = &pgconn.PgError{Code: "53100", Message: "No space left on device"}
	in := make(chan messaging.Message, 32)
	workers := r.siblings(t, 3, in)
	wg := runAll(workers)

	var mu sync.Mutex
	beats := make([][]time.Time, 6)
	progressing := func(i int) messaging.Message {
		ev := batchEvent(i, true, batchT0)
		bytes, err := resolvedevent.MarshalResolvedEvent(&ev)
		if err != nil {
			t.Fatal(err)
		}
		return messaging.NewConsumedMessage("instance1."+fenceCostTenant+".resolved-events", bytes, 1, nil,
			progressAck{recordingAck: recordingAck{log: r.acks, idx: i}, mu: &mu, beats: &beats[i]})
	}
	for i := 0; i < 3; i++ {
		in <- progressing(i)
		i := i
		waitUntil(t, "another writer to collect and hold the next message", func() bool {
			return r.gauge(t, metricHeld) == float64(i+1)
		})
	}
	mu.Lock()
	for i := 0; i < 3; i++ {
		if len(beats[i]) == 0 {
			t.Errorf("held message %d was never told it is in progress", i)
		}
	}
	mu.Unlock()
	if n := len(r.acks.snapshot()); n != 0 {
		t.Fatalf("%d acknowledged while the store was down; want none", n)
	}

	r.api.storeDown.Store(false)
	waitUntil(t, "the held messages to be stored", func() bool { return len(r.acks.acked()) == 3 })
	for i := 3; i < 6; i++ {
		in <- progressing(i)
	}
	close(in)
	waitGroup(t, wg, "the writers")
	r.requireAllStoredOnce(t, 6)
}

// Once a batch is settled the writer holds no reference to it: every slot of its buffers is
// back to the zero value, not just the part the next batch will overwrite. Slots left full
// would keep the last batch's messages (up to MaxBatch of them, each with its decoded event)
// alive for as long as the writer idles.
func TestAWriterDropsItsBatchReferencesOnceSettled(t *testing.T) {
	r := newBatchRig(t, 8)
	var msgs []messaging.Message
	for i := 0; i < 8; i++ {
		msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0)))
	}
	r.ep.collector = &messaging.Collector{}
	r.run(msgs)
	raw, dones, batch := r.ep.raw[:cap(r.ep.raw)], r.ep.dones[:cap(r.ep.dones)], r.ep.batch[:cap(r.ep.batch)]
	if cap(raw) != 8 || cap(dones) != 8 || cap(batch) != 8 {
		t.Fatalf("buffers hold %d/%d/%d slots; want MaxBatch (8) each, allocated once", cap(raw), cap(dones), cap(batch))
	}
	for i := range raw {
		if raw[i].Value != nil || raw[i].Subject != "" {
			t.Errorf("raw[%d] still holds a message", i)
		}
		if dones[i] != nil {
			t.Errorf("dones[%d] still holds a clock-stop", i)
		}
		if batch[i].event != nil || batch[i].done != nil || batch[i].msg.Value != nil {
			t.Errorf("batch[%d] still holds an admitted message", i)
		}
	}
	if n := len(r.acks.acked()); n != 8 {
		t.Errorf("%d acknowledged; want 8", n)
	}
}

// persist_collect_full_total counts the batches the collector filled to the cap without
// waiting, and persist_handoff_depth records how many messages were still waiting when each
// batch was received. Ten messages already waiting and a cap of 4: two full batches, then a
// partial one of two; the depth reads 6, 2 and 0.
func TestCollectionMetricsReportFullBatchesAndTheHandOffDepth(t *testing.T) {
	r := newBatchRig(t, 4)
	var msgs []messaging.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, consumed(t, r.acks, i, fenceCostTenant, 1, batchEvent(i, true, batchT0)))
	}
	r.run(msgs)
	if got := r.metric(t, "devicechain_eventmanagement_persist_collect_full_total", "", ""); got != 2 {
		t.Errorf("persist_collect_full_total = %v; want 2 (the two batches filled to the cap)", got)
	}
	if got := r.metric(t, "devicechain_eventmanagement_persist_handoff_depth", "", ""); got != 3 {
		t.Errorf("persist_handoff_depth observed %v batches; want 3", got)
	}
	if sum := r.histogramSum(t, "devicechain_eventmanagement_persist_handoff_depth"); sum != 8 {
		t.Errorf("persist_handoff_depth observed a total depth of %v; want 6 + 2 + 0 = 8", sum)
	}
}

// A message is in flight from RECEIPT, not from admission: one that waits out the linger for
// the rest of its batch is already counted by persist_inflight (and its duration already
// running). The writer lingers 400 ms for a second message that never comes.
func TestAMessageIsInFlightFromReceiptNotFromAdmission(t *testing.T) {
	r := newBatchRig(t, 8)
	r.ep.Linger = 400 * time.Millisecond
	in := make(chan messaging.Message, 2)
	r.ep.Unpersisted = in
	done := make(chan struct{})
	go func() { defer close(done); r.ep.Process(context.Background()) }()
	handedAt := time.Now()
	in <- consumed(t, r.acks, 0, fenceCostTenant, 1, batchEvent(0, true, batchT0))
	waitUntil(t, "the message to read as in flight", func() bool {
		return r.gauge(t, "devicechain_eventmanagement_persist_inflight") == 1
	})
	if d := time.Since(handedAt); d > 300*time.Millisecond {
		t.Errorf("persist_inflight read 1 only after %v; it must read 1 during the 400 ms linger", d)
	}
	close(in)
	r.finishWithin(t, done)
}
