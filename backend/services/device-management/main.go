// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/graphql"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-device-management/schema"
	esconfig "github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

var (
	Microservice  *core.Microservice
	Configuration *config.DeviceManagementConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api       *model.Api
	CachedApi *model.CachedApi

	InboundEventsReader    messaging.MessageReader
	InboundEventsProcessor *processor.InboundEventsProcessor
	ResolvedEventsWriter   messaging.MessageWriter
	FailedEventsWriter     messaging.MessageWriter

	// AlarmEvaluator consumes the resolved-events stream this service produces and
	// runs the SIMPLE alarm evaluator over resolved measurements (ADR-041). It is a
	// distinct durable consumer from the persistence/state pipelines.
	ResolvedEventsReader messaging.MessageReader
	AlarmEvaluator       *processor.AlarmEvaluator

	// CalloutResponder answers NATS auth-callout requests for device connections
	// (ADR-025). Non-nil only when the broker is configured for auth callout (the
	// issuer seed is present in the instance config).
	CalloutResponder *processor.CalloutResponder
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
	config := &config.DeviceManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create reader for inbound events (wildcard across tenants).
	ievents, err := nmgr.NewReader(esconfig.SUBJECT_INBOUND_EVENTS)
	if err != nil {
		return err
	}
	InboundEventsReader = ievents

	// Add and initialize resolved events writer.
	revents, err := nmgr.NewWriter(config.SUBJECT_RESOLVED_EVENTS)
	if err != nil {
		return err
	}
	ResolvedEventsWriter = revents

	// Add and initialize failed events writer.
	fevents, err := nmgr.NewWriter(config.SUBJECT_FAILED_EVENTS)
	if err != nil {
		return err
	}
	FailedEventsWriter = fevents

	// Add and initialize inbound events processor.
	InboundEventsProcessor = processor.NewInboundEventsProcessor(Microservice, InboundEventsReader,
		ResolvedEventsWriter, FailedEventsWriter, core.NewNoOpLifecycleCallbacks(), CachedApi, Configuration.DeviceAuthMode)
	err = InboundEventsProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}

	// Reader for the resolved-events stream (this service's own output, consumed
	// back as a distinct durable) that feeds the alarm evaluator.
	revreader, err := nmgr.NewReader(config.SUBJECT_RESOLVED_EVENTS)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revreader

	// Add and initialize the alarm evaluator (ADR-041).
	AlarmEvaluator = processor.NewAlarmEvaluator(Microservice, ResolvedEventsReader,
		core.NewNoOpLifecycleCallbacks(), CachedApi)
	err = AlarmEvaluator.Initialize(context.Background())
	if err != nil {
		return err
	}

	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Create and initialize rdb manager.
	rdbcb := core.NewNoOpLifecycleCallbacks()
	RdbManager = rdb.NewRdbManager(Microservice, rdbcb, schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	err = RdbManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create and initialize nats manager before the caches, which are backed by
	// NATS JetStream KV buckets built from it (ADR-007: NATS KV cache backend).
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	err = NatsManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create NATS KV caches TTL'd from configuration (ADR-022 review B2).
	caches, err := model.InitializeCaches(NatsManager, Configuration)
	if err != nil {
		return err
	}

	// Wrap api around rdb manager, then wrap a caching decorator over it for the
	// hot inbound-event resolution path.
	Api = model.NewApi(RdbManager)
	CachedApi = model.NewCachedApi(Api, caches)

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}

	// Create and initialize graphql manager.
	gqlcb := core.NewNoOpLifecycleCallbacks()

	schema := graphql.SchemaContent
	parsed := gqlcore.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, gqlcb, *parsed, providers, Microservice.Readiness)
	err = GraphQLManager.Initialize(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	err := RdbManager.Start(ctx)
	if err != nil {
		return err
	}

	err = GraphQLManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start nats manager.
	err = NatsManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start inbound events processor.
	err = InboundEventsProcessor.Start(ctx)
	if err != nil {
		return err
	}

	// Start the alarm evaluator.
	err = AlarmEvaluator.Start(ctx)
	if err != nil {
		return err
	}

	// Start the device auth-callout responder once the broker is configured for it
	// (ADR-025): it delegates every device connect at the broker back to
	// AuthenticateDevice. Absent an issuer seed the broker isn't running callout,
	// so there is nothing to serve.
	if seed := Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.CalloutIssuerSeed; seed != "" {
		CalloutResponder = processor.NewCalloutResponder(NatsManager.Conn(), CachedApi, seed)
		if err = CalloutResponder.Start(); err != nil {
			return err
		}
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop the auth-callout responder first (before the NATS connection closes) so
	// no in-flight device connect is left unanswered.
	if CalloutResponder != nil {
		if err := CalloutResponder.Stop(); err != nil {
			return err
		}
	}

	// Stop inbound events processor.
	err := InboundEventsProcessor.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop the alarm evaluator before the NATS connection drains.
	err = AlarmEvaluator.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop nats manager.
	err = NatsManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop graphql manager.
	err = GraphQLManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop rdb manager.
	err = RdbManager.Stop(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// Terminate inbound events processor.
	err := InboundEventsProcessor.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate the alarm evaluator.
	err = AlarmEvaluator.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate nats manager.
	err = NatsManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate graphql manager.
	err = GraphQLManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate rdb manager.
	err = RdbManager.Terminate(ctx)
	if err != nil {
		return err
	}

	return nil
}
