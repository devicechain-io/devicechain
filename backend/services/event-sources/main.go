// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"fmt"

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

	// RateLimiter meters inbound events per tenant against the platform ingest
	// ceiling; over-limit events are shed at the receive point before decode.
	RateLimiter *core.TenantRateLimiter

	// Messaging.
	InboundEventsWriter messaging.MessageWriter
	FailedDecodeWriter  messaging.MessageWriter

	// Metrics
	MessagesCounter     *prometheus.CounterVec
	DecodedCounter      *prometheus.CounterVec
	FailedDecodeCounter *prometheus.CounterVec
	RateLimitedCounter  *prometheus.CounterVec
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
	RateLimitedCounter = Microservice.NewCounterVec(
		"total_msg_rate_limited",
		"Count of inbound messages shed for exceeding the per-tenant ingest rate limit",
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
			// The broker's TLS material (ADR-025) belongs to the NATS MQTT gateway,
			// so only apply it to a source that actually dials the gateway. A source
			// pointed at some other MQTT broker must NOT be forced to verify against
			// the NATS CA (it would dial ssl:// at a plaintext port or fail
			// verification); per-source TLS for external brokers is a later concern.
			// When applied, serverName is the dialed host, matched against the SANs.
			natscfg := Microservice.InstanceConfiguration.Infrastructure.Nats
			var tlsConfig *tls.Config
			var user, pass string
			if source.Configuration["host"] == natscfg.Hostname {
				tlsConfig, err = natscfg.TLSConfig(source.Configuration["host"])
				if err != nil {
					return err
				}
				// The gateway source authenticates as the shared service user when
				// broker auth is on; an external-broker source keeps its own creds.
				user, pass = natscfg.Auth.User, natscfg.Auth.Password
			}
			mqtt, err := processor.NewMqttEventSource(source.Id, source.Configuration, tlsConfig, user, pass,
				decoder, onMessageReceived, onEventDecoded, onEventDecodeFailed, onRateAllow)
			if err != nil {
				return err
			}
			created = append(created, mqtt)
		case processor.TYPE_HTTP:
			http, err := processor.NewHttpEventSource(source.Id, source.Configuration,
				decoder, onMessageReceived, onEventDecoded, onEventDecodeFailed, onRateAllow)
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

// onRateAllow meters one inbound message against its tenant's ingest ceiling. It
// returns true when the message may proceed and false when it must be shed,
// recording the shed against the per-(source, tenant) metric so a noisy tenant is
// observable. Called at the receive point of each transport, before decode.
func onRateAllow(source string, tenant string) bool {
	if RateLimiter.Allow(tenant) {
		return true
	}
	RateLimitedCounter.WithLabelValues(source).Inc()
	// Per-tenant attribution is logged (debug) rather than carried as a metric
	// label: this service does not verify tenant existence, so a tenant label would
	// be an unbounded, attacker-influenceable cardinality vector. A safe per-tenant
	// shed metric belongs with the bounded, known-tenant registry a later slice adds.
	if log.Debug().Enabled() {
		log.Debug().Str("source", source).Str("tenant", tenant).
			Msg("Shed inbound event exceeding per-tenant ingest rate limit")
	}
	return false
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

	// Build the per-tenant ingest rate limiter from the (defaulted) configuration
	// before the sources that use it. ApplyDefaults guarantees positive rates, so
	// the limiter always meters — it is never unlimited.
	RateLimiter = core.NewTenantRateLimiter(
		Configuration.IngestRateLimit.MessagesPerSecond, Configuration.IngestRateLimit.Burst)

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
	parsed := gqlcore.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), parsed, providers, Microservice.Readiness)
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
