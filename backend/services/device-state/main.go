// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/graphql"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-device-state/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/shardgroups"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

var (
	stopStreamSharding context.CancelFunc
	Microservice       *core.Microservice
	Configuration      *config.DeviceStateConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	InboundEventsWriter messaging.MessageWriter
	StateProcessors     stateProcessorGroup

	// OwnershipProcessor restamps a device's live view with its owners from
	// device-management's ownership-changed facts (ADR-081), so a device that is sold and
	// then says nothing still leaves its old owner's view.
	OwnershipReader    messaging.MessageReader
	OwnershipProcessor *processor.OwnershipProcessor
	DeletionProcessor  *processor.DeletionProcessor
	OrphanTask         *core.PeriodicTask

	// StateMetrics is built ONCE, in the initialize phase, and shared by every
	// StateProcessor the NATS manager's oncreate callback builds. See buildMetrics.
	StateMetrics *processor.StateMetrics
	// InactivitySweepMetrics is the inactivity monitor's pass signals, built once for the
	// same reason and in the same place.
	InactivitySweepMetrics *core.PeriodicTaskMetrics
	// DroppedEvicted is dropped_measurement_evicted_total, built once for the same reason.
	DroppedEvicted prometheus.Counter
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
	// Rows the per-device bound on the drop record deleted; the Api reports to it once a
	// transaction has committed (see AfterRdb, which wires the two).
	DroppedEvicted = Microservice.NewCounter("dropped_measurement_evicted_total",
		"Dropped-measurement records evicted because a device exceeded its bound of distinct (key, reason) pairs; the oldest last-seen goes first.")
	InactivitySweepMetrics = Microservice.NewPeriodicTaskMetrics("inactivity_sweep")
}

// wireDroppedEvictions points the Api's eviction report at dropped_measurement_evicted_total.
// A function of its own so a test can see the wire exists: an Api left unwired counts nothing,
// and nothing else would notice.
func wireDroppedEvictions(api *model.Api) {
	api.OnDroppedEvicted = func(n int64) { DroppedEvicted.Add(float64(n)) }
}

// newStateProcessor assembles the state processor from this service's globals: its writers
// sized by the loaded configuration, and the instruments built once in
// afterMicroserviceInitialized — handed in because a collector belongs to the process while
// everything the NATS callback builds belongs to the connection, and a second registration
// of the same collector panics. It is a function of its own so a test can check the
// configuration reaches the processor, which nothing else exercises.
func newStateProcessor(reader messaging.MessageReader, opts ...processor.StateProcessorOption) *processor.StateProcessor {
	opts = append(opts, processor.WithProjection(Configuration.Projection))
	return processor.NewStateProcessor(Microservice, reader, core.NewNoOpLifecycleCallbacks(), Api,
		StateMetrics, InactivitySweepMetrics, opts...)
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	stop, err := shardgroups.Enable(nmgr)
	if err != nil {
		return err
	}
	stopStreamSharding = stop
	// Read every resolved-events group (wildcard across tenants), independently
	// of event-management. Without sharding this keeps the legacy reader and durable.
	revents, err := shardgroups.NewReaders(nmgr, streams.ResolvedEvents)
	if err != nil {
		return err
	}

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

	// A tenant belongs to one group, preserving per-device ordering. All
	// processors share the process-scoped collectors built by buildMetrics.
	StateProcessors = make([]*processor.StateProcessor, 0, len(revents))
	for group, reader := range revents {
		var opts []processor.StateProcessorOption
		// The inactivity sweep spans all tenants, so only group 0 runs it.
		if group > 0 {
			opts = append(opts, processor.WithoutInactivityMonitor())
		}
		sp := newStateProcessor(reader, opts...)
		if err := sp.Initialize(context.Background()); err != nil {
			return err
		}
		StateProcessors = append(StateProcessors, sp)
	}

	// The device ownership-changed facts: a second, much quieter stream, consumed on its own
	// so a backlog of events never delays a device leaving its old owner's view. How a fact
	// and the device's events are ordered against each other is model/ownership.go's.
	owners, err := nmgr.NewReader(streams.DeviceOwnership)
	if err != nil {
		return err
	}
	OwnershipReader = owners
	OwnershipProcessor = newOwnershipProcessor(OwnershipReader)
	if err := OwnershipProcessor.Initialize(context.Background()); err != nil {
		return err
	}
	// The entity-deleted facts: ONE consumer for the whole service, however many
	// resolved-events groups there are. The facts are not sharded — shard placement
	// covers only inbound-events and resolved-events, and the instance config refuses any
	// other suffix — so there is one stream to read, not one per group. And a group owns a
	// device's EVENTS, not its STATE: every StateProcessor writes through the one Api into
	// the one store and keeps no per-device state of its own, so purging a device through
	// that Api purges it whichever group projected it. deletion_wiring_test.go pins both:
	// a device projected by group 1 is purged, and the config premise is checked so that
	// sharding these facts one day fails a test instead of reading only group 0's subject.
	deletions, err := nmgr.NewReader(streams.EntityDeleted)
	if err != nil {
		return err
	}
	DeletionProcessor = processor.NewDeletionProcessor(Microservice, deletions, Api, core.NewNoOpLifecycleCallbacks())
	DeletionProcessor.Hold = storehold.New(Microservice, "device-deletion", storehold.RDBProbe(RdbManager))
	return DeletionProcessor.Initialize(context.Background())
}

// newOwnershipProcessor assembles the ownership consumer from this service's globals, with the
// store hold that keeps a fact unacked through a database outage. A function of its own so the
// wiring test can build exactly what the service builds.
func newOwnershipProcessor(reader messaging.MessageReader) *processor.OwnershipProcessor {
	op := processor.NewOwnershipProcessor(Microservice, reader, Api, core.NewNoOpLifecycleCallbacks())
	op.Hold = storehold.New(Microservice, "device-ownership", storehold.RDBProbe(RdbManager))
	return op
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
			wireDroppedEvictions(Api)
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
	if err := initializeOrphanSweep(ctx); err != nil {
		return err
	}
	return nil
}

// initializeOrphanSweep builds process-owned metrics once, independently of NATS reconnects.
func initializeOrphanSweep(ctx context.Context) error {
	infra := Microservice.InstanceConfiguration.Infrastructure
	OrphanTask = nil
	if infra.ServiceAuth.Secret == "" || infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 ||
		infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Orphan sweep disabled: service secret or endpoints are not configured; degraded mode relies on deletion facts alone")
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "device-state",
		[]string{string(auth.DeviceRead), string(auth.TenantRead)})
	source := &processor.DeviceLookup{Client: client,
		DMURL:     fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port),
		UMURL:     fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port),
		ListScope: Microservice.InstanceId}
	sweep := &processor.OrphanSweep{Api: Api, Source: source,
		Deleted: Microservice.NewCounter("orphan_sweep_deleted_total", "Device state rows removed by the orphan sweep."),
		Stamped: Microservice.NewCounter("orphan_sweep_stamped_total", "Device state rows stamped with their incarnation by the orphan sweep.")}
	OrphanTask = core.NewPeriodicTask(Microservice.FunctionalArea, "orphan-sweep", processor.OrphanSweepInterval,
		sweep.Run, core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("orphan_sweep"),
		core.WithImmediateFirstPass())
	return OrphanTask.Initialize(ctx)
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Rdb, then NATS, then the GraphQL server. That order, and the reason the broker has
	// to be up before the HTTP server accepts traffic, now live in core/service.
	if err := Svc.Start(ctx); err != nil {
		return err
	}

	// Start every device state processor. It is built by the NATS manager's oncreate callback,
	// so it does not exist until the line above has run.
	if err := StateProcessors.Start(ctx); err != nil {
		return err
	}
	if err := OwnershipProcessor.Start(ctx); err != nil {
		return err
	}
	if err := DeletionProcessor.Start(ctx); err != nil {
		return err
	}
	if OrphanTask != nil {
		return OrphanTask.Start(ctx)
	}
	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if OrphanTask != nil {
		if err := OrphanTask.Stop(ctx); err != nil {
			return err
		}
	}
	if DeletionProcessor != nil {
		if err := DeletionProcessor.Stop(ctx); err != nil {
			return err
		}
	}
	// Stop all consumers before the managers: each reads from the broker and writes to the
	// database.
	if err := StateProcessors.Stop(ctx); err != nil {
		return err
	}
	if err := OwnershipProcessor.Stop(ctx); err != nil {
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
	//
	// The shard-placement watch stops AFTER Svc.Stop, not before it: cancelling it makes
	// the placement router refuse every publish, and Svc.Stop drains GraphQL first, so a
	// presence-demotion mutation still in flight would fail with ErrUnplacedTenant
	// although the broker connection is up. Cancelling a watch whose connection has
	// already drained is harmless.
	err := Svc.Stop(ctx)
	if stopStreamSharding != nil {
		stopStreamSharding()
	}
	return err
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if OrphanTask != nil {
		if err := OrphanTask.Terminate(ctx); err != nil {
			return err
		}
	}
	if DeletionProcessor != nil {
		if err := DeletionProcessor.Terminate(ctx); err != nil {
			return err
		}
	}
	// Terminate all consumers.
	if err := StateProcessors.Terminate(ctx); err != nil {
		return err
	}
	if err := OwnershipProcessor.Terminate(ctx); err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the
	// pair that actually closes handles.
	if stopStreamSharding != nil {
		stopStreamSharding()
	}
	return Svc.Terminate(ctx)
}
