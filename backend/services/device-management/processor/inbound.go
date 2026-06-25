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
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

const (
	EVENT_RESOLVER_COUNT        = 5   // Number of event resolvers running in parallel
	MESSAGE_BACKLOG_SIZE        = 100 // Number of inbound messages that can be read and waiting to be processed
	FAILED_EVENT_BACKLOG_SIZE   = 100 // Number of failed events that can be waiting to publish
	RESOLVED_EVENT_BACKLOG_SIZE = 100 // Number of resolved events that can be waiting to publish
)

// resolvedItem pairs a resolved event with the tenant it belongs to so the
// outbound producer can publish to the tenant's subject (the tenant is derived
// from the inbound subject and must travel with the event across the channel).
type resolvedItem struct {
	tenant string
	event  dmodel.ResolvedEvent
}

// failedItem pairs a failed event with its tenant for the same reason.
type failedItem struct {
	tenant string
	event  dmodel.FailedEvent
}

type InboundEventsProcessor struct {
	Microservice         *core.Microservice
	InboundEventsReader  messaging.MessageReader
	ResolvedEventsWriter messaging.MessageWriter
	FailedEventsWriter   messaging.MessageWriter
	Api                  dmodel.DeviceManagementApi
	AuthMode             string

	messages  chan messaging.Message
	failed    chan failedItem
	resolved  chan resolvedItem
	resolvers []*EventResolver

	lifecycle core.LifecycleManager
}

// Create a new inbound events processor. authMode is the device authentication
// policy applied while resolving inbound events (transport security, ADR-014).
func NewInboundEventsProcessor(ms *core.Microservice, inbound messaging.MessageReader, resolved messaging.MessageWriter,
	failed messaging.MessageWriter, callbacks core.LifecycleCallbacks, api dmodel.DeviceManagementApi, authMode string) *InboundEventsProcessor {
	iproc := &InboundEventsProcessor{
		Microservice:         ms,
		InboundEventsReader:  inbound,
		ResolvedEventsWriter: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
		AuthMode:             authMode,
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "inbound-event-proc")
	iproc.lifecycle = core.NewLifecycleManager(ipname, iproc, callbacks)
	return iproc
}

// Handle case where event failed to process.
func (iproc *InboundEventsProcessor) ProcessFailedEvent(ctx context.Context) bool {
	item, more := <-iproc.failed
	if more {
		log.Debug().Str("message", item.event.Message).Msg("received failed event")

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
		err = iproc.FailedEventsWriter.WriteMessages(core.WithTenant(ctx, item.tenant), msg)
		iproc.FailedEventsWriter.HandleResponse(err)
		return false
	} else {
		return true
	}
}

// Called when a message can not be unmarshaled to an event. The tenant is
// re-derived from the message subject (the resolver only reaches this callback
// after confirming the subject carries a parseable tenant).
func (iproc *InboundEventsProcessor) OnInvalidEvent(err error, msg messaging.Message) {
	tenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Dropping invalid event with no parseable tenant in subject %q", msg.Subject))
		return
	}
	failed := dmodel.NewFailedEvent(uint(proto.FailureReason_Invalid), iproc.Microservice.FunctionalArea,
		"message could not be parsed", err, msg.Value)
	iproc.failed <- failedItem{tenant: tenant, event: *failed}
}

// Called when an event can not be resolved.
func (iproc *InboundEventsProcessor) OnUnresolvedEvent(tenant string, reason uint, unrez esmodel.UnresolvedEvent, rezerr error) {
	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(&unrez)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal unresolved event to protobuf")
	} else {
		failed := dmodel.NewFailedEvent(reason, iproc.Microservice.FunctionalArea,
			"event could not be resolved", rezerr, bytes)
		iproc.failed <- failedItem{tenant: tenant, event: *failed}
	}
}

// Handle case where event was successfully resolved.
func (iproc *InboundEventsProcessor) ProcessResolvedEvent(ctx context.Context) bool {
	item, more := <-iproc.resolved
	if more {
		bytes, err := proto.MarshalResolvedEvent(&item.event)
		if err != nil {
			log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
		} else {
			msg := messaging.Message{
				Key:   []byte(strconv.FormatInt(int64(item.event.SourceDeviceId), 10)),
				Value: bytes,
			}
			err = iproc.ResolvedEventsWriter.WriteMessages(core.WithTenant(ctx, item.tenant), msg)
			iproc.ResolvedEventsWriter.HandleResponse(err)
		}
		return false
	} else {
		return true
	}
}

// Called when an event is successfully resolved.
func (iproc *InboundEventsProcessor) OnResolvedEvent(tenant string, events []EventResolutionResults) {
	for _, event := range events {
		iproc.resolved <- resolvedItem{tenant: tenant, event: *event.Resolved}
	}
}

// Initialize pool of workers for resolving events.
func (iproc *InboundEventsProcessor) initializeEventResolvers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	iproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	iproc.resolvers = make([]*EventResolver, 0)
	for w := 1; w <= EVENT_RESOLVER_COUNT; w++ {
		resolver := NewEventResolver(w, iproc.Api, iproc.AuthMode, iproc.messages,
			iproc.OnInvalidEvent, iproc.OnResolvedEvent, iproc.OnUnresolvedEvent)
		iproc.resolvers = append(iproc.resolvers, resolver)
		go resolver.Process(ctx)
	}
}

// Initialize outbound processing.
func (iproc *InboundEventsProcessor) initializeOutboundProcessing(ctx context.Context) {
	iproc.failed = make(chan failedItem, FAILED_EVENT_BACKLOG_SIZE)
	iproc.resolved = make(chan resolvedItem, RESOLVED_EVENT_BACKLOG_SIZE)
}

// Initialize component.
func (iproc *InboundEventsProcessor) Initialize(ctx context.Context) error {
	return iproc.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (iproc *InboundEventsProcessor) ExecuteInitialize(ctx context.Context) error {
	// Initialize pool of event resolvers.
	iproc.initializeEventResolvers(ctx)

	// Initialize outbound processing channels.
	iproc.initializeOutboundProcessing(ctx)
	return nil
}

// Start component.
func (iproc *InboundEventsProcessor) Start(ctx context.Context) error {
	return iproc.lifecycle.Start(ctx)
}

// Execute primary processing loop. This is done in a goroutine since it runs indefinitely.
func (iproc *InboundEventsProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := iproc.InboundEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on inbound events stream")
			return true
		} else {
			iproc.InboundEventsReader.HandleResponse(err)
		}
	} else {
		iproc.messages <- msg
	}
	return false
}

// Lifecycle callback that runs startup logic.
func (iproc *InboundEventsProcessor) ExecuteStart(ctx context.Context) error {
	// Processing loop for failed events.
	go func() {
		for {
			eof := iproc.ProcessFailedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for resolved events.
	go func() {
		for {
			eof := iproc.ProcessResolvedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for inbound messages.
	go func() {
		for {
			eof := iproc.ProcessMessage(ctx)
			if eof {
				break
			}
		}
	}()
	return nil
}

// Stop component.
func (iproc *InboundEventsProcessor) Stop(ctx context.Context) error {
	return iproc.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (iproc *InboundEventsProcessor) ExecuteStop(context.Context) error {
	close(iproc.messages)
	close(iproc.resolved)
	close(iproc.failed)
	return nil
}

// Terminate component.
func (iproc *InboundEventsProcessor) Terminate(ctx context.Context) error {
	return iproc.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (iproc *InboundEventsProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
