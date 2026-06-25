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
// subject and must travel with the event across the channel).
type failedItem struct {
	tenant string
	event  dmodel.FailedEvent
}

type EventPersistenceProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	FailedEventsWriter   messaging.MessageWriter
	Api                  emmodel.EventManagementApi

	messages chan messaging.Message
	failed   chan failedItem
	workers  []*EventPersistenceWorker

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain senders before closing the channels they feed, so a
	// worker or the reader can never send on a closed channel at SIGTERM.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup
	outboundWG sync.WaitGroup

	lifecycle core.LifecycleManager
}

// Create a new inbound events processor.
func NewEventPersistenceProcessor(ms *core.Microservice, resolved messaging.MessageReader,
	failed messaging.MessageWriter, callbacks core.LifecycleCallbacks, api emmodel.EventManagementApi) *EventPersistenceProcessor {
	eproc := &EventPersistenceProcessor{
		Microservice:         ms,
		ResolvedEventsReader: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "event-persist-proc")
	eproc.lifecycle = core.NewLifecycleManager(ipname, eproc, callbacks)
	return eproc
}

// Handle case where event failed to process.
func (eproc *EventPersistenceProcessor) ProcessFailedEvent(ctx context.Context) bool {
	item, more := <-eproc.failed
	if more {
		log.Debug().Str("message", item.event.Message).Str("error", item.event.Error).Msg("received failed event")

		// Marshal event message to protobuf.
		bytes, err := proto.MarshalFailedEvent(&item.event)
		if err != nil {
			log.Error().Err(err).Msg("unable to marshal event to protobuf")
		}

		// Create and deliver message on the failed event's tenant subject.
		msg := messaging.Message{
			Key:   []byte(strconv.FormatInt(int64(item.event.Reason), 10)),
			Value: bytes,
		}
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
	eproc.failed <- failedItem{tenant: tenant, event: *failed}
}

// Called when a message can not be persisted.
func (eproc *EventPersistenceProcessor) OnFailedEvent(tenant string, reason uint, event dmodel.ResolvedEvent, perr error) {
	// Marshal event message to protobuf.
	bytes, err := proto.MarshalResolvedEvent(&event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
	} else {
		failed := dmodel.NewFailedEvent(reason, eproc.Microservice.FunctionalArea,
			"event could not be processed", perr, bytes)
		eproc.failed <- failedItem{tenant: tenant, event: *failed}
	}
}

// Initialize pool of workers for persisting events.
func (eproc *EventPersistenceProcessor) initializeEventPersistenceWorkers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	eproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	eproc.workers = make([]*EventPersistenceWorker, 0)
	for w := 1; w <= WORKER_COUNT; w++ {
		resolver := NewEventPersistenceWorker(w, eproc.Api, eproc.messages,
			eproc.OnInvalidEvent, eproc.OnFailedEvent)
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
func (eproc *EventPersistenceProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := eproc.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream")
			return true
		} else {
			eproc.ResolvedEventsReader.HandleResponse(err)
		}
	} else {
		// Hand off to the workers, but abandon the handoff on shutdown so the
		// loop can exit instead of blocking on a full channel (A5). The message
		// is unacked, so it is redelivered after restart.
		select {
		case eproc.messages <- msg:
		case <-ctx.Done():
			return true
		}
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
