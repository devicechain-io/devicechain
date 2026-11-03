// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// FailedEventsIndexStore is the slice of the model the indexer writes through.
type FailedEventsIndexStore interface {
	GateStore
	InsertFailedEventRecord(ctx context.Context, rec *model.FailedEventRecord) (bool, error)
}

// FailedEventMetrics are the indexer's instruments. Built once in the initialize phase (a
// collector belongs to the process and the NATS callback that builds the indexer runs on every
// connection), like PersistMetrics.
type FailedEventMetrics struct {
	recorded   *prometheus.CounterVec
	suppressed *prometheus.CounterVec
	unindexed  *prometheus.CounterVec
}

// NewFailedEventMetrics registers the indexer's instruments.
//
// recorded counts EVERY record the indexer read - stored, folded into a count, or a redelivery -
// because it is the platform's rate of refusing events, which the flood bound deliberately
// hides from the table. The alert reads it, not the table.
func NewFailedEventMetrics(ms *core.Microservice) *FailedEventMetrics {
	return &FailedEventMetrics{
		recorded: ms.NewCounterVec("failed_events_recorded_total",
			"Records of events the platform refused or could not store that the failed-events index read, by writing area and reason. Every record counts, including those the flood bound folded into an existing row.",
			[]string{"source", "reason"}),
		suppressed: ms.NewCounterVec("failed_events_suppressed_total",
			"Failed-event records folded into an existing row's suppressed count instead of adding a row, by reason (the per-device or per-reason hourly bound was reached).",
			[]string{"reason"}),
		unindexed: ms.NewCounterVec("failed_events_unindexed_total",
			"Failed-event records the index did not store at all, by cause: no_tenant (the subject named none), undecodable (the record would not decode), erased (the tenant's data has been erased), gave_up (the store refused it on every delivery).",
			[]string{"cause"}),
	}
}

// Seed exports every series at zero so the first increase after a restart is an increase and
// not a series appearing at 1, which increase() would read as nothing.
func (m *FailedEventMetrics) Seed() {
	for _, c := range []string{"no_tenant", "undecodable", "erased", "gave_up"} {
		m.unindexed.WithLabelValues(c)
	}
}

// sourceLabel bounds the source label to the areas that write the stream.
func sourceLabel(area string) string {
	switch area {
	case "device-management", "event-management":
		return area
	default:
		return "other"
	}
}

// reasonLabel is the metric spelling of a reason; a number this build does not know is
// "unknown", never a label per number (a newer writer could mint many).
func reasonLabel(code int32) string {
	if name, ok := model.ReasonName(code); ok {
		return name
	}
	return "unknown"
}

// FlushEvery is how often the suppressed counts the gate has accumulated are written.
const FlushEvery = time.Second

// FailedEventsIndexer reads the failed-events stream into the failed_event_records index.
//
// One record at a time, on one goroutine: a record is one cheap insert (or none, when the flood
// bound folds it into a count), and a flood is exactly when a pool would only add ordering
// questions to the gate that has to be consistent with itself.
//
// It is at-least-once and idempotent: a row's identity is its stream position and broker time,
// so a redelivered record inserts nothing. A store outage HOLDS the record (core/storehold)
// rather than counting against its delivery budget; this reader is the platform's only copy of
// "why was that refused" once the cold tier rolls the record off, and an outage must not turn
// into a gap in it.
type FailedEventsIndexer struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Store        FailedEventsIndexStore
	Gate         *FailedEventGate
	Metrics      *FailedEventMetrics

	// Hold keeps a record unacked, outside its delivery budget, while the event store cannot
	// take writes. Nil holds nothing.
	Hold *storehold.Holder

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
	readPacer  *core.ReadPacer

	lifecycle core.LifecycleManager
}

// NewFailedEventsIndexer builds the indexer over its reader and store.
func NewFailedEventsIndexer(ms *core.Microservice, reader messaging.MessageReader, store FailedEventsIndexStore,
	metrics *FailedEventMetrics, callbacks core.LifecycleCallbacks) *FailedEventsIndexer {
	ix := &FailedEventsIndexer{
		Microservice: ms, Reader: reader, Store: store, Metrics: metrics,
		Gate: NewFailedEventGate(store),
	}
	ix.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-%s", ms.FunctionalArea, "failed-events-indexer"), ix, callbacks)
	return ix
}

func (ix *FailedEventsIndexer) pacer() *core.ReadPacer {
	if ix.readPacer == nil {
		ix.readPacer = core.NewReadPacer(ix.Microservice, "failed-events")
	}
	return ix.readPacer
}

func (ix *FailedEventsIndexer) Initialize(ctx context.Context) error {
	return ix.lifecycle.Initialize(ctx)
}

func (ix *FailedEventsIndexer) ExecuteInitialize(ctx context.Context) error {
	ix.procCtx, ix.procCancel = context.WithCancel(ctx)
	if ix.Metrics != nil {
		ix.Metrics.Seed()
	}
	return nil
}

func (ix *FailedEventsIndexer) Start(ctx context.Context) error { return ix.lifecycle.Start(ctx) }

func (ix *FailedEventsIndexer) ExecuteStart(context.Context) error {
	ix.wg.Add(2)
	go func() {
		defer ix.wg.Done()
		messaging.RunConsumer(ix.procCtx, ix.Reader, ix.pacer(), func(msg messaging.Message) bool {
			ix.handle(ix.procCtx, msg)
			return true
		})
	}()
	go func() {
		defer ix.wg.Done()
		t := time.NewTicker(FlushEvery)
		defer t.Stop()
		for {
			select {
			case <-ix.procCtx.Done():
				return
			case <-t.C:
				ix.Gate.Flush(ix.procCtx)
			}
		}
	}()
	return nil
}

func (ix *FailedEventsIndexer) Stop(ctx context.Context) error { return ix.lifecycle.Stop(ctx) }

// ExecuteStop ends the read loop and the flusher, then writes what the gate still holds: the
// database is open until the service's own stop, which follows this one.
func (ix *FailedEventsIndexer) ExecuteStop(ctx context.Context) error {
	if ix.procCancel != nil {
		ix.procCancel()
	}
	ix.wg.Wait()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ix.Gate.Flush(fctx)
	return nil
}

func (ix *FailedEventsIndexer) Terminate(ctx context.Context) error {
	return ix.lifecycle.Terminate(ctx)
}

func (ix *FailedEventsIndexer) ExecuteTerminate(context.Context) error { return nil }

// handle indexes one record and settles its message.
//
// A record no redelivery can fix (no tenant on its subject, an envelope that will not decode) is
// counted and acked: nothing is lost that was ever recoverable, because the stream still holds
// the bytes for the cold tier's window. A store failure takes the platform's disposition:
// settled for an erased tenant, held through an outage, left unacked for AckWait-paced
// redelivery otherwise until the last delivery, then counted as given up.
func (ix *FailedEventsIndexer) handle(ctx context.Context, msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		ix.unindexed("no_tenant")
		log.Warn().Str("subject", msg.Subject).Msg("Dropping a failed-events record with no parseable tenant")
		_ = msg.Ack()
		return
	}
	rec, err := ParseFailedRecord(msg.Value)
	if err != nil {
		ix.unindexed("undecodable")
		log.Warn().Err(err).Str("tenant", tenant).Uint64("streamSeq", msg.StreamSeq).
			Msg("Dropping a failed-events record that does not decode")
		_ = msg.Ack()
		return
	}
	ix.Metrics.recorded.WithLabelValues(sourceLabel(rec.SourceArea), reasonLabel(rec.Reason)).Inc()

	at := msg.AppendTime
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	seq := int64(msg.StreamSeq)

	var verdict Verdict
	holdCtx := ix.procCtx
	if holdCtx == nil {
		holdCtx = ctx
	}
	err = ix.Hold.RunCtx(holdCtx, tctx, []messaging.Message{msg}, func(c context.Context) error {
		var e error
		verdict, e = ix.index(c, tenant, rec, msg, seq, at)
		return e
	})
	if err != nil {
		switch messaging.Dispose(msg, err) {
		case messaging.TenantErased:
			ix.unindexed("erased")
			_ = msg.Ack()
		case messaging.Exhausted:
			ix.unindexed("gave_up")
			log.Error().Err(err).Str("tenant", tenant).Uint64("streamSeq", msg.StreamSeq).Int("delivered", msg.NumDelivered).
				Msg("Giving up on a failed-events record; it is not in the index, and stays on the stream until the cold tier rolls it off")
			_ = msg.Ack()
		default:
			// Unacked, not nak'd: AckWait paces the redelivery.
			log.Warn().Err(err).Str("tenant", tenant).Uint64("streamSeq", msg.StreamSeq).
				Msg("Unable to index a failed-events record; will retry on redelivery")
		}
		return
	}
	if verdict == VerdictSuppress {
		ix.Metrics.suppressed.WithLabelValues(reasonLabel(rec.Reason)).Inc()
	}
	_ = msg.Ack()
}

func (ix *FailedEventsIndexer) unindexed(cause string) {
	ix.Metrics.unindexed.WithLabelValues(cause).Inc()
}

// index applies the flood bound and stores the row. It is idempotent on re-execution: a
// decision that suppressed adds a pending count only after every step that can fail, and a
// decision that admitted writes a row whose identity makes a second write add nothing.
func (ix *FailedEventsIndexer) index(ctx context.Context, tenant string, rec *FailedRecord,
	msg messaging.Message, seq int64, at time.Time) (Verdict, error) {
	d, err := ix.Gate.Decide(ctx, tenant, rec.Reason, rec.DeviceToken, seq, at)
	if err != nil {
		return d.Verdict, err
	}
	switch d.Verdict {
	case VerdictSuppress, VerdictDuplicate:
		return d.Verdict, nil
	}
	row := &model.FailedEventRecord{
		OccurredTime: at,
		RecordSeq:    seq,
		SourceArea:   rec.SourceArea,
		Reason:       rec.Reason,
		DeviceToken:  rec.DeviceToken,
		EventType:    rec.EventType,
		Error:        rec.Error,
		Correlation:  model.SanitizeFailedEventText(msg.CorrelationID(), 128),

		PayloadRetained: rec.PayloadRetained,
	}
	if d.Verdict == VerdictAdmitOverflow {
		row.DeviceToken = model.OverflowDeviceToken
		row.EventType = nil
		row.PayloadRetained = false
		row.Error = "further records for this reason were not itemized; they are counted here"
	}
	if _, err := ix.Store.InsertFailedEventRecord(ctx, row); err != nil {
		return d.Verdict, err
	}
	ix.Gate.Admitted(d, seq, at)
	return d.Verdict, nil
}
