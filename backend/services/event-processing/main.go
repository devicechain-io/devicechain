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
	DetectRuleStore         *model.DetectRuleStore
	DeviceRosterStore       *model.DeviceRosterStore
	ProfileActiveStore      *model.ProfileActiveStore
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

	// Published-rule fact propagation (ADR-051 slice 4b-3): DETECT rules are profile-homed
	// (ADR-045) and reach the engine as facts device-management emits at profile publish,
	// keyed on the profile-version token. The live durable reader feeds ongoing publishes onto
	// the processor's single-writer loop, which persists each into the durable rule projection
	// (RuleStore) before acking. The startup rule set is rebuilt from that DURABLE PROJECTION —
	// not the finite-retention fact stream — so a rule survives a restart however long ago it
	// was published; the fact stream is only the live delta transport. A re-seen fact is an
	// idempotent upsert, so any replay/live overlap is harmless.
	ruleReader, err := nmgr.NewReader(dmconfig.SUBJECT_DETECTION_RULES_PUBLISHED)
	if err != nil {
		return err
	}

	// Dead-man read-model fact readers (ADR-051 slice 4c-2b). The roster reader feeds the set of
	// devices expected to report; the entity-deleted reader (an independent consumer alongside
	// event-management's) removes a deleted device's roster entry. Each consumer persists to its
	// durable projection before acking, so the arming survives a restart independent of the
	// finite-retention fact streams. This slice lands the projections; slice 4c-2b-2 arms off them.
	rosterReader, err := nmgr.NewReader(dmconfig.SUBJECT_DEVICE_ROSTER)
	if err != nil {
		return err
	}
	entityDeletedReader, err := nmgr.NewReader(dmconfig.SUBJECT_ENTITY_DELETED)
	if err != nil {
		return err
	}
	scoped, err := processor.NewStoreRuleSource(DetectRuleStore).Load(context.Background())
	if err != nil {
		return err
	}
	RuleRegistry = runtime.NewRuleRegistry(scoped)

	// The checkpointing DETECT processor: feeds each resolved event into the owned
	// keyed-streaming engine, commits engine state to the snapshot store, and acks
	// only after the commit (ADR-051 correctness spine). On startup it replays the
	// resolved stream in order from its snapshot sequence (via the NatsManager's replay
	// reader) up to the head before consuming live, and applies live rule updates from the
	// published-rule fact reader on the same loop.
	lateness := time.Duration(Configuration.WatermarkLatenessSeconds) * time.Second
	if lateness < 0 {
		lateness = 0
	}
	// A negative guard (operator opt-out) disables idle-advance; the processor treats any
	// non-positive value as disabled, so a negative seconds value maps straight through.
	idleGuard := time.Duration(Configuration.IdleAdvanceGuardSeconds) * time.Second
	cfg := processor.Config{
		PartitionId:        singletonPartition,
		Suffix:             dmconfig.SUBJECT_RESOLVED_EVENTS,
		CheckpointEvents:   Configuration.CheckpointEvents,
		CheckpointInterval: time.Duration(Configuration.CheckpointIntervalSeconds) * time.Second,
		MaxFutureSkew:      time.Duration(Configuration.MaxEventFutureSkewSeconds) * time.Second,
		Lateness:           lateness,
		IdleAdvanceGuard:   idleGuard,
	}
	ResolvedEventsProcessor = processor.NewResolvedEventsProcessor(Microservice, ResolvedEventsReader,
		nmgr, SnapshotStore, RuleRegistry, derivedWriter, cfg, core.NewNoOpLifecycleCallbacks())
	ResolvedEventsProcessor.RuleUpdatesReader = ruleReader
	ResolvedEventsProcessor.RuleStore = DetectRuleStore
	// Dead-man read-model wiring (ADR-051 slice 4c-2b): the consumers persist the roster and
	// active-version projections before acking, then feed the engine's dead-man armer — built and
	// reconciled from these projections in ExecuteStart (slice 4c-2b-2b) — so a never-reported
	// device's absence still fires.
	ResolvedEventsProcessor.RosterReader = rosterReader
	ResolvedEventsProcessor.EntityDeletedReader = entityDeletedReader
	ResolvedEventsProcessor.RosterStore = DeviceRosterStore
	ResolvedEventsProcessor.ProfileActiveStore = ProfileActiveStore
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
	// The durable rule projection (ADR-051 slice 4b-3): the fact consumer persists published
	// rules here and the engine's rule set is rebuilt from it at startup, so rules survive a
	// restart independent of the finite-retention fact stream.
	DetectRuleStore = model.NewDetectRuleStore(RdbManager)
	// The dead-man read-models (ADR-051 slice 4c-2b): the devices expected to report and which
	// version is active per profile token (with its publish time, the grace base). The roster/
	// entity-deleted/rule consumers maintain them before acking; slice 4c-2b-2's engine arming is
	// rebuilt from them at startup, so a never-reported device's absence arming survives a restart.
	DeviceRosterStore = model.NewDeviceRosterStore(RdbManager)
	ProfileActiveStore = model.NewProfileActiveStore(RdbManager)

	// Create and initialize nats manager (builds the readers + checkpoint processor). The
	// DETECT rule set is rebuilt from the durable rule projection inside createNatsComponents
	// (ADR-051 slice 4b-3); the published-rule fact reader created there feeds live updates.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// The GraphQL surface carries the scaffold health/metrics server (/healthz, /readyz,
	// /metrics) plus the ADR-044 detection-rule validation gate (validateDetectionRules).
	// The gate compiles rules through the stateless DETECT compiler, so the resolver is
	// state-free and needs no context providers.
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
