// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	gql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/graphql"
	"github.com/devicechain-io/dc-event-sources/model"
	processor "github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventSourcesConfiguration
	EventSources  []core.LifecycleComponent

	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	// Messaging.
	InboundEventsWriter messaging.MessageWriter
	FailedDecodeWriter  messaging.MessageWriter

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
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
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
		case processor.TYPE_HTTP:
			http, err := processor.NewHttpEventSource(source.Id, source.Configuration,
				decoder, onMessageReceived, onEventDecoded, onEventDecodeFailed)
			if err != nil {
				return err
			}
			created = append(created, http)
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

// Called by event sources when an event is successfully decoded. The tenant is
// derived by the source from its own addressing (MQTT topic / HTTP path) before
// the event reaches here; an empty tenant cannot be published to a tenant-scoped
// subject, so the event is dropped (fail-closed) rather than published unscoped.
func onEventDecoded(source string, tenant string, event *model.UnresolvedEvent, payload interface{}) {
	// Increment counter for metrics.
	DecodedCounter.WithLabelValues(source).Inc()

	event.Source = source
	event.Payload = payload

	if tenant == "" {
		log.Warn().Msg(fmt.Sprintf("Dropping decoded event from source %q with no tenant", source))
		return
	}
	ctx := core.WithTenant(context.Background(), tenant)

	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal event to protobuf")
		return
	}

	// Create and deliver message (writer derives the scoped subject from ctx).
	msg := messaging.Message{
		Key:   []byte(event.Device),
		Value: bytes,
	}
	err = InboundEventsWriter.WriteMessages(ctx, msg)
	InboundEventsWriter.HandleResponse(err)
}

// Handle failed decoding.
func onEventDecodeFailed(source string, tenant string, raw []byte, err error) {
	// Increment counter for metrics.
	FailedDecodeCounter.WithLabelValues(source).Inc()

	// A message that could not be decoded is still routed to the failed-decode
	// subject for the originating tenant; without a tenant it cannot be scoped, so
	// it is dropped fail-closed.
	if tenant == "" {
		log.Warn().Msg(fmt.Sprintf("Dropping failed-decode message from source %q with no tenant", source))
		return
	}
	ctx := core.WithTenant(context.Background(), tenant)

	// Create and deliver message.
	msg := messaging.Message{
		Key:   []byte(source),
		Value: raw,
	}
	senderr := FailedDecodeWriter.WriteMessages(ctx, msg)
	FailedDecodeWriter.HandleResponse(senderr)
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	ievents, err := nmgr.NewWriter(config.SUBJECT_INBOUND_EVENTS)
	if err != nil {
		return err
	}
	InboundEventsWriter = ievents

	failed, err := nmgr.NewWriter(config.SUBJECT_FAILED_DECODE)
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

	// Create and initialize nats manager.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	err = NatsManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{}

	// Create and initialize graphql manager.
	schema := graphql.SchemaContent
	parsed := gql.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), *parsed, providers, Microservice.Readiness)
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
	// Start nats manager.
	err := NatsManager.Start(ctx)
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

	// Stop nats manager.
	err = NatsManager.Stop(ctx)
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

	// Terminate nats manager.
	err = NatsManager.Terminate(ctx)
	if err != nil {
		return err
	}

	return nil
}
