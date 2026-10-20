// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

// The DETECT output-path rig: a controlled source, the output path under test, and an
// independent oracle over what reached the broker and when.
//
// It exists so that a change to how detections leave the loop (batched rule-health writes,
// overlapping derived publishes) is measured against the path as it is now and checked
// against the same two invariants every time:
//
//   - ORDER. Within one series of one tenant (a lane), derived events reach the broker in the
//     order the engine produced them. A lane spans rules on purpose: two rules on one device
//     must not let a later edge of one overtake an earlier edge of the other.
//   - NOTHING IS MADE DURABLE AHEAD OF ITS OUTPUT. A checkpoint that covers an input is saved
//     only after every detection derived from that input has a POSITIVE broker outcome, and an
//     input is acknowledged only after a checkpoint covering it was saved.
//
// The oracle never asks the processor what it did. It knows what the source should produce
// (expectedDetections, computed from the readings alone, not by running an engine) and reads
// three things off the side of the path into one ordered log: each broker acknowledgement (the
// writer under the publisher records a write only once it returned success), each snapshot
// write (a gorm callback on the checkpoint store's own database) and each input acknowledgement
// (the message's acknowledger). Both planted defects in detect_output_oracle_test.go must make it
// fail; the unmodified path must pass.

import (
	"context"
	"encoding/json"
	"fmt"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"hash/fnv"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// outputBase is the event time of stream sequence 0. Sequence s occurs at outputBase + s ms, so
// event time rises with the stream and an input's sequence can be read back off a detection.
var outputBase = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

const (
	outputHigh = "90" // above every fixture rule's threshold
	outputLow  = "70" // below it
)

// outputFixture is a controlled source: readings whose threshold crossings are known in advance,
// so the detections they must produce are counted from the source, not from the engine.
//
// Input i (stream sequence i+1) is for tenant tenants[i%T] and device d<(i/T)%devices>, and is
// that device's sample n = i/(T*devices). A device reads high on samples where
// (n/firePeriod)%2 == 1, so it crosses the threshold every firePeriod samples: a raise on the way
// up and a resolve on the way down, never latched. firePeriod 1 alternates on every sample (one
// edge per input per rule), and 0 never crosses (the no-fire control). Every tenant carries
// rulesPerTenant identical threshold rules, so each crossing is that many detections in one lane.
type outputFixture struct {
	tenants        []string
	devices        int
	rulesPerTenant int
	firePeriod     int
}

// fireRate is the fraction of a device's samples that are an edge.
func (fx outputFixture) fireRate() float64 {
	if fx.firePeriod == 0 {
		return 0
	}
	return 1 / float64(fx.firePeriod)
}

func (fx outputFixture) ruleID(tenant string, k int) string {
	return tenant + "/p@1/hot" + strconv.Itoa(k)
}

func (fx outputFixture) device(i int) string {
	return "d" + strconv.Itoa((i/len(fx.tenants))%fx.devices)
}

func (fx outputFixture) sample(i int) int { return i / (len(fx.tenants) * fx.devices) }

func (fx outputFixture) high(n int) bool {
	return fx.firePeriod > 0 && n >= 0 && (n/fx.firePeriod)%2 == 1
}

// registry is every tenant's rules, all on profile version p@1.
func (fx outputFixture) registry(tb testing.TB) *runtime.RuleRegistry {
	tb.Helper()
	thr := 80.0
	var scoped []runtime.ScopedRule
	for _, tenant := range fx.tenants {
		for k := 0; k < fx.rulesPerTenant; k++ {
			cr, err := rules0.Compile(rules0.Rule{
				ID: fx.ruleID(tenant, k), Name: "hot", Type: rules0.TypeThreshold,
				When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
			}, rules0.Limits{})
			if err != nil {
				tb.Fatalf("compile fixture rule: %v", err)
			}
			scoped = append(scoped, runtime.ScopedRule{Tenant: tenant, ProfileVersionToken: "p@1", Compiled: cr})
		}
	}
	return runtime.NewRuleRegistry(scoped)
}

// outputSeqTime is the event time of stream sequence seq.
func outputSeqTime(seq uint64) time.Time {
	return outputBase.Add(time.Duration(seq) * time.Millisecond)
}

// messages builds inputs [0, n) as consumed messages acknowledged into log. Their bytes are
// encoded here, outside any timed region.
func (fx outputFixture) messages(tb testing.TB, n int, log *outputLog) []messaging.Message {
	tb.Helper()
	out := make([]messaging.Message, n)
	for i := 0; i < n; i++ {
		seq := uint64(i + 1)
		occurred := outputSeqTime(seq)
		value := outputLow
		if fx.high(fx.sample(i)) {
			value = outputHigh
		}
		ev := &resolvedevent.ResolvedEvent{
			Source: "http1", SourceDeviceToken: fx.device(i), ProfileVersionToken: "p@1",
			OccurredTime: occurred, ProcessedTime: occurred, EventType: eventtype.Measurement,
			Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
				OccurredTime: occurred,
				Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: value}},
			}}},
		}
		b, err := resolvedevent.MarshalResolvedEvent(ev)
		if err != nil {
			tb.Fatalf("marshal fixture event: %v", err)
		}
		m := messaging.NewConsumedMessage("dc."+fx.tenants[i%len(fx.tenants)]+".resolved-events", b, 0, nil,
			&oracleAck{seq: seq, log: log})
		m.StreamSeq = seq
		out[i] = m
	}
	return out
}

// detKey is a derived event's identity, as downstream consumers deduplicate it.
type detKey struct {
	tenant, rule, series, edge string
	at                         int64
}

// lane is the unit the ordering invariant is stated over: one series of one tenant, all rules.
func (k detKey) lane() string { return k.tenant + "|" + k.series }

// inputSeq is the stream sequence of the input that produced the detection.
func (k detKey) inputSeq() uint64 {
	return uint64(time.Unix(0, k.at).Sub(outputBase) / time.Millisecond)
}

func keyOf(de runtime.DerivedEvent) detKey {
	return detKey{tenant: de.Tenant, rule: de.RuleID, series: de.Series, edge: de.Edge, at: de.OccurredTime.UnixNano()}
}

// expectedDetections is what inputs [0, n) must produce, in input order, from the readings alone.
func (fx outputFixture) expectedDetections(n int) []detKey {
	var out []detKey
	for i := 0; i < n; i++ {
		s := fx.sample(i)
		was, is := fx.high(s-1), fx.high(s)
		if was == is {
			continue
		}
		edge := "resolved"
		if is {
			edge = "raised"
		}
		tenant := fx.tenants[i%len(fx.tenants)]
		for k := 0; k < fx.rulesPerTenant; k++ {
			out = append(out, detKey{tenant: tenant, rule: fx.ruleID(tenant, k), series: fx.device(i),
				edge: edge, at: outputSeqTime(uint64(i + 1)).UnixNano()})
		}
	}
	return out
}

// The oracle's log. Every entry is appended under one mutex, so its order is the order the
// events happened in, whichever goroutine they happened on.
type outputEventKind uint8

const (
	evPublished outputEventKind = iota // the broker positively acknowledged a derived event
	evSaved                            // a checkpoint wrote its snapshot at stream sequence seq
	evInputAck                         // inputs up to seq were acknowledged
	evStat                             // a rule-health fire was recorded (det carries no series)
)

type outputEvent struct {
	kind outputEventKind
	seq  uint64
	det  detKey
	// raw is a published event's payload, decoded into det only when the log is read, so the
	// oracle adds no decode to the broker wait the benchmarks time.
	raw []byte
}

type outputLog struct {
	mu     sync.Mutex
	events []outputEvent
	// times is when each event was logged, beside events rather than in them so that two logs
	// can still be compared for equality.
	times []time.Time
}

func (l *outputLog) add(e outputEvent) {
	now := time.Now()
	l.mu.Lock()
	l.events = append(l.events, e)
	l.times = append(l.times, now)
	l.mu.Unlock()
}

// outputLatencies is, for every positively acknowledged derived event, how long after its input
// was handled the acknowledgement came: the detection's whole output latency, INCLUDING its wait
// in the buffer for the checkpoint that publishes it. handled[seq] is when input seq was handled.
func (l *outputLog) outputLatencies(handled []time.Time) []time.Duration {
	events := l.snapshot()
	l.mu.Lock()
	times := append([]time.Time(nil), l.times...)
	l.mu.Unlock()
	var out []time.Duration
	for i, e := range events {
		if e.kind != evPublished {
			continue
		}
		if s := e.det.inputSeq(); s < uint64(len(handled)) && !handled[s].IsZero() {
			out = append(out, times[i].Sub(handled[s]))
		}
	}
	return out
}

// snapshot is the log so far, with every published payload decoded. A payload that does not
// decode is a detKey with no fields, which the judge counts as unexpected.
func (l *outputLog) snapshot() []outputEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]outputEvent(nil), l.events...)
	for i := range out {
		if out[i].raw != nil {
			var de runtime.DerivedEvent
			if json.Unmarshal(out[i].raw, &de) == nil {
				out[i].det = keyOf(de)
			}
			out[i].raw = nil
		}
	}
	return out
}

// published counts positive broker outcomes so far.
func (l *outputLog) published() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e.kind == evPublished {
			n++
		}
	}
	return n
}

// oracleAck is an input's acknowledger: a floor ack records that every input up to seq was acked.
type oracleAck struct {
	seq uint64
	log *outputLog
}

func (a *oracleAck) Ack() error { a.log.add(outputEvent{kind: evInputAck, seq: a.seq}); return nil }
func (a *oracleAck) AckFloor() error {
	a.log.add(outputEvent{kind: evInputAck, seq: a.seq})
	return nil
}
func (a *oracleAck) Current() bool { return true }

// oracleWriter sits directly on the broker, under everything else: it records a derived event
// only once the broker's write RETURNED SUCCESS, which is what a positive outcome is.
type oracleWriter struct {
	inner messaging.MessageWriter
	log   *outputLog
}

func (w *oracleWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if err := w.inner.WriteMessages(ctx, msgs...); err != nil {
		return err
	}
	for _, m := range msgs {
		w.log.add(outputEvent{kind: evPublished, raw: m.Value})
	}
	return nil
}

func (w *oracleWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *oracleWriter) HandleResponse(error) {}

// oracleStatStore records each rule-health fire the store accepted. RecordFire carries no
// series, so the logged key has none.
type oracleStatStore struct {
	inner fireStatStore
	log   *outputLog
}

func (s oracleStatStore) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) error {
	if err := s.inner.RecordFire(ctx, ruleID, tenant, at, edge); err != nil {
		return err
	}
	s.log.add(outputEvent{kind: evStat, det: detKey{tenant: tenant, rule: ruleID, edge: edge, at: at.UnixNano()}})
	return nil
}

// oracleAdmissions logs each fire the PRODUCTION accumulator is handed, at the moment it is
// handed it. That is what the fire oracle judges for variant B: admission is the decision to
// count a fire, so it must come after the publish's positive acknowledgement, exactly as the
// baseline's inline write must. Whether admitted fires reach the table is checked against the
// store separately.
type oracleAdmissions struct {
	inner runtime.FireRecorder
	log   *outputLog
}

func (r oracleAdmissions) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) {
	r.log.add(outputEvent{kind: evStat, det: detKey{tenant: tenant, rule: ruleID, edge: edge, at: at.UnixNano()}})
	r.inner.RecordFire(ctx, ruleID, tenant, at, edge)
}

// latencyWriter is a broker whose acknowledgement takes a fixed time, plus up to jitter more
// chosen deterministically from the message's dedup id, and always succeeds. Each write waits on
// its caller's own goroutine, so concurrent writes overlap as they would on a real broker.
type latencyWriter struct{ ack, jitter time.Duration }

func (w latencyWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	d := w.ack
	if w.jitter > 0 && len(msgs) > 0 {
		h := fnv.New32a()
		_, _ = h.Write([]byte(msgs[0].DedupID))
		d += time.Duration(h.Sum32()%1024) * w.jitter / 1024
	}
	if d > 0 {
		time.Sleep(d)
	}
	return nil
}

func (w latencyWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w latencyWriter) HandleResponse(error) {}

// latencyStatStore puts a fixed round trip in front of a real rule-stat store.
type latencyStatStore struct {
	inner fireStatStore
	rtt   time.Duration
}

func (s latencyStatStore) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) error {
	if s.rtt > 0 {
		time.Sleep(s.rtt)
	}
	return s.inner.RecordFire(ctx, ruleID, tenant, at, edge)
}

// RecordFireBatch puts the same round trip in front of one batch, for variant B.
func (s latencyStatStore) RecordFireBatch(ctx context.Context, tenant string, deltas []model.RuleFireDelta) (int, error) {
	if s.rtt > 0 {
		select {
		case <-time.After(s.rtt):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return s.inner.(rulehealth.Store).RecordFireBatch(ctx, tenant, deltas)
}

// watchSnapshotWrites records every snapshot write db makes into log. It is the statement, not the
// commit (gorm traces no COMMIT), so it fires slightly EARLY: an oracle that holds the write to
// "after every output it covers" is stricter than the invariant, never looser.
func watchSnapshotWrites(tb testing.TB, db *gorm.DB, log *outputLog) {
	tb.Helper()
	// A database reused across runs (a benchmark's cached PostgreSQL manager) keeps its one
	// pair of callbacks, and each run points them at its own log.
	if p, ok := snapshotWatches.Load(db); ok {
		p.(*atomic.Pointer[outputLog]).Store(log)
		return
	}
	target := &atomic.Pointer[outputLog]{}
	target.Store(log)
	snapshotWatches.Store(db, target)
	snapType := reflect.TypeOf(model.DetectSnapshot{})
	record := func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Schema == nil || tx.Statement.Schema.ModelType != snapType {
			return
		}
		log := target.Load()
		switch d := tx.Statement.Dest.(type) {
		case *model.DetectSnapshot:
			log.add(outputEvent{kind: evSaved, seq: uint64(d.StreamSeq)})
		case map[string]interface{}:
			if s, ok := d["stream_seq"].(int64); ok {
				log.add(outputEvent{kind: evSaved, seq: uint64(s)})
			}
		}
	}
	if err := db.Callback().Create().After("gorm:create").Register("detperf:snapshot-create", record); err != nil {
		tb.Fatalf("register snapshot watcher: %v", err)
	}
	if err := db.Callback().Update().After("gorm:update").Register("detperf:snapshot-update", record); err != nil {
		tb.Fatalf("register snapshot watcher: %v", err)
	}
}

// snapshotWatches is each watched database's current log.
var snapshotWatches sync.Map

// newSqliteSnapshotManager is an in-memory checkpoint store with tenant scoping, as newTestStore
// builds one, but on a shared-cache database so every pooled connection sees the same rows.
func newSqliteSnapshotManager(tb testing.TB) *rdb.RdbManager {
	tb.Helper()
	db := sqlitetest.OpenMemory(tb, &gorm.Config{Logger: logger.Discard})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		tb.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DetectSnapshot{}); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return &rdb.RdbManager{Database: db}
}

// newSqliteRuleStatStore is the rule-stat store over sqlite with the PRODUCTION callback chain in
// production order (scoping, token grammar, audit journal, erasure fence), so every fire pays the
// fence read it pays in production. timer, when set, times every statement.
func newSqliteRuleStatStore(tb testing.TB, timer *statementTimer) *model.RuleStatStore {
	tb.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	if timer != nil {
		cfg.Logger = timer
	}
	db := sqlitetest.OpenMemory(tb, cfg)
	for _, register := range []func(*gorm.DB) error{
		rdb.RegisterTenantScoping, rdb.RegisterTokenGrammar, rdb.RegisterAuditJournal, rdb.RegisterTenantFence,
	} {
		if err := register(db); err != nil {
			tb.Fatalf("register production callback: %v", err)
		}
	}
	if err := db.AutoMigrate(&rdb.PurgedTenant{}, &rdb.AuditEvent{}, &model.RuleStat{}); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return model.NewRuleStatStore(&rdb.RdbManager{Database: db})
}

// outputVariant is one way of getting the pending detections out of the loop. The benchmark-only
// seam the output-path comparison runs through: variant A is the path as it stands, and the
// planted defects in the oracle tests are variants too, which is what proves the oracle can tell.
type outputVariant struct {
	name string
	// wrapWriter decorates the broker writer, above the oracle's own (so the oracle still sees
	// only what the broker really acknowledged). Nil leaves it as it is.
	wrapWriter func(tb testing.TB, w messaging.MessageWriter) messaging.MessageWriter
	// checkpoint runs one checkpoint. Nil is variantA's.
	checkpoint func(ctx context.Context, rp *ResolvedEventsProcessor) bool
	// settle waits for anything the variant left in flight, before the oracle reads the log.
	settle func()
	// window is the production publish window the variant runs at (0: the default, 32).
	window int
}

// variantA is the baseline: per-detection publish, each waiting for its broker acknowledgement,
// each followed by the synchronous rule-health write, then save, then ack.
//
// 🔴 IT DOES NOT CALL THE PRODUCTION publishPending. It carries its own copy of that loop
// (baselinePublishPending), taken at 344fa002, the commit this rig was written against, so the baseline
// survives the change it is the baseline FOR: once publishPending batches or overlaps its
// publishes, "A" must still mean the serial path. TestTheBaselineAdapterIsTheProductionPath pins
// the copy to production for as long as the two are meant to agree; when a later slice changes
// production on purpose, that test is the one to retire, never this copy.
var variantA = outputVariant{name: "A", checkpoint: baselineCheckpoint}

// baselineCheckpoint publishes the buffer the baseline way, under the checkpoint's own publish
// deadline, then hands the rest (save, ack) to the production checkpoint, which then finds
// nothing left to publish.
func baselineCheckpoint(ctx context.Context, rp *ResolvedEventsProcessor) bool {
	pubCtx, cancel := context.WithTimeout(ctx, rp.checkpointTimeout())
	ok := baselinePublishPending(pubCtx, rp)
	cancel()
	if !ok {
		return false
	}
	return rp.checkpoint(ctx)
}

// baselinePublishPending is publishPending as it stood: one Publish per buffered detection, in
// order, each returning only after its broker acknowledgement and its rule-health write; the
// first retryable failure keeps the unpublished tail and defers the checkpoint.
func baselinePublishPending(ctx context.Context, rp *ResolvedEventsProcessor) bool {
	rp.dropSupersededDetections()
	i := 0
	for i < len(rp.pendingDets) {
		if err := rp.publisher.Publish(ctx, rp.pendingDets[i].Detection, rp.pendingDets[i].triggeredAt); err != nil {
			rp.metrics.recordCheckpointFailure(checkpointStagePublish)
			rp.pendingDets = rp.pendingDets[i:]
			return false
		}
		i++
	}
	rp.pendingDets = rp.pendingDets[:0]
	return true
}

// serialOutput pins the publish window to 1, the serial path, for the tests that assert one
// global publish order across devices (the default window overlaps devices' publishes, so only
// each device's own order is fixed).
var serialOutput = runtime.OutputConfig{MaxInFlight: 1, MaxBufferedBytes: runtime.DefaultMaxBufferedBytes}

// variantProduction runs the production checkpoint unmodified at window 1: that IS variant A, and
// the parity test holds the two together. The window is pinned because the default is wider.
var variantProduction = outputVariant{name: "production", window: 1, checkpoint: func(ctx context.Context, rp *ResolvedEventsProcessor) bool {
	return rp.checkpoint(ctx)
}}

// variantB is the batched rule-health path with SERIAL publishing: the baseline's publish loop
// with the production accumulator as the recorder. The rig wires the accumulator when the spec
// carries a batch store. It was the production checkpoint until publishing was pipelined; it is
// kept as a benchmark-only adapter so "B" still means what it measured (stats batched, publishes
// serial) rather than whatever production now does.
var variantB = outputVariant{name: "B", checkpoint: baselineCheckpoint}

// variantPipelined is the production checkpoint at a given publish window: variant C at window 1
// (the new publisher, still one publish at a time) and variant D above it. The rig sets the
// window from the spec; the variant itself is the production checkpoint.
func variantPipelined(window int) outputVariant {
	name := "C"
	if window > 1 {
		name = fmt.Sprintf("D%d", window)
	}
	return outputVariant{name: name, window: window, checkpoint: func(ctx context.Context, rp *ResolvedEventsProcessor) bool {
		return rp.checkpoint(ctx)
	}}
}

// outputRigSpec says what one rig is built over.
type outputRigSpec struct {
	fx outputFixture
	// broker is the derived-event writer. Nil is a broker that acknowledges at once.
	broker messaging.MessageWriter
	// stats is the rule-health store written INLINE, the baseline's recorder. Nil wires none.
	stats fireStatStore
	// batch is the rule-health store written through the production accumulator
	// (internal/rulehealth), with batchCfg (zero: the defaults). It replaces stats when set.
	batch    rulehealth.Store
	batchCfg rulehealth.Config
	// statLossExpected lets run's final flush drop fires (a store that is meant to fail).
	statLossExpected bool
	// snapshots is the checkpoint store's database. Nil is a fresh in-memory sqlite.
	snapshots *rdb.RdbManager
	variant   outputVariant
	// checkpointEvents is the batch-count checkpoint trigger; 0 is the service default.
	checkpointEvents int
	// output is the production publish window and byte budget. Zero: the variant's window with
	// the default budget, or the service default when the variant has none.
	output runtime.OutputConfig
}

// outputRig is a processor built over a spec, the oracle's log, and the split timings.
type outputRig struct {
	fx      outputFixture
	rp      *ResolvedEventsProcessor
	metrics *DetectMetrics
	log     *outputLog
	variant outputVariant
	// handleTime is the wall time spent in handle: decode, fan-out and the engine.
	handleTime time.Duration
	// handled is when each input was handled, by stream sequence, for the output latencies.
	handled []time.Time
	// checkpointTime is the wall time spent in checkpoints, publish included.
	checkpointTime time.Duration
	checkpoints    int
	// acc is the production rule-health accumulator when the spec asked for one. run stops it
	// with its final flush, so the store holds every admitted fire when run returns.
	acc *rulehealth.Accumulator
	// settleTime is the wall time of that final flush (inside run, so inside a benchmark's timer).
	settleTime       time.Duration
	statLossExpected bool
}

func newOutputRig(tb testing.TB, spec outputRigSpec) *outputRig {
	tb.Helper()
	if spec.output == (runtime.OutputConfig{}) && spec.variant.window > 0 {
		spec.output = runtime.OutputConfig{MaxInFlight: spec.variant.window, MaxBufferedBytes: runtime.DefaultMaxBufferedBytes}
	}
	ms := &core.Microservice{InstanceId: "detperf", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	metrics := NewDetectMetrics(ms)
	log := &outputLog{}
	reg := spec.fx.registry(tb)

	broker := spec.broker
	if broker == nil {
		broker = latencyWriter{}
	}
	var w messaging.MessageWriter = &oracleWriter{inner: broker, log: log}
	if spec.variant.wrapWriter != nil {
		w = spec.variant.wrapWriter(tb, w)
	}
	pub := runtime.NewPublisher(w, reg, metrics)
	var acc *rulehealth.Accumulator
	switch {
	case spec.batch != nil:
		cfg := spec.batchCfg
		if cfg == (rulehealth.Config{}) {
			cfg = rulehealth.DefaultConfig()
		}
		var err error
		if acc, err = rulehealth.New(spec.batch, cfg, metrics); err != nil {
			tb.Fatalf("rule-health accumulator: %v", err)
		}
		acc.Start()
		tb.Cleanup(func() { acc.Stop(context.Background(), false) })
		pub.SetFireRecorder(oracleAdmissions{inner: acc, log: log})
	case spec.stats != nil:
		pub.SetFireRecorder(ruleStatRecorder{store: oracleStatStore{inner: spec.stats, log: log}, metrics: metrics})
	}

	snaps := spec.snapshots
	if snaps == nil {
		snaps = newSqliteSnapshotManager(tb)
	}
	watchSnapshotWrites(tb, snaps.Database, log)

	every := spec.checkpointEvents
	if every == 0 {
		every = 1000 // config.DefaultCheckpointEvents
	}
	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)
	rp := &ResolvedEventsProcessor{
		Store: model.NewSnapshotStore(snaps),
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   every,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
			Output:             spec.output,
		},
		registry:   reg,
		publisher:  pub,
		clock:      detectcore.RealClock{},
		metrics:    metrics,
		procCtx:    ctx,
		procCancel: cancel,
	}
	if err := rp.restore(context.Background()); err != nil {
		tb.Fatalf("restore: %v", err)
	}
	if spec.variant.checkpoint == nil {
		spec.variant.checkpoint = baselineCheckpoint
	}
	return &outputRig{fx: spec.fx, rp: rp, metrics: metrics, log: log, variant: spec.variant, acc: acc,
		statLossExpected: spec.statLossExpected}
}

// run feeds msgs through the loop's own sequence, handle then a checkpoint whenever the
// batch-count trigger is reached, then the final checkpoint, and waits for the variant to settle.
// A checkpoint that does not commit fails the run: the fixtures here have no fault in them.
func (r *outputRig) run(tb testing.TB, msgs []messaging.Message) {
	tb.Helper()
	ctx := context.Background()
	if cap(r.handled) < len(msgs)+2 {
		r.handled = make([]time.Time, 0, len(msgs)+2)
	}
	for _, m := range msgs {
		start := time.Now()
		r.rp.handle(m)
		end := time.Now()
		r.handleTime += end.Sub(start)
		if m.StreamSeq < 1<<24 {
			for uint64(len(r.handled)) <= m.StreamSeq {
				r.handled = append(r.handled, time.Time{})
			}
			r.handled[m.StreamSeq] = end
		}
		if len(r.rp.pendingAcks) >= r.rp.cfg.CheckpointEvents {
			r.checkpoint(tb, ctx)
		}
	}
	if len(r.rp.pendingAcks) > 0 || len(r.rp.pendingDets) > 0 || r.rp.dirty {
		r.checkpoint(tb, ctx)
	}
	if r.acc != nil {
		start := time.Now()
		if dropped := r.acc.Stop(context.Background(), true); dropped != 0 && !r.statLossExpected {
			tb.Fatalf("the rule-health accumulator's final flush dropped %d fires on a healthy store", dropped)
		}
		r.settleTime = time.Since(start)
	}
	if r.variant.settle != nil {
		r.variant.settle()
	}
}

func (r *outputRig) checkpoint(tb testing.TB, ctx context.Context) {
	tb.Helper()
	start := time.Now()
	ok := r.variant.checkpoint(ctx, r.rp)
	r.checkpointTime += time.Since(start)
	r.checkpoints++
	if !ok {
		tb.Fatalf("variant %s: a checkpoint did not commit", r.variant.name)
	}
}

// outputVerdict is what the oracle found. Each list holds human-readable violations.
type outputVerdict struct {
	// ordering: a lane's detections reached the broker out of the order the engine produced them.
	ordering []string
	// earlyDurable: a snapshot was written before every detection it covers had a positive
	// outcome, or an input was acknowledged before a snapshot covering it was written.
	earlyDurable []string
	// statAhead: a rule-health fire was recorded for a detection the broker had not yet
	// positively acknowledged. Only an acknowledged publish may count as a fire.
	statAhead []string
	stats     int
	// missing / unexpected / duplicates compare what the broker acknowledged with the source.
	missing, unexpected, duplicates int
	published                       int
}

func (v outputVerdict) clean() bool {
	return len(v.ordering) == 0 && len(v.earlyDurable) == 0 && len(v.statAhead) == 0 && v.missing == 0 && v.unexpected == 0
}

func (v outputVerdict) String() string {
	return fmt.Sprintf("published=%d stats=%d missing=%d unexpected=%d duplicates=%d ordering=%d %v earlyDurable=%d %v statAhead=%d %v",
		v.published, v.stats, v.missing, v.unexpected, v.duplicates, len(v.ordering), first(v.ordering),
		len(v.earlyDurable), first(v.earlyDurable), len(v.statAhead), first(v.statAhead))
}

func first(s []string) []string {
	if len(s) > 3 {
		return s[:3]
	}
	return s
}

// judgeOutput checks the log against what inputs [0, n) must produce.
func judgeOutput(fx outputFixture, n int, events []outputEvent) outputVerdict {
	var v outputVerdict
	want := fx.expectedDetections(n)
	expected := make(map[detKey]int, len(want)) // key -> its position in its rule's sequence
	perRule := map[string][]detKey{}
	for _, k := range want {
		r := k.tenant + "|" + k.rule + "|" + k.series
		expected[k] = len(perRule[r])
		perRule[r] = append(perRule[r], k)
	}

	seen := make(map[detKey]bool, len(want))
	ruleNext := map[string]int{}    // next expected position per (tenant, rule, series)
	laneLast := map[string]uint64{} // highest input sequence published per lane
	var savedThrough uint64         // highest snapshot sequence written so far
	covered := 0                    // want[:covered] has been judged against a snapshot
	acked := map[detKey]int{}       // positive outcomes so far, keyed without the series
	for _, e := range events {
		switch e.kind {
		case evStat:
			v.stats++
			if acked[e.det]--; acked[e.det] < 0 {
				v.statAhead = append(v.statAhead, fmt.Sprintf("rule %s fire at input %d recorded before its publish was acknowledged",
					e.det.rule, e.det.inputSeq()))
			}
		case evPublished:
			v.published++
			noSeries := e.det
			noSeries.series = ""
			acked[noSeries]++
			if seen[e.det] {
				v.duplicates++
				continue
			}
			seen[e.det] = true
			pos, ok := expected[e.det]
			if !ok {
				v.unexpected++
				continue
			}
			r := e.det.tenant + "|" + e.det.rule + "|" + e.det.series
			if pos != ruleNext[r] {
				v.ordering = append(v.ordering, fmt.Sprintf("rule %s series %s: %s at input %d published as #%d of its sequence, want #%d",
					e.det.rule, e.det.series, e.det.edge, e.det.inputSeq(), ruleNext[r], pos))
			}
			if pos >= ruleNext[r] {
				ruleNext[r] = pos + 1
			}
			if s := e.det.inputSeq(); s < laneLast[e.det.lane()] {
				v.ordering = append(v.ordering, fmt.Sprintf("lane %s: input %d's %s published after input %d's",
					e.det.lane(), s, e.det.edge, laneLast[e.det.lane()]))
			} else {
				laneLast[e.det.lane()] = s
			}
		case evSaved:
			// Each detection is judged at the FIRST snapshot that covers its input; want is in
			// input order, so one cursor walks it once across the whole log.
			for ; covered < len(want) && want[covered].inputSeq() <= e.seq; covered++ {
				if k := want[covered]; !seen[k] {
					v.earlyDurable = append(v.earlyDurable, fmt.Sprintf("snapshot at %d written before input %d's %s (%s) was acknowledged by the broker",
						e.seq, k.inputSeq(), k.edge, k.series))
				}
			}
			if e.seq > savedThrough {
				savedThrough = e.seq
			}
		case evInputAck:
			if e.seq > savedThrough {
				v.earlyDurable = append(v.earlyDurable, fmt.Sprintf("inputs through %d acknowledged with the snapshot only at %d", e.seq, savedThrough))
			}
		}
	}
	for _, k := range want {
		if !seen[k] {
			v.missing++
		}
	}
	return v
}

// statementTimer is a gorm logger that times every statement a database runs, split into the
// erasure-fence reads and everything else. The fence read is issued from inside the upsert's own
// create callbacks, and gorm's clock for a statement starts before its callbacks run, so the
// upsert's time CONTAINS the fence read's: the upsert alone is other minus fence. gorm hands Trace the statement's start, so the time
// is the statement's own; rendering its SQL to classify it happens after the clock is read, but
// it does run on the caller's goroutine, so a benchmark that wants the headline cost without it
// runs with no timer at all.
type statementTimer struct {
	fenceNs, otherNs atomic.Int64
	fenceN, otherN   atomic.Int64
}

func (s *statementTimer) LogMode(logger.LogLevel) logger.Interface { return s }
func (s *statementTimer) Info(context.Context, string, ...any)     {}
func (s *statementTimer) Warn(context.Context, string, ...any)     {}
func (s *statementTimer) Error(context.Context, string, ...any)    {}

func (s *statementTimer) Trace(_ context.Context, begin time.Time, fc func() (string, int64), _ error) {
	elapsed := time.Since(begin).Nanoseconds()
	if sql, _ := fc(); strings.Contains(sql, rdb.FenceTable) {
		s.fenceNs.Add(elapsed)
		s.fenceN.Add(1)
		return
	}
	s.otherNs.Add(elapsed)
	s.otherN.Add(1)
}
