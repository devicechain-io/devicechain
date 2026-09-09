// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/graphql"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-device-state/processor"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
)

var (
	Microservice  *core.Microservice
	Configuration *config.DeviceStateConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	ResolvedEventsReader messaging.MessageReader
	InboundEventsWriter  messaging.MessageWriter
	StateProcessor       *processor.StateProcessor

	// StateMetrics is built ONCE, in the initialize phase, and shared by every
	// StateProcessor the NATS manager's oncreate callback builds. See buildMetrics.
	StateMetrics *core.ProcessorMetrics
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
	config := &config.DeviceStateConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// buildMetrics creates this service's Prometheus instruments exactly once.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE PROCESSOR IS BUILT.
// The processor is built in createNatsComponents, which the NATS manager invokes on
// EVERY start, and a collector belongs to the PROCESS whereas everything that callback
// builds belongs to the CONNECTION — registering one twice on this microservice's
// registry panics. Initialize is where the process's own singletons are made, which is
// what makes this the safe half; messaging.NewNatsManager carries the reasoning.
func buildMetrics() {
	StateMetrics = processor.NewStateMetrics(Microservice)
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create reader for resolved events (wildcard across tenants). This is a
	// second, independent consumer fanning out alongside event-management.
	revents, err := nmgr.NewReader(streams.ResolvedEvents)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// The service's ONE producer, and this is the only place it can be built: the
	// operator demotion mutation (ADR-067) publishes onto the shared inbound-events
	// stream, which needs an initialized NatsManager, and the Api it hangs off is
	// constructed before that manager exists.
	//
	// 🔴 IT PUBLISHES RATHER THAN WRITING THE ROW IT OWNS, deliberately. device-state
	// owns device_states and could update them in place; doing so would leave the
	// DETECT engine's presence cursor — keyed on the identical ordering predicate —
	// describing a different device from the projection. See model.DemotionEmitter.
	inbound, err := nmgr.NewWriter(streams.InboundEvents)
	if err != nil {
		return err
	}
	InboundEventsWriter = inbound
	Api.SetDemotionEmitter(model.NewDemotionEmitter(InboundEventsWriter, time.Now))

	// Add and initialize device state processor. Its instruments were built once in
	// afterMicroserviceInitialized and are handed in, because a collector belongs to
	// the process while everything this callback builds belongs to the connection, and
	// a second registration of the same collector panics.
	StateProcessor = processor.NewStateProcessor(Microservice, ResolvedEventsReader,
		core.NewNoOpLifecycleCallbacks(), Api, StateMetrics)
	err = StateProcessor.Initialize(context.Background())
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
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	err = RdbManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create RDB caches.
	model.InitializeCaches(RdbManager)

	// Wrap api around rdb manager.
	Api = model.NewApi(RdbManager)

	// Build every Prometheus instrument this service exports, before the NATS manager
	// that consumes them.
	buildMetrics()

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
	parsed := gqlcore.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, gqlcb, parsed, providers, Microservice.Readiness)
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

	// Start device state processor.
	err = StateProcessor.Start(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop device state processor.
	err := StateProcessor.Stop(ctx)
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
	// Terminate device state processor.
	err := StateProcessor.Terminate(ctx)
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
