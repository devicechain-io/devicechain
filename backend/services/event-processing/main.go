// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/graphql"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-event-processing/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

// singletonPartition is the single-writer partition id for the GA deployment: one
// active DETECT engine per Instance (ADR-051). The snapshot store keys on it so a
// post-GA tenant-sharded fleet can checkpoint per shard without a schema change.
const singletonPartition = "singleton"

// DetectTermGate carries the current leadership term from the processor to the
// durables it gates. See processor.TermGate.
var DetectTermGate *processor.TermGate

var (
	Microservice  *core.Microservice
	Configuration *config.EventProcessingConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	SnapshotStore        *model.SnapshotStore
	DetectRuleStore      *model.DetectRuleStore
	RuleStatStore        *model.RuleStatStore
	Drafter              *nldraft.Drafter
	DeviceRosterStore    *model.DeviceRosterStore
	ProfileActiveStore   *model.ProfileActiveStore
	DeviceAttributeStore *model.DeviceAttributeStore
	RuleRegistry         *runtime.RuleRegistry
	// FenceSets / CurrentFenceSets / FenceManifests are the three halves of the ADR-078 fence-set
	// seam onto
	// device-management's frozen snapshot archive: the version-addressed one the replay preview
	// resolves historical fence sets through, and the current-set one the DETECT startup reconcile
	// seeds its live projection from. Both are nil when the seam is unconfigured (no service secret
	// or no device-management coordinate), which disables the projection and degrades a geofence
	// preview loudly — see buildFenceSetSeam.
	FenceSets               runtime.FenceSetSource
	CurrentFenceSets        runtime.CurrentFenceSetSource
	FenceManifests          runtime.FenceManifestResolver
	ResolvedEventsReader    messaging.MessageReader
	ResolvedEventsProcessor *processor.ResolvedEventsProcessor
	// ReactDispatcher is the REACT stage's derived-event consumer (ADR-051 slice 5b/5c). Since the
	// 6d cutover made raise-alarm the sole alarm-raise path, its raise-alarm sink is always wired, so the
	// dispatcher is always started; send-command is the optional sink (nil when unconfigured).
	ReactDispatcher *processor.ReactDispatcher
	// TenantPurgeResponder answers the ADR-077 purge coordinator's request to evict a deleted
	// tenant from the running engine. It is the only way that state can be erased — no query
	// reaches inside a checkpoint blob — so an instance running DETECT without this responder
	// is one whose tenant purges can never complete.
	TenantPurgeResponder *processor.TenantPurgeResponder

	// The Prometheus instruments this service exports — roughly thirty-five collectors
	// between them. Both are built ONCE, in the initialize phase, and shared by every
	// component the NATS manager's oncreate callback builds. See buildMetrics.
	DetectMetrics *processor.DetectMetrics
	ReactMetrics  *processor.ReactMetrics
	// EgressUnresolved counts REACT connector admissions metered at the platform default
	// for want of the tenant's own outbound ceiling. Built once, in buildMetrics.
	EgressUnresolved func(core.CeilingSource)

	// DeadLetters is this service's identity as a dead-letter producer: the source its
	// letters are stamped with and the dead_letter_lost_total their losses count on. Built by
	// core/service (Svc.DeadLetters), which the max-delivery recorder shares. See buildMetrics.
	DeadLetters *deadletter.Producer
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
// buildMetrics creates this service's Prometheus instruments exactly once.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM createNatsComponents, WHERE THE
// COMPONENTS THAT READ THEM ARE BUILT. That callback is invoked by the NATS manager on
// EVERY start, and a collector belongs to the PROCESS whereas everything that callback
// builds belongs to the CONNECTION — registering one twice on this microservice's
// registry panics. This service builds the most of any: roughly thirty-five collectors
// across DETECT and REACT, so the first duplicate takes the process down before the
// rest is wired. Initialize is where the process's own singletons are made, which is
// what makes this the safe half; messaging.NewNatsManager carries the reasoning.
func buildMetrics() {
	DetectMetrics = processor.NewDetectMetrics(Microservice)
	ReactMetrics = processor.NewReactMetrics(Microservice)
	EgressUnresolved = governance.NewUnresolvedAdmissions(Microservice, governance.Outbound)
	// core/service built it: the platform's max-delivery recorder letters under it too,
	// and a second NewProducer here would panic on the duplicate counter.
	DeadLetters = Svc.DeadLetters
}

func createNatsComponents(nmgr *messaging.NatsManager) error {
	// The leadership gate every DETECT durable below is created with (ADR-070). It is
	// built FIRST and shared, because a reader's gate predicate is fixed at creation
	// and the processor that will lead with these readers does not exist yet. Closed
	// until a term is acquired, so none of them consumes in the window between process
	// start and the first acquisition.
	DetectTermGate = processor.NewTermGate()
	gated := messaging.ReaderWithTermGate(DetectTermGate.Held)

	// Reader for resolved events (wildcard across tenants). This is a third,
	// independent consumer fanning out alongside event-management (persistence) and
	// device-state (projection) — event-processing's DETECT tap (ADR-051).
	//
	// 🔴 EVERY DETECT DURABLE IS TERM-GATED, NOT JUST THIS ONE. The fact consumers
	// persist and ack per message with no checkpoint gate of their own, and the leader
	// has no periodic reconcile for the registry, the attribute view or dead-man
	// arming — only the fence view sweeps. So a fact a zombie acks after the new
	// leader's catch-up is invisible until that leader restarts.
	revents, err := nmgr.NewReader(streams.ResolvedEvents, gated)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// Derived-event writer: DETECT publishes each detection on the per-tenant
	// "{instanceId}.{tenant}.derived-events" subject as a subscribe-able product
	// (ADR-037); the writer scopes the subject to the tenant supplied in context, which
	// the publisher's tenant backstop validates against the rule's owning tenant.
	derivedWriter, err := nmgr.NewWriter(streams.DerivedEvents)
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
	ruleReader, err := nmgr.NewReader(streams.DetectionRulesPublished, gated)
	if err != nil {
		return err
	}

	// Dead-man read-model fact readers (ADR-051 slice 4c-2b). The roster reader feeds the set of
	// devices expected to report; the entity-deleted reader (an independent consumer alongside
	// event-management's) removes a deleted device's roster entry. Each consumer persists to its
	// durable projection before acking, so the arming survives a restart independent of the
	// finite-retention fact streams. This slice lands the projections; slice 4c-2b-2 arms off them.
	rosterReader, err := nmgr.NewReader(streams.DeviceRoster, gated)
	if err != nil {
		return err
	}
	entityDeletedReader, err := nmgr.NewReader(streams.EntityDeleted, gated)
	if err != nil {
		return err
	}
	// Dynamic-threshold fact reader (ADR-051 slice 4c-3): the numeric, platform-set device
	// attributes a detection rule can read a threshold from. Its consumer persists each into the
	// DeviceAttribute projection before acking; the eval that reads it is slice 4c-3b-2.
	attributeReader, err := nmgr.NewReader(streams.DeviceAttribute, gated)
	if err != nil {
		return err
	}
	// Geofence fact reader (ADR-078): the MANIFEST of each newly-minted fence-set version —
	// which fences it holds and the content address of each one's geometry. Its consumer
	// resolves the geometry off-loop, fetching only what the compiled-geometry cache does not
	// already hold, and hands the assembled set to the single-writer loop; a location event's
	// containment predicate then resolves from memory and the loop never blocks on a read back
	// into device-management. The startup reconcile (FenceSets, below) is the other half — the
	// stream carries only changes from now on, so without it a restart would be blind.
	fenceReader, err := nmgr.NewReader(streams.GeoFenceSetManifest, gated)
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
		PartitionId:                 singletonPartition,
		Suffix:                      streams.ResolvedEvents,
		CheckpointEvents:            Configuration.CheckpointEvents,
		CheckpointInterval:          time.Duration(Configuration.CheckpointIntervalSeconds) * time.Second,
		Lateness:                    lateness,
		IdleAdvanceGuard:            idleGuard,
		MaxRulesPerTenant:           Configuration.MaxRulesPerTenant,
		MaxLiveKeysPerTenant:        Configuration.MaxLiveKeysPerTenant,
		MaxRetainedSamplesPerTenant: Configuration.MaxRetainedSamplesPerTenant,
	}
	// Its instruments were built once in afterMicroserviceInitialized and are handed in,
	// because a collector belongs to the process while everything this callback builds
	// belongs to the connection, and a second registration of the same collector panics.
	ResolvedEventsProcessor = processor.NewResolvedEventsProcessor(Microservice, ResolvedEventsReader,
		nmgr, SnapshotStore, RuleRegistry, derivedWriter, RuleStatStore, cfg,
		core.NewNoOpLifecycleCallbacks(), DetectMetrics)
	// Leadership (ADR-070): DETECT fetches, acks, checkpoints and publishes only
	// inside a held term. The chart already deploys this area as replicas:1 with
	// strategy Recreate, so on a ROLLOUT the old pod is gone before this one starts
	// and the first acquisition is immediate. What the lease adds is the case Recreate
	// cannot cover — an eviction, a node drain or a manual delete, where two pods do
	// briefly coexist and the old one's in-flight batch would otherwise redeliver, 60
	// seconds later, to a leader that has already replayed past it and will ack it away.
	detectLease, err := nmgr.NewDistributedLease(messaging.DefaultLeaseTTL)
	if err != nil {
		return err
	}
	ResolvedEventsProcessor.Lease = detectLease
	ResolvedEventsProcessor.Gate = DetectTermGate
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
	// Geofence wiring (ADR-078), and it takes BOTH halves. The fact consumer keeps the loop-owned
	// containment projection live as fences are edited; the source re-seeds it at startup from
	// device-management's durable snapshots, because an in-memory cache survives no restart and the
	// fact stream carries only changes from now on. FenceSets is nil when the cross-service seam is
	// unconfigured, which leaves the projection disabled — every containment call then reports an
	// unresolvable fence set (counted) rather than the invisible lie of "outside".
	//
	// All three halves of the archive seam go in, and they share one compiled-geometry cache:
	// the current-set source the reconcile sweep seeds from, the version-addressed source a
	// replay preview resolves through, and the manifest resolver the fact consumer hands each
	// announcement to. Geometry fetched by any one of them is already held by the others.
	ResolvedEventsProcessor.FenceSetReader = fenceReader
	ResolvedEventsProcessor.FenceSets = CurrentFenceSets
	ResolvedEventsProcessor.VersionedFenceSets = FenceSets
	ResolvedEventsProcessor.FenceManifests = FenceManifests
	if err := ResolvedEventsProcessor.Initialize(context.Background()); err != nil {
		return err
	}

	// The REACT dispatcher (ADR-051 slice 5b/5c): a separate durable consumer of the derived-event
	// stream this service also produces, consuming only while this replica holds the DETECT term (see
	// newReactReader), dispatching each detection's authored actions (raise-alarm and
	// send-command). It resolves each rule's action chain from the durable rule projection by id — the
	// same projection DETECT rebuilds from — so an action edit takes effect without re-publishing events.
	// Its raise-alarm sink is always wired (the sole alarm-raise path since 6d), so it always starts; see
	// wireReactDispatcher.
	return wireReactDispatcher(nmgr)
}

// wireReactDispatcher builds the REACT dispatcher over its action sinks:
//   - raise-alarm is ALWAYS wired (its NATS writer is always available) — since the 6d cutover it is
//     the sole alarm-raise path (ADR-057), publishing edges to device-management's raise-alarm subject.
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
	// contributor set (ADR-057). This is the sole alarm-RAISE path since the 6d cutover retired the
	// measurement-driven evaluator, so it is always wired — there is no longer a peer to double-raise
	// against. It is not the sole writer of an alarm: an operator ack/clear mutates the same row in
	// device-management, which is why the fold there runs under a CAS. A NATS writer is always available (unlike send-command, which needs an external
	// coordinate), so raise-alarm has no disabled state.
	writer, err := nmgr.NewWriter(streams.RaiseAlarm)
	if err != nil {
		return err
	}
	alarms := processor.NewAlarmClient(writer)
	log.Info().Msg("REACT raise-alarm dispatch ENABLED (ADR-051 slice 5c / ADR-057): the sole alarm-raise path.")

	// connector sink (ADR-060 §4): a dedicated tenant-scoped writer on the connector-dispatch subject;
	// the outbound-connectors service (slice C3) consumes it and executes each httpCall/publish action.
	// Like raise-alarm, a NATS writer is always available, so it is always wired — no external
	// coordinate needed. Creating the writer auto-provisions the connector-dispatch stream, so it is
	// safe to publish before the C3 consumer exists (that consumer will DeliverNew past any backlog).
	connectorWriter, err := nmgr.NewWriter(streams.ConnectorDispatch)
	if err != nil {
		return err
	}
	connectors := processor.NewConnectorClient(connectorWriter)
	log.Info().Msg("REACT connector dispatch ENABLED (ADR-060): httpCall/publish actions publish to the outbound-connectors service.")

	// SOURCE-side outbound egress cost-gate (ADR-060 SD-3): REACT charges the tenant's outbound
	// budget before publishing a connector-dispatch, dropping over-quota actions at the source so a
	// runaway rule cannot flood the connector-dispatch stream. Always non-nil (fail-open to the
	// platform default when per-tenant overrides are not wired) — never unlimited.
	connectorRate := buildEgressLimiter(Configuration, Microservice.InstanceConfiguration.Infrastructure, EgressUnresolved)

	reader, err := newReactReader(nmgr)
	if err != nil {
		return err
	}
	// The ADR-024 arm. A detection whose actions cannot be dispatched used to end as a log
	// line and a counter; it is now written where it can be inspected. The writer is built
	// here rather than inside the dispatcher so a deployment that could not create it
	// fails at startup, next to every other stream this service needs, rather than at the
	// first failure — which is the one moment the arm has to work.
	deadWriter, err := nmgr.NewWriter(streams.DeadLetters)
	if err != nil {
		return err
	}
	ReactDispatcher = processor.NewReactDispatcher(Microservice, reader,
		processor.NewStoreRuleResolver(DetectRuleStore), commands, alarms, connectors, connectorRate,
		DeadLetters.NewSink(deadWriter), processor.ShedLetterBudget{
			PerTenantPerSecond: Configuration.ShedLetterPerSecond,
			PerTenantBurst:     Configuration.ShedLetterBurst,
			GlobalPerSecond:    Configuration.ShedLetterGlobalPerSecond,
			GlobalBurst:        Configuration.ShedLetterGlobalBurst,
		}, ReactMetrics)
	return nil
}

// newReactReader is the REACT derived-events reader: term-gated on the DETECT lease and
// releasing its buffer when the term is lost.
//
// 🔑 REACT IS GATED FOR THE CEILING, NOT FOR SINGLE-WRITER SAFETY. It holds no ordered
// state, and every replica could dispatch correctly on its own. What it does hold is
// connectorRate — the source-side outbound ceiling, which is in memory per process. With
// every replica consuming, each replica charged its own copy of every tenant's ceiling, so a
// warm standby doubled it. Gating on the DETECT lease makes the replica that detects the only
// one that reacts, so the ceiling is charged once. During a lease handover both replicas can
// dispatch for up to about five seconds (the old owner's Held overshoots the server-side
// expiry by up to the JetStream API timeout; see termSlack in processor/leadership.go).
//
// The gate is DetectTermGate.Held — a method value on the pointer createNatsComponents
// assigns before wireReactDispatcher runs, and late-bound through the TermGate's atomic, so
// a reader built at process start gates correctly on every term acquired after it.
//
// ReaderWithReleaseOnPark gives the buffer up when the term is lost. DETECT keeps its
// buffer across a flicker of Held because dropping it would reorder a single writer's input;
// REACT has no order to protect, and a buffer it kept would be handed out on a later term
// after the other replica had already dispatched it. It narrows that window rather than
// closing it: ReaderWithReleaseOnPark names the subscription-buffered leftovers it cannot
// reach, which is one more reason a connector call can reach its destination twice.
//
// REACT is deliberately NOT one of the processor's termReaders (bound and unbound per term).
// Unbinding protects DETECT from a pull request served past the new leader's replay head,
// which for DETECT is loss; for REACT the same event only delays a dispatch. Binding REACT
// per term would also put its bind failures on the DETECT term-build fuse.
func newReactReader(nmgr *messaging.NatsManager) (messaging.MessageReader, error) {
	return nmgr.NewReader(streams.DerivedEvents, messaging.ReaderWithDeliverNew(),
		messaging.ReaderWithTermGate(DetectTermGate.Held), messaging.ReaderWithReleaseOnPark())
}

// buildEgressLimiter constructs the per-tenant SOURCE-side OUTBOUND egress cost-gate (ADR-060 SD-3).
// When the shared service secret and user-management endpoint are configured, per-tenant outbound
// overrides are fetched from user-management over a service token (least-privilege tenant:read — a
// SEPARATE, narrower scope than the command:write token the send-command sink uses) and cached,
// failing open to the platform default; otherwise every tenant is metered at the platform default.
// Either way the ceiling is a real limit — never unlimited — since ApplyDefaults/Validate guarantee a
// positive platform default. Mirrors outbound-connectors' buildEgressLimiter; the source shed
// here and that service's bounded egress wait charge the SAME outbound dimension, on the SAME
// trigger time (core.MeteringTime), at both ends.
//
// unresolved counts admissions made at the platform default for want of a tenant's own ceiling
// (governance.NewUnresolvedAdmissions). It is built once per process, in buildMetrics, because
// this limiter is rebuilt on every NATS start and a collector registered twice panics. Every
// tenant here comes from the platform's own derived-events stream, so it always gets an
// allowance of its own.
func buildEgressLimiter(cfg *config.EventProcessingConfiguration, infra mscfg.InfrastructureConfiguration,
	unresolved func(core.CeilingSource)) *core.TenantRateLimiter {
	def := governance.Limits{
		MessagesPerSecond: cfg.OutboundMessagesPerSecond,
		Burst:             cfg.OutboundBurst,
	}
	counted := core.WithUnresolvedAdmissions(unresolved)
	if infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Service secret or user-management endpoint not configured — per-tenant outbound overrides disabled; metering every tenant at the platform default (ADR-060 SD-3).")
		return core.NewTenantRateLimiter(core.StaticCeiling(def.MessagesPerSecond, def.Burst), counted)
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-processing", []string{string(auth.TenantRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	resolver := governance.NewServiceLimitResolver(client, umURL, def, governance.Outbound)
	log.Info().Str("userManagement", umURL).Msg("REACT source-side per-tenant outbound overrides enabled (fail-open to platform default, ADR-060 SD-3).")
	return core.NewTenantRateLimiter(resolver.Ceiling, counted)
}

// buildFenceSetSeam constructs the ADR-078 fence-set fetch seam onto device-management's frozen
// snapshot archive, over a service-token client carrying least-privilege device:read — the same
// authority the geofence CRUD reads take, because it is the same material (a fence's geometry is
// where a tenant's sites are). The tenant is never a query argument: it rides the service token's
// tenant header and device-management's rows are tenant-scoped, so one tenant's fence set is not
// reachable through a request made for another.
//
// It is enabled only when the shared service secret AND device-management's coordinate are set;
// otherwise both halves stay nil, which disables the live containment projection and degrades a
// geofence preview. That is the fail-closed direction: with no seam every containment call reports
// an unresolvable fence set and is COUNTED, where a projection quietly seeded with nothing would
// answer "outside" and read as a healthy rule that never fires.
func buildFenceSetSeam() (runtime.FenceSetSource, runtime.CurrentFenceSetSource, runtime.FenceManifestResolver) {
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — geofence evaluation is UNAVAILABLE (ADR-078): containment reports unresolvable rather than 'outside'.")
		return nil, nil, nil
	}
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		log.Warn().Msg("device-management endpoint not configured (infrastructure.deviceManagement) — geofence evaluation is UNAVAILABLE (ADR-078).")
		return nil, nil, nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-processing", []string{string(auth.DeviceRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
	log.Info().Str("deviceManagement", url).Msg("Geofence fence-set fetch ENABLED (ADR-078): the startup reconcile and the replay preview resolve frozen fence sets from device-management.")
	// One cache, shared by all three seams the client satisfies. Its bound is counted in
	// VERTICES rather than entries, because a three-vertex box and a five-hundred-vertex
	// polygon are one entry each and differ by ~500x in what they cost to hold.
	return processor.NewFenceSetClient(Microservice, client, url,
		geofence.NewGeometryCache(geofence.DefaultMaxCachedVertices))
}

// buildDrafter constructs the ADR-056 NL→rule drafting orchestrator (slice 1). It wires the
// bounded infer→compile→repair loop over an ai-inference service-token client (least-privilege
// ai:infer — a SEPARATE, narrower scope than the command:write / tenant:read tokens the other
// seams use). It is enabled only when the shared service secret AND ai-inference's coordinate are
// set; otherwise it returns nil and the draft door reports "unavailable" fail-closed (the form +
// canvas authoring doors are unaffected). ai-inference is an opt-in area, so nil is the common
// case. The candidate it returns is compiled through this service's OWN rules.Compile firewall at
// the platform-default limits — the AI proposes, the deterministic compiler disposes.
func buildDrafter() *nldraft.Drafter {
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — NL→rule drafting is UNAVAILABLE (ADR-056 slice 1).")
		return nil
	}
	if infra.AiInference.Hostname == "" || infra.AiInference.Port == 0 {
		log.Warn().Msg("ai-inference endpoint not configured (infrastructure.aiInference) — NL→rule drafting is UNAVAILABLE (ADR-056 slice 1).")
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-processing", []string{string(auth.AIInfer)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.AiInference.Hostname, infra.AiInference.Port)
	log.Info().Str("aiInference", url).Msg("NL→rule drafting ENABLED (ADR-056 slice 1): candidates compile through the DETECT firewall at the platform-default limits.")
	return nldraft.NewDrafter(processor.NewInferenceClient(client, url), rules.DefaultLimits(), 0)
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}
	// Install the operator-configured rule-duration ceiling into rules.DefaultLimits BEFORE
	// anything can compile a rule — the GraphQL publish gate is not serving yet and the fact
	// consumer has not bound — so the publish gate and the runtime fact consumer are guaranteed
	// to enforce the same ceiling. ApplyDefaults has already floored an unset value to the
	// platform default, so this is never a zero that would silently mean "unlimited".
	rules.SetPlatformMaxRuleDuration(time.Duration(Configuration.MaxRuleDurationSeconds) * time.Second)

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness.
	//
	// Left here rather than handed to core/service: services do not agree on how the gate
	// opens — most fetch it in the background like this, user-management has its own
	// validator and marks ready outright, and the ingest services open it with no auth
	// surface at all. Three answers is not a default.
	Microservice.StartInstanceAuthGate(ctx)

	// The three managers, their construction and the order of all four of their lifecycle
	// phases now live in core/service. What stays here is what is genuinely this service's:
	// which store, which migrations, which oncreate callback, which schema, and the wiring
	// in AfterRdb that has to happen between two of the constructions.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			// This chain includes the snapshot-store migrations, which the manager runs
			// under the startup advisory lock.
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Migrations: model.Migrations,
			Config:     Configuration.RdbConfiguration,
		},
		// Runs once the rdb manager is initialized and before the other two are built.
		// Every store below wraps that manager, and both the NATS oncreate callback and
		// the GraphQL resolver read them — which is what makes this the one point in the
		// sequence where this service has to run.
		AfterRdb: func(_ context.Context, m *service.Managers) error {
			RdbManager = m.Rdb

			SnapshotStore = model.NewSnapshotStore(RdbManager)
			// The durable rule projection (ADR-051 slice 4b-3): the fact consumer persists published
			// rules here and the engine's rule set is rebuilt from it at startup, so rules survive a
			// restart independent of the finite-retention fact stream.
			DetectRuleStore = model.NewDetectRuleStore(RdbManager)
			RuleStatStore = model.NewRuleStatStore(RdbManager)
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

			// Build the ADR-078 fence-set fetch seam BEFORE the NATS manager: createNatsComponents wires
			// the current-set half into the processor, whose startup reconcile seeds the containment
			// projection from it, so it must exist by then.
			FenceSets, CurrentFenceSets, FenceManifests = buildFenceSetSeam()

			// buildDrafter wires the ADR-056 NL→rule drafting seam (nil ⇒ the draft door reports
			// unavailable, fail-closed). It is built here so the resolver below can carry it
			// alongside the stores.
			Drafter = buildDrafter()

			// Build every Prometheus instrument this service exports, before the NATS manager
			// that consumes them.
			buildMetrics()
			return nil
		},
		// createNatsComponents builds the readers + checkpoint processor. The DETECT rule set
		// is rebuilt from the durable rule projection inside it (ADR-051 slice 4b-3); the
		// published-rule fact reader created there feeds live updates.
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
			// The GraphQL surface carries the scaffold health/metrics server (/healthz, /readyz,
			// /metrics), the ADR-044 detection-rule validation gate (validateDetectionRules — pure,
			// compiles through the stateless DETECT compiler), and the slice-7b rule-health read
			// (ruleHealth), which reads the durable rule + firing projections — so the resolver carries
			// their stores. Auth/tenant ride the request context.
			//
			// 🔴 THE RESOLVER IS BUILT IN A FUNCTION BECAUSE ITS STORES DO NOT EXIST YET. Every
			// field below is made in AfterRdb, which has not run when this Spec literal is
			// written; a resolver built here as a value would carry six nil stores into a
			// server that compiles, starts and serves, and fails at the first ruleHealth query.
			Schema: graphql.SchemaContent,
			Resolver: func() interface{} {
				return &graphql.SchemaResolver{
					DetectRules: DetectRuleStore,
					RuleStats:   RuleStatStore,
					Profiles:    ProfileActiveStore,
					Drafter:     Drafter,
					// The replay preview resolves each replayed event's STAMPED fence-set version through
					// this seam (ADR-078), so previewing a geofence rule over last week evaluates against the
					// fences that were live then. It is off the DETECT loop and may block, which is why the
					// preview holds it and the fan-out does not.
					FenceSets: FenceSets,
				}
			},
			// The NATS manager is injected as a provider so the slice-7c detectionStream
			// subscription can tap the tenant's derived-event feed (SubscribeLive).
			//
			// 🔴 IT READS Svc.Nats RATHER THAN THE PACKAGE VARIABLE, which is still nil at
			// this moment: NatsManager is published below, after Initialize returns, and this
			// runs inside it. The manager is already connected by then — the broker is built
			// and initialized before the GraphQL server — so the subscription server never
			// accepts a client ahead of it.
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextNatsKey: Svc.Nats,
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
	// Rdb, then NATS, then the GraphQL server. That order, and the reason the broker has to
	// be up before the HTTP server accepts traffic, now live in core/service. The relational
	// manager going first also matters locally: the processor restores engine state from the
	// snapshot store at Start, so the store must be live before the processor starts.
	if err := Svc.Start(ctx); err != nil {
		return err
	}
	if err := ResolvedEventsProcessor.Start(ctx); err != nil {
		return err
	}
	// AFTER the processor, never before, and the reason has changed shape now that Start no
	// longer blocks on the first leadership term.
	//
	// It is no longer a claim that the loop is running when the responder appears — with a
	// warm standby (ADR-070) it usually is not, and the responder answers that honestly
	// (processor.errNoTermHeld). What the ordering still guarantees is that the processor's
	// FIELDS are wired: EvictTenant reads rp.procCtx and sends on rp.tenantPurges, and a
	// request that arrived before Initialize/Start had run would select on a nil context and
	// take the process down. Initialize mints the context and the constructor makes the
	// channel, both of which are done by here.
	//
	// It also still means a request cannot reach a replica whose GraphQL and NATS managers
	// are not up, which is what makes the answer — refusal or eviction — a considered one
	// rather than an artefact of half-built wiring.
	TenantPurgeResponder = processor.NewTenantPurgeResponder(NatsManager.Conn(),
		Microservice.InstanceId, ResolvedEventsProcessor)
	if err := TenantPurgeResponder.Start(); err != nil {
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
	// Unsubscribe the eviction responder first: past this point the loop is stopping, so an
	// accepted request could only be answered with a failure.
	//
	// 🔑 THAT IS DELIBERATELY ASYMMETRIC WITH START, not symmetric with it. The responder is
	// started BEFORE the REACT dispatcher and stopped BEFORE it as well, so a mirror of the
	// start order would stop it second. The reason above is what decides it, and it outranks
	// the mirror: the window in which the responder can accept work it cannot finish is the
	// thing being closed, and that window opens the moment shutdown begins.
	if TenantPurgeResponder != nil {
		if err := TenantPurgeResponder.Stop(); err != nil {
			return err
		}
	}
	// Stop REACT first (before its reader is torn down with the NATS manager), symmetric with start.
	if ReactDispatcher != nil {
		if err := ReactDispatcher.Stop(ctx); err != nil {
			return err
		}
	}
	if err := ResolvedEventsProcessor.Stop(ctx); err != nil {
		return err
	}
	// GraphQL, then NATS, then Rdb. The detections subscription reads
	// the tenant's derived-event stream over this connection for as long as a socket is
	// open, and GraphQLManager's stop is what closes those sockets — so closing them first
	// cancels each subscription's context while its broker subscription is still live, and
	// the feed unwinds from the top.
	//
	// 🔴 THE REVERSE ORDER REPORTS NOTHING AT ALL, WHICH IS THE REASON TO ORDER IT. Draining
	// the connection first removes the subscription, and nats.go's removeSub closes a
	// subscription's delivery channel only for a SyncSubscription; SubscribeLive is built on
	// ChanSubscribe, so the channel is set to nil and never closed. Its forwarding goroutine
	// stays parked, the channel it feeds is never closed, and the resolver's range over it
	// simply blocks — with no error anywhere, since the manager sets its shutting-down flag
	// before Drain and the ClosedHandler then logs an expected shutdown. The subscriber gets
	// neither data nor a close until the GraphQL stop drops its socket: a silent stall for
	// the length of the teardown, not a fault anyone can see.
	//
	// The DETECT partition lease is unaffected either way: the processor's stop above waits
	// for the term to end, which flushes the final checkpoint and releases the lease, and
	// both of those happen before either of these two lines runs.
	// core/service walks them in exactly that order, for every service. Pinned next door in
	// graphql_shutdown_order_test.go, which drives this function.
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := ResolvedEventsProcessor.Terminate(ctx); err != nil {
		return err
	}
	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the pair
	// that actually closes handles, and which the processor above still precedes.
	return Svc.Terminate(ctx)
}
