// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

const (
	WORKER_COUNT                 = 5   // Number of event persisters running in parallel
	MESSAGE_BACKLOG_SIZE         = 100 // Number of messages that can be read and waiting to be processed
	FAILED_EVENT_BACKLOG_SIZE    = 100 // Number of failed events that can be waiting to publish
	PERSISTED_EVENT_BACKLOG_SIZE = 100 // Number of persisted events that can be waiting to publish
)

// persistedItem pairs a persisted event with the tenant it belongs to so the
// outbound producer can publish to the tenant's subject (the tenant is derived
// from the inbound subject and must travel with the event across the channel).
type persistedItem struct {
	tenant string
	event  interface{}
}

// failedItem pairs a failed event with its tenant for the same reason.
type failedItem struct {
	tenant string
	event  dmodel.FailedEvent
}

type EventPersistenceProcessor struct {
	Microservice          *core.Microservice
	ResolvedEventsReader  messaging.MessageReader
	PersistedEventsWriter messaging.MessageWriter
	FailedEventsWriter    messaging.MessageWriter
	Api                   emmodel.EventManagementApi

	messages  chan messaging.Message
	persisted chan persistedItem
	failed    chan failedItem
	workers   []*EventPersistenceWorker

	lifecycle core.LifecycleManager
}

// Create a new inbound events processor.
func NewEventPersistenceProcessor(ms *core.Microservice, resolved messaging.MessageReader, persisted messaging.MessageWriter,
	failed messaging.MessageWriter, callbacks core.LifecycleCallbacks, api emmodel.EventManagementApi) *EventPersistenceProcessor {
	eproc := &EventPersistenceProcessor{
		Microservice:          ms,
		ResolvedEventsReader:  resolved,
		PersistedEventsWriter: persisted,
		FailedEventsWriter:    failed,
		Api:                   api,
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

// Handle case where event was successfully persisted.
func (eproc *EventPersistenceProcessor) ProcessPersistedEvent(ctx context.Context) bool {
	item, more := <-eproc.persisted
	if more {
		bytes := []byte("test")
		msg := messaging.Message{
			Key:   []byte("xxx"),
			Value: bytes,
		}
		err := eproc.PersistedEventsWriter.WriteMessages(core.WithTenant(ctx, item.tenant), msg)
		eproc.PersistedEventsWriter.HandleResponse(err)
		return false
	} else {
		return true
	}
}

// Called when an event is successfully persisted.
func (eproc *EventPersistenceProcessor) OnPersistedEvent(tenant string, event interface{}) {
	eproc.persisted <- persistedItem{tenant: tenant, event: event}
}

// Initialize pool of workers for persisting events.
func (eproc *EventPersistenceProcessor) initializeEventPersistenceWorkers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	eproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	eproc.workers = make([]*EventPersistenceWorker, 0)
	for w := 1; w <= WORKER_COUNT; w++ {
		resolver := NewEventPersistenceWorker(w, eproc.Api, eproc.messages,
			eproc.OnInvalidEvent, eproc.OnPersistedEvent, eproc.OnFailedEvent)
		eproc.workers = append(eproc.workers, resolver)
		go resolver.Process(ctx)
	}
}

// Initialize outbound processing.
func (eproc *EventPersistenceProcessor) initializeOutboundProcessing(ctx context.Context) {
	eproc.failed = make(chan failedItem, FAILED_EVENT_BACKLOG_SIZE)
	eproc.persisted = make(chan persistedItem, PERSISTED_EVENT_BACKLOG_SIZE)
}

// Initialize component.
func (eproc *EventPersistenceProcessor) Initialize(ctx context.Context) error {
	return eproc.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (eproc *EventPersistenceProcessor) ExecuteInitialize(ctx context.Context) error {
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
		eproc.messages <- msg
	}
	return false
}

// Lifecycle callback that runs startup logic.
func (eproc *EventPersistenceProcessor) ExecuteStart(ctx context.Context) error {
	// Processing loop for failed events.
	go func() {
		for {
			eof := eproc.ProcessFailedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for resolved events.
	go func() {
		for {
			eof := eproc.ProcessPersistedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for inbound messages.
	go func() {
		for {
			eof := eproc.ProcessMessage(ctx)
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

// Lifecycle callback that runs shutdown logic.
func (eproc *EventPersistenceProcessor) ExecuteStop(context.Context) error {
	close(eproc.messages)
	close(eproc.persisted)
	close(eproc.failed)
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
