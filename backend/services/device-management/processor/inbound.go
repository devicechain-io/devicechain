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

// ackCoord coordinates acknowledgement of one source message across the 1->N
// resolved-event fan-out it produced. The source is acked once every resolved
// event has been durably published, or left unacked if any publish fails so the
// whole message is redelivered after AckWait (ADR-022 review A3). It is only ever touched by
// the single ProcessResolvedEvent goroutine, so no locking is required.
type ackCoord struct {
	src       messaging.Message
	remaining int
	failed    bool
}

// resolvedItem pairs a resolved event with the tenant it belongs to so the
// outbound producer can publish to the tenant's subject (the tenant is derived
// from the inbound subject and must travel with the event across the channel).
// coord ties the item back to its source message for ack coordination.
// correlation carries the inbound message's correlation id so the outbound
// resolved event is stamped with it and stays traceable end to end (E15).
type resolvedItem struct {
	tenant      string
	event       dmodel.ResolvedEvent
	coord       *ackCoord
	correlation string
}

// failedItem pairs a failed event with its tenant for the same reason.
// correlation carries the inbound message's correlation id onto the outbound
// failed event for the same end-to-end traceability (E15).
type failedItem struct {
	tenant      string
	event       dmodel.FailedEvent
	correlation string
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

	// metrics records RED instrumentation for the resolve loop (E13); it is
	// shared across every resolver worker.
	metrics *core.ProcessorMetrics

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain senders before closing the channels they feed, so a
	// resolver or the reader can never send on a closed channel at SIGTERM.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup
	outboundWG sync.WaitGroup

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
		}.WithCorrelationID(item.correlation)
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
	iproc.failed <- failedItem{tenant: tenant, event: *failed, correlation: msg.CorrelationID()}
}

// Called when an event can not be resolved. correlation is the inbound message's
// correlation id, carried onto the outbound failed event for traceability (E15).
func (iproc *InboundEventsProcessor) OnUnresolvedEvent(tenant string, reason uint, unrez esmodel.UnresolvedEvent, rezerr error, correlation string) {
	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(&unrez)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal unresolved event to protobuf")
	} else {
		failed := dmodel.NewFailedEvent(reason, iproc.Microservice.FunctionalArea,
			"event could not be resolved", rezerr, bytes)
		iproc.failed <- failedItem{tenant: tenant, event: *failed, correlation: correlation}
	}
}

// Handle case where event was successfully resolved. The source message is acked
// only after the last resolved event it produced has been durably published, and
// left unacked if any publish fails so AckWait redelivers the whole message (A3).
func (iproc *InboundEventsProcessor) ProcessResolvedEvent(ctx context.Context) bool {
	item, more := <-iproc.resolved
	if more {
		bytes, err := proto.MarshalResolvedEvent(&item.event)
		if err == nil {
			msg := messaging.Message{
				// Partition on the source device token so a device's events stay
				// ordered on one stream partition (ADR-044): the token is 1:1 with the
				// device, so ordering is preserved and no id crosses the seam.
				Key:   []byte(item.event.SourceDeviceToken),
				Value: bytes,
			}.WithCorrelationID(item.correlation)
			err = iproc.ResolvedEventsWriter.WriteMessages(core.WithTenant(ctx, item.tenant), msg)
			iproc.ResolvedEventsWriter.HandleResponse(err)
		} else {
			log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
		}
		iproc.settleResolved(item.coord, err)
		return false
	} else {
		return true
	}
}

// settleResolved records the outcome of publishing one resolved event against
// its source's ack coordinator: a publish failure latches failure so the source
// is left unacked (AckWait redelivers the whole message); success decrements the
// outstanding count and acks the source when the last resolved event has been
// published.
func (iproc *InboundEventsProcessor) settleResolved(coord *ackCoord, err error) {
	if coord == nil {
		return
	}
	if err != nil {
		// A publish failed: latch failure so the source is never acked, which leaves
		// the whole message for AckWait-paced redelivery. Do NOT nak — an immediate
		// nak would burn MaxDeliver in ~1.4ms inside a downstream outage (ADR-030).
		coord.failed = true
		return
	}
	coord.remaining--
	if coord.remaining == 0 && !coord.failed {
		_ = coord.src.Ack()
	}
}

// Called when an event is successfully resolved. An event resolving to no tracked
// relationships produces no output, so the source is acked immediately; otherwise
// a coordinator acks it once all of its resolved events have been published.
func (iproc *InboundEventsProcessor) OnResolvedEvent(src messaging.Message, tenant string, events []EventResolutionResults) {
	if len(events) == 0 {
		_ = src.Ack()
		return
	}
	coord := &ackCoord{src: src, remaining: len(events)}
	correlation := src.CorrelationID()
	for _, event := range events {
		iproc.resolved <- resolvedItem{tenant: tenant, event: *event.Resolved, coord: coord, correlation: correlation}
	}
}

// Initialize pool of workers for resolving events.
func (iproc *InboundEventsProcessor) initializeEventResolvers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	iproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	iproc.resolvers = make([]*EventResolver, 0)
	for w := 1; w <= EVENT_RESOLVER_COUNT; w++ {
		resolver := NewEventResolver(w, iproc.Api, iproc.AuthMode, iproc.messages,
			iproc.OnInvalidEvent, iproc.OnResolvedEvent, iproc.OnUnresolvedEvent, iproc.metrics)
		iproc.resolvers = append(iproc.resolvers, resolver)
		// Resolvers run on a background context (not the cancelable read context)
		// so that on shutdown they drain the remaining buffered messages to
		// completion and ack them rather than aborting in-flight resolution.
		iproc.workerWG.Add(1)
		go func(r *EventResolver) {
			defer iproc.workerWG.Done()
			r.Process(context.Background())
		}(resolver)
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
	// Derive the cancelable context the read loop runs under (E10/A5).
	iproc.procCtx, iproc.procCancel = context.WithCancel(ctx)

	// Build RED instrumentation for the resolve loop before the resolvers are
	// created so it can be shared across every worker (E13).
	iproc.metrics = iproc.Microservice.NewProcessorMetrics("resolve")

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
		// Hand off to the resolvers, but abandon the handoff on shutdown so the
		// loop can exit instead of blocking on a full channel (A5). The message
		// is unacked, so it is redelivered after restart.
		select {
		case iproc.messages <- msg:
		case <-ctx.Done():
			return true
		}
	}
	return false
}

// Lifecycle callback that runs startup logic.
func (iproc *InboundEventsProcessor) ExecuteStart(ctx context.Context) error {
	// Processing loop for failed events (drains until the failed channel closes).
	iproc.outboundWG.Add(1)
	go func() {
		defer iproc.outboundWG.Done()
		for {
			eof := iproc.ProcessFailedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for resolved events (drains until the resolved channel closes).
	iproc.outboundWG.Add(1)
	go func() {
		defer iproc.outboundWG.Done()
		for {
			eof := iproc.ProcessResolvedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for inbound messages (runs under the cancelable context so
	// ExecuteStop can stop it before the channels are closed).
	iproc.readerWG.Add(1)
	go func() {
		defer iproc.readerWG.Done()
		for {
			eof := iproc.ProcessMessage(iproc.procCtx)
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

// Lifecycle callback that runs shutdown logic. It unwinds the pipeline in
// dependency order so no goroutine ever sends on a closed channel (A5): stop the
// reader, then close the channel it feeds, then wait for the resolvers it feeds
// before closing the channels they feed, then wait for the outbound loops.
func (iproc *InboundEventsProcessor) ExecuteStop(context.Context) error {
	if iproc.procCancel != nil {
		iproc.procCancel()
	}
	iproc.readerWG.Wait()   // reader stopped: no more sends to messages
	close(iproc.messages)   //
	iproc.workerWG.Wait()   // resolvers drained + exited: no more sends to resolved/failed
	close(iproc.resolved)   //
	close(iproc.failed)     //
	iproc.outboundWG.Wait() // outbound loops drained + exited
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
