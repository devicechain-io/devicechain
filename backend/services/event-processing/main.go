// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"time"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/graphql"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-event-processing/processor"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// singletonPartition is the single-writer partition id for the GA deployment: one
// active DETECT engine per Instance (ADR-051). The snapshot store keys on it so a
// post-GA tenant-sharded fleet can checkpoint per shard without a schema change.
const singletonPartition = "singleton"

var (
	Microservice  *core.Microservice
	Configuration *config.EventProcessingConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	SnapshotStore           *model.SnapshotStore
	RuleRegistry            *runtime.RuleRegistry
	ResolvedEventsReader    messaging.MessageReader
	ResolvedEventsProcessor *processor.ResolvedEventsProcessor
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
	cfg := &config.EventProcessingConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg)
	if err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Reader for resolved events (wildcard across tenants). This is a third,
	// independent consumer fanning out alongside event-management (persistence) and
	// device-state (projection) — event-processing's DETECT tap (ADR-051).
	revents, err := nmgr.NewReader(dmconfig.SUBJECT_RESOLVED_EVENTS)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// Derived-event writer: DETECT publishes each detection on the per-tenant
	// "{instanceId}.{tenant}.derived-events" subject as a subscribe-able product
	// (ADR-037); the writer scopes the subject to the tenant supplied in context, which
	// the publisher's tenant backstop validates against the rule's owning tenant.
	derivedWriter, err := nmgr.NewWriter(config.SUBJECT_DERIVED_EVENTS)
	if err != nil {
		return err
	}

	// The checkpointing DETECT processor: feeds each resolved event into the owned
	// keyed-streaming engine, commits engine state to the snapshot store, and acks
	// only after the commit (ADR-051 correctness spine). On startup it replays the
	// stream in order from its snapshot sequence (via the NatsManager's replay reader)
	// up to the head before consuming live. It runs with an empty rule set in this
	// slice — multi-tenant rule CRUD arrives later — so it advances the watermark and
	// checkpoints its position without emitting detections yet.
	lateness := time.Duration(Configuration.WatermarkLatenessSeconds) * time.Second
	if lateness < 0 {
		lateness = 0
	}
	cfg := processor.Config{
		PartitionId:        singletonPartition,
		Suffix:             dmconfig.SUBJECT_RESOLVED_EVENTS,
		CheckpointEvents:   Configuration.CheckpointEvents,
		CheckpointInterval: time.Duration(Configuration.CheckpointIntervalSeconds) * time.Second,
		MaxFutureSkew:      time.Duration(Configuration.MaxEventFutureSkewSeconds) * time.Second,
		Lateness:           lateness,
	}
	ResolvedEventsProcessor = processor.NewResolvedEventsProcessor(Microservice, ResolvedEventsReader,
		nmgr, SnapshotStore, RuleRegistry, derivedWriter, cfg, core.NewNoOpLifecycleCallbacks())
	return ResolvedEventsProcessor.Initialize(context.Background())
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize the rdb manager (runs the snapshot-store migrations under
	// the startup advisory lock). It must be initialized before the NATS manager so
	// the snapshot store exists when the processor is constructed below.
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), model.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}
	SnapshotStore = model.NewSnapshotStore(RdbManager)

	// Build the rule registry from the rule source. Slice 4a is backed by an empty static
	// source — the runtime fan-out, derived-event publisher, and tenant backstop are wired
	// and exercised by tests, but no rules are authored yet. Slice 4b replaces the source
	// with one fed by device-management's published-rule fact events (profile-homed rules,
	// ADR-045), scoped by profile-version token.
	scoped, err := runtime.StaticRuleSource{}.Load(ctx)
	if err != nil {
		return err
	}
	RuleRegistry = runtime.NewRuleRegistry(scoped)

	// Create and initialize nats manager (builds the reader + checkpoint processor).
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// The scaffold GraphQL surface exists only to stand up the shared health/metrics
	// server (/healthz, /readyz, /metrics); the rule surface arrives later. No
	// context providers are needed yet (the resolver is state-free).
	providers := map[gqlcore.ContextKey]interface{}{}
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness.
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		parsed, providers, Microservice.Readiness)
	return GraphQLManager.Initialize(ctx)
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Start the rdb manager first: the processor restores engine state from the
	// snapshot store at Start, so the store must be live before the processor starts.
	if err := RdbManager.Start(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	return ResolvedEventsProcessor.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := ResolvedEventsProcessor.Stop(ctx); err != nil {
		return err
	}
	if err := NatsManager.Stop(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
	}
	return RdbManager.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := ResolvedEventsProcessor.Terminate(ctx); err != nil {
		return err
	}
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}
