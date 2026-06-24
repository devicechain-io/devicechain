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

package main

import (
	"context"
	"encoding/json"
	"fmt"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/graphql"
	"github.com/devicechain-io/dc-event-sources/model"
	processor "github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	kcore "github.com/devicechain-io/dc-microservice/kafka"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventSourcesConfiguration
	EventSources  []core.LifecycleComponent

	GraphQLManager *gqlcore.GraphQLManager
	KakfaManager   *kcore.KafkaManager

	// Kafka.
	InboundEventsWriter kcore.KafkaWriter
	FailedDecodeWriter  kcore.KafkaWriter

	// Metrics
	MessagesCounter     *prometheus.CounterVec
	DecodedCounter      *prometheus.CounterVec
	FailedDecodeCounter *prometheus.CounterVec
)

func main() {
	callbacks := core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceInitialized,
		},
		Starter: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceStarted,
		},
		Stopper: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceStopped,
			Postprocess: func(context.Context) error { return nil },
		},
		Terminator: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceTerminated,
			Postprocess: func(context.Context) error { return nil },
		},
	}
	Microservice = core.NewMicroservice(callbacks)
	Microservice.Run()
}

// Parses the configuration from raw bytes.
func parseConfiguration() error {
	config := &config.EventSourcesConfiguration{}
	err := json.Unmarshal(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Initialize metrics.
func initializeMetrics() {
	MessagesCounter = Microservice.NewCounterVec(
		"total_inbound_messages",
		"Count of total inbound messages from event sources",
		[]string{"source"})
	DecodedCounter = Microservice.NewCounterVec(
		"total_msg_decode_successful",
		"Count of total messages successfully decoded",
		[]string{"source"})
	FailedDecodeCounter = Microservice.NewCounterVec(
		"total_msg_failed_decode",
		"Count of total messages that failed to decode",
		[]string{"source"})
}

// Create decoder based on event source configuration.
func createDecoder(source config.EventSource) (processor.Decoder, error) {
	switch source.Decoder.Type {
	case processor.DECODER_TYPE_JSON:
		return processor.NewJsonDecoder(source.Decoder.Configuration), nil
	default:
		return nil, fmt.Errorf("unkown decoder type: %s", source.Type)
	}
}

// Use configuration to build event sources.
func buildEventSources() error {
	created := make([]core.LifecycleComponent, 0)
	for _, source := range Configuration.EventSources {
		// Create decoder.
		decoder, err := createDecoder(source)
		if err != nil {
			return err
		}

		// Create event source.
		switch source.Type {
		case processor.TYPE_MQTT:
			mqtt, err := processor.NewMqttEventSource(source.Id, source.Configuration,
				decoder, onMessageReceived, onEventDecoded, onEventDecodeFailed)
			if err != nil {
				return err
			}
			created = append(created, mqtt)
		default:
			return fmt.Errorf("unkown event source type: %s", source.Type)
		}
	}
	EventSources = created
	return nil
}

// Handle accounting for received messages.
func onMessageReceived(source string, raw []byte) {
	// Increment counter for metrics.
	MessagesCounter.WithLabelValues(source).Inc()
}

// Called by event sources when an event is successfully decoded.
func onEventDecoded(source string, event *model.UnresolvedEvent, payload interface{}) {
	// Increment counter for metrics.
	DecodedCounter.WithLabelValues(source).Inc()

	event.Source = source
	event.Payload = payload

	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal event to protobuf")
	}

	// Create and deliver message.
	msg := kafka.Message{
		Key:   []byte(event.Device),
		Value: bytes,
	}
	err = InboundEventsWriter.WriteMessages(context.Background(), msg)
	InboundEventsWriter.HandleResponse(err)
}

// Handle failed decoding.
func onEventDecodeFailed(source string, raw []byte, err error) {
	// Increment counter for metrics.
	FailedDecodeCounter.WithLabelValues(source).Inc()

	// Create and deliver message.
	msg := kafka.Message{
		Key:   []byte(source),
		Value: raw,
	}
	senderr := FailedDecodeWriter.WriteMessages(context.Background(), msg)
	FailedDecodeWriter.HandleResponse(senderr)
}

// Create kafka components used by this microservice.
func createKafkaComponents(kmgr *kcore.KafkaManager) error {
	ievents, err := kmgr.NewWriter(kmgr.NewScopedTopic(config.KAFKA_TOPIC_INBOUND_EVENTS))
	if err != nil {
		return err
	}
	InboundEventsWriter = ievents

	failed, err := kmgr.NewWriter(kmgr.NewScopedTopic(config.KAFKA_TOPIC_FAILED_DECODE))
	if err != nil {
		return err
	}
	FailedDecodeWriter = failed
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Build event sources from configuration.
	err = buildEventSources()
	if err != nil {
		return err
	}

	// Initialize metrics.
	initializeMetrics()

	// Create and initialize kafka manager.
	KakfaManager = kcore.NewKafkaManager(Microservice, core.NewNoOpLifecycleCallbacks(), createKafkaComponents)
	err = KakfaManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{}

	// Create and initialize graphql manager.
	schema := graphql.SchemaContent
	parsed := gql.MustParseSchema(schema, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), *parsed, providers)
	err = GraphQLManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Initialize each event source.
	for _, source := range EventSources {
		err = source.Initialize(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Start kafka manager.
	err := KakfaManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start graphql manager.
	err = GraphQLManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start each event source.
	for _, source := range EventSources {
		err = source.Start(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop each event source.
	for _, source := range EventSources {
		err := source.Stop(ctx)
		if err != nil {
			return err
		}
	}

	// Stop graphql manager.
	err := GraphQLManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop kafka manager.
	err = KakfaManager.Stop(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// Terminate each event source.
	for _, source := range EventSources {
		err := source.Terminate(ctx)
		if err != nil {
			return err
		}
	}

	// Terminate graphql manager.
	err := GraphQLManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate kafka manager.
	err = KakfaManager.Terminate(ctx)
	if err != nil {
		return err
	}

	return nil
}
