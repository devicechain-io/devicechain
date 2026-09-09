// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

const (
	WORKER_COUNT              = 5   // Number of event persisters running in parallel
	MESSAGE_BACKLOG_SIZE      = 100 // Number of messages that can be read and waiting to be processed
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

	// metrics records RED-style instrumentation (rate/errors/duration) for the
	// persist loop (ADR-022 E13); shared by reference across all workers.
	metrics *core.ProcessorMetrics

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

// NewPersistMetrics builds this processor's RED instrumentation.
//
// 🔴 IT IS SEPARATE FROM THE CONSTRUCTOR BECAUSE THE TWO RUN IN DIFFERENT PHASES.
// The processor is built inside the NATS manager's oncreate callback, which runs on
// EVERY start — it has to, because the processor holds a reader bound to the
// connection — while a Prometheus collector may be registered only once or the second
// registration panics. So the caller builds this in the INITIALIZE phase and hands the
// same instruments to every start's processor.
func NewPersistMetrics(ms *core.Microservice) *core.ProcessorMetrics {
	return ms.NewProcessorMetrics("persist")
}

// Create a new inbound events processor.
//
// metrics is built once in the initialize phase (see NewPersistMetrics) and shared by
// every processor this service constructs, because this constructor runs again on
// every start.
func NewEventPersistenceProcessor(ms *core.Microservice, resolved messaging.MessageReader,
	failed messaging.MessageWriter, callbacks core.LifecycleCallbacks, api emmodel.EventManagementApi,
	metrics *core.ProcessorMetrics) *EventPersistenceProcessor {
	eproc := &EventPersistenceProcessor{
		Microservice:         ms,
		ResolvedEventsReader: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
		metrics:              metrics,
	}

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
	eproc.workers = make([]*EventPersistenceWorker, 0)
	for w := 1; w <= WORKER_COUNT; w++ {
		resolver := NewEventPersistenceWorker(w, eproc.Api, eproc.messages,
			eproc.OnInvalidEvent, eproc.OnFailedEvent, eproc.metrics)
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

// Execute primary processing loop. This is done in a goroutine since it runs indefinitely.
//
// It returns true when the loop should stop: on EOF, on shutdown, and when a run of non-EOF
// read errors has outlasted the pacer's budget — in which case the pacer has already ended
// the process, because a persistence loop that cannot read is silently losing the tail of
// every tenant's history, and a restart is the remedy for most of what causes it.
func (eproc *EventPersistenceProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := eproc.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream")
			return true
		}
		eproc.ResolvedEventsReader.HandleResponse(err)
		return eproc.pacer().PauseAfterError(ctx, err)
	}
	eproc.pacer().Succeeded()
	// Hand off to the workers, but abandon the handoff on shutdown so the
	// loop can exit instead of blocking on a full channel (A5). The message
	// is unacked, so it is redelivered after restart.
	select {
	case eproc.messages <- msg:
	case <-ctx.Done():
		return true
	}
	return false
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
		for {
			eof := eproc.ProcessMessage(eproc.procCtx)
			if eof {
				break
			}
		}
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
