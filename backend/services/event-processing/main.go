// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/graphql"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-event-processing/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
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
	DeviceAttributeStore    *model.DeviceAttributeStore
	RuleRegistry            *runtime.RuleRegistry
	ResolvedEventsReader    messaging.MessageReader
	ResolvedEventsProcessor *processor.ResolvedEventsProcessor
	// ReactDispatcher is the REACT stage's derived-event consumer (ADR-051 slice 5b/5c). Since the
	// 6d cutover made raise-alarm the sole alarm path, its raise-alarm sink is always wired, so the
	// dispatcher is always started; send-command is the optional sink (nil when unconfigured).
	ReactDispatcher *processor.ReactDispatcher
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
	// Dynamic-threshold fact reader (ADR-051 slice 4c-3): the numeric, platform-set device
	// attributes a detection rule can read a threshold from. Its consumer persists each into the
	// DeviceAttribute projection before acking; the eval that reads it is slice 4c-3b-2.
	attributeReader, err := nmgr.NewReader(dmconfig.SUBJECT_DEVICE_ATTRIBUTE)
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
		PartitionId:          singletonPartition,
		Suffix:               dmconfig.SUBJECT_RESOLVED_EVENTS,
		CheckpointEvents:     Configuration.CheckpointEvents,
		CheckpointInterval:   time.Duration(Configuration.CheckpointIntervalSeconds) * time.Second,
		MaxFutureSkew:        time.Duration(Configuration.MaxEventFutureSkewSeconds) * time.Second,
		Lateness:             lateness,
		IdleAdvanceGuard:     idleGuard,
		MaxRulesPerTenant:    Configuration.MaxRulesPerTenant,
		MaxLiveKeysPerTenant: Configuration.MaxLiveKeysPerTenant,
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
	// Dynamic-threshold wiring (ADR-051 slice 4c-3): the attribute consumer persists each device-
	// attribute fact before acking, and the entity-deleted consumer purges a deleted device's
	// attributes, both via this store. The loop-owned attrView is reconciled from it at startup and
	// kept live by rechecks the consumers signal, so a rule's dynamic threshold (the CEL "attr" var)
	// resolves from the device's own attribute (slice 4c-3b-2).
	ResolvedEventsProcessor.AttributeReader = attributeReader
	ResolvedEventsProcessor.AttributeStore = DeviceAttributeStore
	if err := ResolvedEventsProcessor.Initialize(context.Background()); err != nil {
		return err
	}

	// The REACT dispatcher (ADR-051 slice 5b/5c): an INDEPENDENT durable consumer of the derived-event
	// stream this service also produces, dispatching each detection's authored actions (raise-alarm and
	// send-command). It resolves each rule's action chain from the durable rule projection by id — the
	// same projection DETECT rebuilds from — so an action edit takes effect without re-publishing events.
	// Its raise-alarm sink is always wired (the sole alarm path since 6d), so it always starts; see
	// wireReactDispatcher.
	return wireReactDispatcher(nmgr)
}

// wireReactDispatcher builds the REACT dispatcher over its action sinks:
//   - raise-alarm is ALWAYS wired (its NATS writer is always available) — since the 6d cutover it is
//     the sole alarm path (ADR-057), publishing edges to device-management's raise-alarm subject.
//   - send-command is enabled only when the shared service secret AND command-delivery's coordinate
//     are set, so a sendCommand action reaches command-delivery over the ADR-044 service-token client
//     (least-privilege command:write); it stays nil (inert) otherwise.
//
// The dispatcher and its derived-event consumer are therefore always started. The reader is
// DeliverNew so a first start on a running cluster begins at the stream head rather than replaying the
// 7-day derived-event backlog DETECT has published since slice 4a — consuming that history would flood
// stale alarm/command side effects (the first-start hazard notification-management's reader opts out of).
func wireReactDispatcher(nmgr *messaging.NatsManager) error {
	infra := Microservice.InstanceConfiguration.Infrastructure

	// send-command sink (nil ⇒ send-command disabled).
	var commands react.CommandSink
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — REACT send-command dispatch is DISABLED (ADR-051 slice 5b).")
	} else if infra.CommandDelivery.Hostname == "" || infra.CommandDelivery.Port == 0 {
		log.Warn().Msg("command-delivery endpoint not configured (infrastructure.commandDelivery) — REACT send-command dispatch is DISABLED (ADR-051 slice 5b).")
	} else {
		client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-processing", []string{string(auth.CommandWrite)})
		url := fmt.Sprintf("http://%s:%d/graphql", infra.CommandDelivery.Hostname, infra.CommandDelivery.Port)
		commands = processor.NewCommandClient(client, url)
		log.Info().Str("commandDelivery", url).Msg("REACT send-command dispatch ENABLED (ADR-051 slice 5b).")
	}

	// raise-alarm sink: a dedicated tenant-scoped writer on device-management's raise-alarm subject;
	// the thin device-management consumer folds each edge into the (device, alarmKey) alarm's
	// contributor set (ADR-057). This is the sole alarm path since the 6d cutover retired the
	// measurement-driven evaluator, so it is always wired — there is no longer a peer to double-raise
	// against. A NATS writer is always available (unlike send-command, which needs an external
	// coordinate), so raise-alarm has no disabled state.
	writer, err := nmgr.NewWriter(dmconfig.SUBJECT_RAISE_ALARM)
	if err != nil {
		return err
	}
	alarms := processor.NewAlarmClient(writer)
	log.Info().Msg("REACT raise-alarm dispatch ENABLED (ADR-051 slice 5c / ADR-057): the sole alarm path.")

	reader, err := nmgr.NewReader(config.SUBJECT_DERIVED_EVENTS, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}
	ReactDispatcher = processor.NewReactDispatcher(Microservice, reader,
		processor.NewStoreRuleResolver(DetectRuleStore), commands, alarms)
	return nil
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
	// The dynamic-threshold read-model (ADR-051 slice 4c-3): the current numeric value of each
	// platform-set device attribute, so a rule can resolve a per-device threshold from it. The
	// attribute/entity-deleted consumers maintain it before acking; slice 4c-3b-2's eval reads it.
	DeviceAttributeStore = model.NewDeviceAttributeStore(RdbManager)

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
	if err := ResolvedEventsProcessor.Start(ctx); err != nil {
		return err
	}
	// Start the REACT dispatcher last (after its reader is live) — independent of the DETECT
	// processor. Always non-nil since 6d (raise-alarm is always wired); the nil guard is defensive.
	if ReactDispatcher != nil {
		return ReactDispatcher.Start(ctx)
	}
	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop REACT first (before its reader is torn down with the NATS manager), symmetric with start.
	if ReactDispatcher != nil {
		if err := ReactDispatcher.Stop(ctx); err != nil {
			return err
		}
	}
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
