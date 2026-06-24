/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
	kcore "github.com/devicechain-io/dc-microservice/kafka"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"
)

const (
	EVENT_RESOLVER_COUNT        = 5   // Number of event resolvers running in parallel
	KAFKA_BACKLOG_SIZE          = 100 // Number of Kafka messages that can be read and waiting to be processed
	FAILED_EVENT_BACKLOG_SIZE   = 100 // Number of failed events that can be waiting to push to kafka
	RESOLVED_EVENT_BACKLOG_SIZE = 100 // Number of resolved events that can be waiting to push to kafka
)

type InboundEventsProcessor struct {
	Microservice         *core.Microservice
	InboundEventsReader  kcore.KafkaReader
	ResolvedEventsWriter kcore.KafkaWriter
	FailedEventsWriter   kcore.KafkaWriter
	Api                  dmodel.DeviceManagementApi

	messages  chan kafka.Message
	failed    chan dmodel.FailedEvent
	resolved  chan dmodel.ResolvedEvent
	resolvers []*EventResolver

	lifecycle core.LifecycleManager
}

// Create a new inbound events processor.
func NewInboundEventsProcessor(ms *core.Microservice, inbound kcore.KafkaReader, resolved kcore.KafkaWriter,
	failed kcore.KafkaWriter, callbacks core.LifecycleCallbacks, api dmodel.DeviceManagementApi) *InboundEventsProcessor {
	iproc := &InboundEventsProcessor{
		Microservice:         ms,
		InboundEventsReader:  inbound,
		ResolvedEventsWriter: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "inbound-event-proc")
	iproc.lifecycle = core.NewLifecycleManager(ipname, iproc, callbacks)
	return iproc
}

// Handle case where event failed to process.
func (iproc *InboundEventsProcessor) ProcessFailedEvent(ctx context.Context) bool {
	failed, more := <-iproc.failed
	log.Debug().Msg(fmt.Sprintf("received failed event: %s", failed.Message))
	if more {
		// Marshal event message to protobuf.
		bytes, err := proto.MarshalFailedEvent(&failed)
		if err != nil {
			log.Error().Err(err).Msg("unable to marshal event to protobuf")
		}

		// Create and deliver message.
		msg := kafka.Message{
			Key:   []byte(strconv.FormatInt(int64(failed.Reason), 10)),
			Value: bytes,
		}
		err = iproc.FailedEventsWriter.WriteMessages(ctx, msg)
		iproc.FailedEventsWriter.HandleResponse(err)
		return false
	} else {
		return true
	}
}

// Called when a message can not be unmarshaled to an event.
func (iproc *InboundEventsProcessor) OnInvalidEvent(err error, msg kafka.Message) {
	failed := dmodel.NewFailedEvent(uint(proto.FailureReason_Invalid), iproc.Microservice.FunctionalArea,
		"message could not be parsed", err, msg.Value)
	iproc.failed <- *failed
}

// Called when an event can not be resolved.
func (iproc *InboundEventsProcessor) OnUnresolvedEvent(reason uint, unrez esmodel.UnresolvedEvent, rezerr error) {
	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(&unrez)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal unresolved event to protobuf")
	} else {
		failed := dmodel.NewFailedEvent(reason, iproc.Microservice.FunctionalArea,
			"event could not be resolved", rezerr, bytes)
		iproc.failed <- *failed
	}
}

// Handle case where event was successfully resolved.
func (iproc *InboundEventsProcessor) ProcessResolvedEvent(ctx context.Context) bool {
	resolved, more := <-iproc.resolved
	if more {
		bytes, err := proto.MarshalResolvedEvent(&resolved)
		if err != nil {
			log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
		} else {
			msg := kafka.Message{
				Key:   []byte(strconv.FormatInt(int64(resolved.SourceDeviceId), 10)),
				Value: bytes,
			}
			err = iproc.ResolvedEventsWriter.WriteMessages(ctx, msg)
			iproc.ResolvedEventsWriter.HandleResponse(err)
		}
		return false
	} else {
		return true
	}
}

// Called when an event is successfully resolved.
func (iproc *InboundEventsProcessor) OnResolvedEvent(events []EventResolutionResults) {
	for _, event := range events {
		iproc.resolved <- *event.Resolved
	}
}

// Initialize pool of workers for resolving events.
func (iproc *InboundEventsProcessor) initializeEventResolvers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	iproc.messages = make(chan kafka.Message, KAFKA_BACKLOG_SIZE)
	iproc.resolvers = make([]*EventResolver, 0)
	for w := 1; w <= EVENT_RESOLVER_COUNT; w++ {
		resolver := NewEventResolver(w, iproc.Api, iproc.messages,
			iproc.OnInvalidEvent, iproc.OnResolvedEvent, iproc.OnUnresolvedEvent)
		iproc.resolvers = append(iproc.resolvers, resolver)
		go resolver.Process(ctx)
	}
}

// Initialize outbound processing.
func (iproc *InboundEventsProcessor) initializeOutboundProcessing(ctx context.Context) {
	iproc.failed = make(chan dmodel.FailedEvent, FAILED_EVENT_BACKLOG_SIZE)
	iproc.resolved = make(chan dmodel.ResolvedEvent, RESOLVED_EVENT_BACKLOG_SIZE)
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
