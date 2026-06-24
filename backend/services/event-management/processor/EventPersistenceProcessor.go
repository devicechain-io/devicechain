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
	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	kcore "github.com/devicechain-io/dc-microservice/kafka"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"
)

const (
	WORKER_COUNT                 = 5   // Number of event persisters running in parallel
	KAFKA_BACKLOG_SIZE           = 100 // Number of kafka messages that can be read and waiting to be processed
	FAILED_EVENT_BACKLOG_SIZE    = 100 // Number of failed events that can be waiting to be sent to kafka
	PERSISTED_EVENT_BACKLOG_SIZE = 100 // Number of persisted events that can be waiting to be sent to kafka
)

type EventPersistenceProcessor struct {
	Microservice          *core.Microservice
	ResolvedEventsReader  kcore.KafkaReader
	PersistedEventsWriter kcore.KafkaWriter
	FailedEventsWriter    kcore.KafkaWriter
	Api                   emmodel.EventManagementApi

	messages  chan kafka.Message
	persisted chan interface{}
	failed    chan dmodel.FailedEvent
	workers   []*EventPersistenceWorker

	lifecycle core.LifecycleManager
}

// Create a new inbound events processor.
func NewEventPersistenceProcessor(ms *core.Microservice, resolved kcore.KafkaReader, persisted kcore.KafkaWriter,
	failed kcore.KafkaWriter, callbacks core.LifecycleCallbacks, api emmodel.EventManagementApi) *EventPersistenceProcessor {
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
	failed, more := <-eproc.failed
	log.Debug().Msg(fmt.Sprintf("received failed event: %s (%s)", failed.Message, failed.Error))
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
		err = eproc.FailedEventsWriter.WriteMessages(ctx, msg)
		eproc.FailedEventsWriter.HandleResponse(err)
		return false
	} else {
		return true
	}
}

// Called when a message can not be unmarshaled to an event.
func (eproc *EventPersistenceProcessor) OnInvalidEvent(err error, msg kafka.Message) {
	failed := dmodel.NewFailedEvent(uint(proto.FailureReason_Invalid), eproc.Microservice.FunctionalArea,
		"message could not be parsed", err, msg.Value)
	eproc.failed <- *failed
}

// Called when a message can not be persisted.
func (eproc *EventPersistenceProcessor) OnFailedEvent(reason uint, event dmodel.ResolvedEvent, perr error) {
	// Marshal event message to protobuf.
	bytes, err := proto.MarshalResolvedEvent(&event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
	} else {
		failed := dmodel.NewFailedEvent(reason, eproc.Microservice.FunctionalArea,
			"event could not be processed", perr, bytes)
		eproc.failed <- *failed
	}
}

// Handle case where event was successfully persisted.
func (eproc *EventPersistenceProcessor) ProcessPersistedEvent(ctx context.Context) bool {
	_, more := <-eproc.persisted
	if more {
		bytes := []byte("test")
		msg := kafka.Message{
			Key:   []byte("xxx"),
			Value: bytes,
		}
		err := eproc.PersistedEventsWriter.WriteMessages(ctx, msg)
		eproc.PersistedEventsWriter.HandleResponse(err)
		return false
	} else {
		return true
	}
}

// Called when an event is successfully resolved.
func (eproc *EventPersistenceProcessor) OnPersistedEvent(event interface{}) {
	eproc.persisted <- event
}

// Initialize pool of workers for persisting events.
func (eproc *EventPersistenceProcessor) initializeEventPersistenceWorkers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	eproc.messages = make(chan kafka.Message, KAFKA_BACKLOG_SIZE)
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
	eproc.failed = make(chan dmodel.FailedEvent, FAILED_EVENT_BACKLOG_SIZE)
	eproc.persisted = make(chan interface{}, PERSISTED_EVENT_BACKLOG_SIZE)
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
