// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	emconfig "github.com/devicechain-io/dc-event-management/config"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	// MESSAGE_BACKLOG_SIZE is how many read messages can wait for a writer. It is not
	// scaled with the batch size: a writer drains what is waiting and the reader refills
	// the buffer while the writer commits, so a deeper buffer would hold more messages in
	// memory without making a batch any fuller.
	MESSAGE_BACKLOG_SIZE      = 100
	FAILED_EVENT_BACKLOG_SIZE = 100 // Number of failed events that can be waiting to publish
)

// failedItem pairs a failed event with its tenant so the outbound producer can
// publish to the tenant's subject (the tenant is derived from the inbound
// subject and must travel with the event across the channel). The inbound
// message's correlation id (E15) also travels with it so the outbound failed
// event can be stamped with the same id for end-to-end tracing.
type failedItem struct {
	tenant        string
	event         dmodel.FailedEvent
	correlationId string
}

type EventPersistenceProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	FailedEventsWriter   messaging.MessageWriter
	Api                  emmodel.EventManagementApi

	messages chan messaging.Message
	failed   chan failedItem
	workers  []*EventPersistenceWorker

	// persistence sizes the writers: how many run and how many events each commits in one
	// transaction. Defaulted in the constructor by the configuration's own ApplyDefaults.
	persistence emconfig.PersistenceConfiguration

	// metrics records RED-style instrumentation (rate/errors/duration) for the
	// persist loop (ADR-022 E13) and the batch signals; shared by reference across all
	// workers.
	metrics *PersistMetrics

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain senders before closing the channels they feed, so a
	// worker or the reader can never send on a closed channel at SIGTERM.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup
	outboundWG sync.WaitGroup

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. Built on first use by pacer(), because this struct is also
	// assembled by literal in tests that never run the constructor.
	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
}

// pacer returns the read loop's error pacer, building it on first use. It is touched only
// by the single read goroutine, which is the pacer's own contract.
func (eproc *EventPersistenceProcessor) pacer() *core.ReadPacer {
	if eproc.readPacer == nil {
		eproc.readPacer = core.NewReadPacer(eproc.Microservice, "resolved events")
	}
	return eproc.readPacer
}

// PersistMetrics is the persist loop's instrumentation: the per-message RED metrics every
// processing loop exports, plus what only a batching writer has to say.
type PersistMetrics struct {
	// messages is persist_messages_total, _duration_seconds and _inflight. A message is
	// in flight from when a writer takes it until its disposition, which includes the
	// time it waits for its batch to commit.
	messages *core.ProcessorMetrics
	// batchSize is persist_batch_size: events committed per transaction.
	batchSize prometheus.Observer
	// fallbacks is persist_batch_fallbacks_total: batch transactions that did not commit.
	fallbacks prometheus.Counter
}

// start marks one message in flight. Nil-safe, like everything on this type, because
// tests build workers by literal without metrics.
func (m *PersistMetrics) start() func(result string) {
	if m == nil {
		return func(string) {}
	}
	return m.messages.Start()
}

// committed records one committed transaction holding n events.
func (m *PersistMetrics) committed(n int) {
	if m != nil {
		m.batchSize.Observe(float64(n))
	}
}

// fallback records one batch transaction that did not commit.
func (m *PersistMetrics) fallback() {
	if m != nil {
		m.fallbacks.Inc()
	}
}

// NewPersistMetrics builds this processor's RED instrumentation.
//
// 🔴 IT IS SEPARATE FROM THE CONSTRUCTOR BECAUSE THE TWO RUN IN DIFFERENT PHASES.
// The processor is built inside the NATS manager's oncreate callback, which runs on
// EVERY start — it has to, because the processor holds a reader bound to the
// connection — while a Prometheus collector belongs to the PROCESS and may be
// registered only once, or the second registration panics. So the caller builds this in
// the INITIALIZE phase and hands the same instruments to every processor that callback
// builds.
func NewPersistMetrics(ms *core.Microservice) *PersistMetrics {
	return &PersistMetrics{
		messages: ms.NewProcessorMetrics("persist"),
		batchSize: ms.NewHistogramVec("persist_batch_size", "Events committed per persistence transaction.",
			nil, []float64{1, 2, 4, 8, 16, 32, 64}).WithLabelValues(),
		fallbacks: ms.NewCounter("persist_batch_fallbacks_total",
			"Batch transactions that did not commit, after which their events were written again."),
	}
}

// ProcessorOption configures an EventPersistenceProcessor.
type ProcessorOption func(*EventPersistenceProcessor)

// WithPersistence sizes the writers from the service's configuration. Without it the
// processor runs the configuration's defaults.
func WithPersistence(cfg emconfig.PersistenceConfiguration) ProcessorOption {
	return func(eproc *EventPersistenceProcessor) {
		eproc.persistence = cfg
	}
}

// Create a new inbound events processor.
//
// metrics is built once in the initialize phase (see NewPersistMetrics) and shared by
// every processor this service constructs, because that callback is connection-scoped
// and the instruments are not.
func NewEventPersistenceProcessor(ms *core.Microservice, resolved messaging.MessageReader,
	failed messaging.MessageWriter, callbacks core.LifecycleCallbacks, api emmodel.EventManagementApi,
	metrics *PersistMetrics, opts ...ProcessorOption) *EventPersistenceProcessor {
	eproc := &EventPersistenceProcessor{
		Microservice:         ms,
		ResolvedEventsReader: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
		metrics:              metrics,
	}
	for _, opt := range opts {
		opt(eproc)
	}
	// The same defaulting the configuration load runs, so a processor built without the
	// option, or with a configuration assembled in code, cannot disagree with it.
	eproc.persistence.ApplyDefaults()

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "event-persist-proc")
	eproc.lifecycle = core.NewLifecycleManager(ipname, eproc, callbacks)
	return eproc
}

// marshalFailedEvent encodes a failed event for publication. It is a variable rather
// than a direct call to proto.MarshalFailedEvent so its FAILURE can be exercised.
//
// 🔴 THE BRANCH IT FEEDS IS OTHERWISE UNREACHABLE, WHICH IS HOW A BUG LIVED IN IT.
// PFailedEvent carries only scalars, so proto.Marshal has nothing to reject and the
// error return is dead in practice — and an error path no test can enter is an error
// path nobody reads carefully. This one fell through without a return and published a
// message with a nil Value; nothing could have caught that, because nothing could make
// the marshal fail. The seam is one line and it turns the branch's behaviour into a
// property the suite asserts rather than one a reviewer has to take on trust.
var marshalFailedEvent = proto.MarshalFailedEvent

// Handle case where event failed to process.
func (eproc *EventPersistenceProcessor) ProcessFailedEvent(ctx context.Context) bool {
	item, more := <-eproc.failed
	if more {
		log.Debug().Str("message", item.event.Message).Str("error", item.event.Error).Msg("received failed event")

		// Marshal event message to protobuf.
		//
		// 🔴 A FAILED MARSHAL STOPS HERE RATHER THAN FALLING THROUGH. Without the
		// return, `bytes` is nil and the publish below still happens — an empty record
		// on the failed-events subject, carrying the reason in its key and nothing a
		// reader could decode. That is worse than publishing nothing, because the only
		// signal a consumer gets is a record it cannot parse, while the line saying why
		// is on a pod's stdout. OnFailedEvent, the sibling callback that marshals the
		// resolved event, already publishes nothing in this case; this is the same
		// decision written the same way, so the two cannot disagree.
		bytes, err := marshalFailedEvent(&item.event)
		if err != nil {
			log.Error().Err(err).Msg("unable to marshal event to protobuf")
			return false
		}

		// Create and deliver message on the failed event's tenant subject. Stamp
		// it with the inbound message's correlation id (E15) so the emitted
		// failed event can be traced back to the resolved event that triggered it.
		msg := messaging.Message{
			Key:   []byte(strconv.FormatInt(int64(item.event.Reason), 10)),
			Value: bytes,
		}.WithCorrelationID(item.correlationId)
		err = eproc.FailedEventsWriter.WriteMessages(core.WithTenant(ctx, item.tenant), msg)
		eproc.FailedEventsWriter.HandleResponse(err)
		return false
	} else {
		return true
	}
}

// Called when a message can not be unmarshaled to an event. The tenant is
// re-derived from the message subject (the worker only reaches this callback
// after confirming the subject carries a parseable tenant).
func (eproc *EventPersistenceProcessor) OnInvalidEvent(err error, msg messaging.Message) {
	tenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Dropping invalid event with no parseable tenant in subject %q", msg.Subject))
		return
	}
	failed := dmodel.NewFailedEvent(uint(proto.FailureReason_Invalid), eproc.Microservice.FunctionalArea,
		"message could not be parsed", err, msg.Value)
	eproc.failed <- failedItem{tenant: tenant, event: *failed, correlationId: msg.CorrelationID()}
}

// Called when a message can not be persisted. The inbound correlation id (E15)
// is threaded through from the worker so the emitted failed event carries it.
func (eproc *EventPersistenceProcessor) OnFailedEvent(tenant string, reason uint, event dmodel.ResolvedEvent, perr error, correlationId string) {
	// Marshal event message to protobuf.
	bytes, err := proto.MarshalResolvedEvent(&event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
	} else {
		failed := dmodel.NewFailedEvent(reason, eproc.Microservice.FunctionalArea,
			"event could not be processed", perr, bytes)
		eproc.failed <- failedItem{tenant: tenant, event: *failed, correlationId: correlationId}
	}
}

// Initialize pool of workers for persisting events.
func (eproc *EventPersistenceProcessor) initializeEventPersistenceWorkers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	eproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	eproc.workers = make([]*EventPersistenceWorker, 0, eproc.persistence.Writers)
	for w := 1; w <= eproc.persistence.Writers; w++ {
		resolver := NewEventPersistenceWorker(w, eproc.Api, eproc.messages,
			eproc.OnInvalidEvent, eproc.OnFailedEvent, eproc.metrics)
		resolver.MaxBatch = eproc.persistence.MaxBatch
		resolver.Linger = eproc.persistence.Linger()
		eproc.workers = append(eproc.workers, resolver)
		// Workers run on a background context (not the cancelable read context)
		// so that on shutdown they drain the remaining buffered messages to
		// completion and ack them rather than aborting in-flight persistence.
		eproc.workerWG.Add(1)
		go func(r *EventPersistenceWorker) {
			defer eproc.workerWG.Done()
			r.Process(context.Background())
		}(resolver)
	}
	// The operator's way to confirm a setting took.
	log.Info().Int("writers", eproc.persistence.Writers).Int("max_batch", eproc.persistence.MaxBatch).
		Dur("linger", eproc.persistence.Linger()).Msg("Event persistence writers started")
}

// WriterSettings is what one started writer runs with.
type WriterSettings struct {
	MaxBatch int
	Linger   time.Duration
}

// Writers reports the writers Initialize started, one entry each, so a caller can confirm
// its configuration reached them.
func (eproc *EventPersistenceProcessor) Writers() []WriterSettings {
	out := make([]WriterSettings, 0, len(eproc.workers))
	for _, w := range eproc.workers {
		out = append(out, WriterSettings{MaxBatch: w.MaxBatch, Linger: w.Linger})
	}
	return out
}

// Initialize outbound processing.
func (eproc *EventPersistenceProcessor) initializeOutboundProcessing(ctx context.Context) {
	eproc.failed = make(chan failedItem, FAILED_EVENT_BACKLOG_SIZE)
}

// Initialize component.
func (eproc *EventPersistenceProcessor) Initialize(ctx context.Context) error {
	return eproc.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (eproc *EventPersistenceProcessor) ExecuteInitialize(ctx context.Context) error {
	// Derive the cancelable context the read loop runs under (E10/A5).
	eproc.procCtx, eproc.procCancel = context.WithCancel(ctx)

	// Initialize pool of event resolvers.
	eproc.initializeEventPersistenceWorkers(ctx)

	// Initialize outbound processing channels.
	eproc.initializeOutboundProcessing(ctx)
	return nil
}

// Start component.
func (eproc *EventPersistenceProcessor) Start(ctx context.Context) error {
	return eproc.lifecycle.Start(ctx)
}

// readLoop drains the resolved-events stream into the persistence workers until the context
// is cancelled, the reader reports EOF, or a run of non-EOF read errors outlasts the pacer's
// budget — at which point the pacer has already ended the process, because a persistence
// loop that cannot read is silently losing the tail of every tenant's history, and a restart
// is the remedy for most of what causes it.
func (eproc *EventPersistenceProcessor) readLoop(ctx context.Context) {
	messaging.RunConsumer(ctx, eproc.ResolvedEventsReader, eproc.pacer(), func(msg messaging.Message) bool {
		return eproc.handOff(ctx, msg)
	})
}

// handOff gives one resolved event to the persistence workers, and reports whether the read
// loop should carry on. It abandons the handoff on shutdown so the loop can exit instead of
// blocking on a full channel (A5). The message is unacked at that point, so it is redelivered
// after restart.
func (eproc *EventPersistenceProcessor) handOff(ctx context.Context, msg messaging.Message) bool {
	select {
	case eproc.messages <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// Lifecycle callback that runs startup logic.
func (eproc *EventPersistenceProcessor) ExecuteStart(ctx context.Context) error {
	// Processing loop for failed events (drains until the failed channel closes).
	eproc.outboundWG.Add(1)
	go func() {
		defer eproc.outboundWG.Done()
		for {
			eof := eproc.ProcessFailedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for inbound messages (runs under the cancelable context so
	// ExecuteStop can stop it before the channels are closed).
	eproc.readerWG.Add(1)
	go func() {
		defer eproc.readerWG.Done()
		eproc.readLoop(eproc.procCtx)
	}()
	return nil
}

// Stop component.
func (eproc *EventPersistenceProcessor) Stop(ctx context.Context) error {
	return eproc.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic. It unwinds the pipeline in
// dependency order so no goroutine ever sends on a closed channel (A5): stop the
// reader, then close the channel it feeds, then wait for the workers it feeds
// before closing the channel they feed, then wait for the outbound loop.
func (eproc *EventPersistenceProcessor) ExecuteStop(context.Context) error {
	if eproc.procCancel != nil {
		eproc.procCancel()
	}
	eproc.readerWG.Wait()   // reader stopped: no more sends to messages
	close(eproc.messages)   //
	eproc.workerWG.Wait()   // workers drained + exited: no more sends to failed
	close(eproc.failed)     //
	eproc.outboundWG.Wait() // outbound loop drained + exited
	return nil
}

// Terminate component.
func (eproc *EventPersistenceProcessor) Terminate(ctx context.Context) error {
	return eproc.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (eproc *EventPersistenceProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
