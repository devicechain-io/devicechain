// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	gql "github.com/graph-gophers/graphql-go"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-event-management/graphql"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-event-management/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventManagementConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	ResolvedEventsReader      messaging.MessageReader
	EventPersistenceProcessor *processor.EventPersistenceProcessor
	PersistedEventsWriter     messaging.MessageWriter
	FailedEventsWriter        messaging.MessageWriter
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
	config := &config.EventManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create reader for resolved events (wildcard across tenants).
	revents, err := nmgr.NewReader(dmconfig.SUBJECT_RESOLVED_EVENTS)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// Add and initialize persisted events writer.
	pevents, err := nmgr.NewWriter(config.SUBJECT_PERSISTED_EVENTS)
	if err != nil {
		return err
	}
	PersistedEventsWriter = pevents

	// Add and initialize failed events writer.
	fevents, err := nmgr.NewWriter(dmconfig.SUBJECT_FAILED_EVENTS)
	if err != nil {
		return err
	}
	FailedEventsWriter = fevents

	// Add and initialize inbound events processor.
	EventPersistenceProcessor = processor.NewEventPersistenceProcessor(Microservice, ResolvedEventsReader,
		PersistedEventsWriter, FailedEventsWriter, core.NewNoOpLifecycleCallbacks(), Api)
	err = EventPersistenceProcessor.Initialize(context.Background())
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
	RdbManager = rdb.NewRdbManager(Microservice, rdbcb, model.Migrations,
		Microservice.InstanceConfiguration.Persistence.Tsdb, Configuration.TsdbConfiguration)
	err = RdbManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create RDB caches.
	model.InitializeCaches(RdbManager)

	// Wrap api around rdb manager.
	Api = model.NewApi(RdbManager)

	// Create and initialize nats manager.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	err = NatsManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}

	// Create and initialize graphql manager.
	gqlcb := core.NewNoOpLifecycleCallbacks()

	schema := graphql.SchemaContent
	parsed := gql.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartAuthGate(ctx, func(ctx context.Context) (*auth.Validator, error) {
		return auth.FetchValidatorForInstance(ctx, Microservice.InstanceConfiguration.Infrastructure.UserManagement)
	})

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

	// Start event persistence processor.
	err = EventPersistenceProcessor.Start(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop event persistence processor.
	err := EventPersistenceProcessor.Stop(ctx)
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
	// Terminate nats manager.
	err := NatsManager.Terminate(ctx)
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
