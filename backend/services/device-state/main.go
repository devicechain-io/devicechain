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
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
)

var (
	Microservice  *core.Microservice
	Configuration *config.DeviceStateConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

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
	// InactivitySweepMetrics is the inactivity monitor's pass signals, built once for the
	// same reason and in the same place.
	InactivitySweepMetrics *core.PeriodicTaskMetrics
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
	InactivitySweepMetrics = Microservice.NewPeriodicTaskMetrics("inactivity_sweep")
}

// newStateProcessor assembles the state processor from this service's globals: its writers
// sized by the loaded configuration, and the instruments built once in
// afterMicroserviceInitialized — handed in because a collector belongs to the process while
// everything the NATS callback builds belongs to the connection, and a second registration
// of the same collector panics. It is a function of its own so a test can check the
// configuration reaches the processor, which nothing else exercises.
func newStateProcessor(reader messaging.MessageReader) *processor.StateProcessor {
	return processor.NewStateProcessor(Microservice, reader, core.NewNoOpLifecycleCallbacks(), Api,
		StateMetrics, InactivitySweepMetrics, processor.WithWriters(Configuration.Projection.Writers))
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

	// Add and initialize device state processor.
	StateProcessor = newStateProcessor(ResolvedEventsReader)
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

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	//
	// Left here rather than handed to core/service: services do not agree on how the gate
	// opens — most fetch it in the background like this, user-management has its own
	// validator and marks ready outright, and the ingest services open it with no auth
	// surface at all. Three answers is not a default.
	Microservice.StartInstanceAuthGate(ctx)

	// The three managers, their construction and the order of all four of their lifecycle
	// phases now live in core/service. What stays here is what is genuinely this
	// service's: which migrations, which oncreate callback, which schema, and the wiring
	// in AfterRdb that has to happen between two of the constructions.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			Migrations: model.Migrations,
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Config:     Configuration.RdbConfiguration,
		},
		// Runs once the rdb manager is initialized and before the other two are built.
		// The Api wraps that manager and the GraphQL providers below carry the Api, so
		// this is the one point in the sequence where this service has to run.
		AfterRdb: func(_ context.Context, m *service.Managers) error {
			RdbManager = m.Rdb
			model.InitializeCaches(RdbManager)
			Api = model.NewApi(RdbManager)
			// Build every Prometheus instrument this service exports, before the NATS
			// manager that consumes them.
			buildMetrics()
			return nil
		},
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
			Schema:   graphql.SchemaContent,
			Resolver: func() interface{} { return &graphql.SchemaResolver{} },
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextRdbKey: RdbManager,
					gqlcore.ContextApiKey: Api,
				}
			},
		},
	})
	if err := Svc.Initialize(ctx); err != nil {
		return err
	}
	// Published for the rest of the service, which names these managers directly.
	NatsManager, GraphQLManager = Svc.Nats, Svc.GraphQL
	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Rdb, then NATS, then the GraphQL server. That order, and the reason the broker has
	// to be up before the HTTP server accepts traffic, now live in core/service.
	if err := Svc.Start(ctx); err != nil {
		return err
	}

	// Start device state processor. It is built by the NATS manager's oncreate callback,
	// so it does not exist until the line above has run.
	err := StateProcessor.Start(ctx)
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

	// The GraphQL server stops before the NATS manager, and for this service that is not
	// merely the uniform order — it has a mutation that publishes on the caller's own
	// goroutine: DemoteAssertedPresence writes a synthetic presence event through
	// InboundEventsWriter, which is a writer on this connection. Draining the HTTP server
	// first is what keeps a demotion already in flight from failing on a connection that
	// has begun to go away, and its failure is not a tidy one — the caller is told no
	// devices in that page were demoted.
	//
	// core/service stops them in exactly that order, for every service. Pinned next door
	// in graphql_shutdown_order_test.go, which drives this function.
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// Terminate device state processor.
	err := StateProcessor.Terminate(ctx)
	if err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the
	// pair that actually closes handles.
	return Svc.Terminate(ctx)
}
