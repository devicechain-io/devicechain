// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// readErrorBackoff caps the retry rate after a non-EOF read error the reader's own
// self-heal does not resolve, so a persistent instant error cannot hot-spin.
const readErrorBackoff = 200 * time.Millisecond

// maxRulePersistBackoff caps the backoff of the in-process retry that persists a published
// rule to the durable projection. The retry runs until the write succeeds (or shutdown), so a
// DB outage does not lose the rule to the broker's finite redelivery (MaxDeliver) — rule facts
// are rare and cross-fact ordering is irrelevant (unique version tokens), so blocking this
// goroutine while the store recovers is safe.
const maxRulePersistBackoff = 30 * time.Second

// ReplayOpener opens a bounded, ordered replay of a stream from a start sequence
// (satisfied by *messaging.NatsManager). Narrowed to an interface so the checkpoint
// loop can be tested without a broker.
type ReplayOpener interface {
	NewReplayReader(suffix string, startSeq uint64) (messaging.ReplayReader, uint64, error)
}

// Config tunes the checkpoint loop (ADR-051 correctness spine).
type Config struct {
	// PartitionId is the single-writer partition this loop owns; GA runs one active
	// DETECT per Instance, so one partition.
	PartitionId string
	// Suffix is the resolved-events subject suffix, used to open the replay reader on
	// the same stream the live durable reader consumes.
	Suffix string
	// CheckpointEvents is the max applied events between snapshot commits.
	CheckpointEvents int
	// CheckpointInterval is the max wall-clock time between commits, so a quiet
	// stream still checkpoints (and its buffered acks are released).
	CheckpointInterval time.Duration
	// TickInterval is how often the live loop wakes to honor the checkpoint interval
	// on a quiet stream. Defaults to one second.
	TickInterval time.Duration
	// Lateness bounds how far event time is held back before advancing the watermark
	// (out-of-orderness tolerance).
	Lateness time.Duration
	// MaxFutureSkew bounds how far a device-reported occurred time may lead the server's
	// processed time before it is clamped, so one bad device clock cannot advance the shared
	// watermark far into the future. Non-positive disables the clamp.
	MaxFutureSkew time.Duration
	// IdleAdvanceGuard is how long the read loop must be quiet (no delivery) before the loop
	// tests broker emptiness and, if caught up, advances the watermark off the wall clock so a
	// silent series' absence/duration/session timer fires (ADR-051 slice 4c). It is one of
	// several gates — quiet-for-guard drains the reader's local fetch buffer; the durable's zero
	// pending + ack-pending backlog is the authoritative caught-up signal. Non-positive disables
	// idle-advance. See idleAdvance.
	IdleAdvanceGuard time.Duration
	// Clock is the wall clock used for checkpoint-interval timing; defaults to the
	// real clock.
	Clock detectcore.Clock
	// MaxRulesPerTenant and MaxLiveKeysPerTenant are the per-tenant runtime state budget ceilings
	// (ADR-023 amendment, ADR-051 slice 6c). Slice 6c-1 measures usage against them at each checkpoint
	// and exposes bounded gauges (a count of over-budget tenants, not a per-tenant label); slice 6c-2
	// enforces them. Non-positive means unmeasured (the tests' default); the service always supplies
	// the fail-safe platform ceiling from config.
	MaxRulesPerTenant    int
	MaxLiveKeysPerTenant int
}

// ResolvedEventsProcessor is event-processing's single-writer tap on the
// resolved-events stream (ADR-051). It feeds each resolved event into the owned
// keyed-streaming DETECT engine, periodically commits the engine's state to the
// Postgres snapshot store, and acks JetStream only AFTER that commit — the
// ack-on-checkpoint contract that makes restart-and-replay correct rather than
// false-or-missed.
//
// The correctness spine is a two-phase startup:
//   - RESTORE: rebuild the engine from the last committed snapshot.
//   - REPLAY: read the stream IN ORDER from the snapshot sequence up to the head
//     captured at startup, feeding the engine, BEFORE any live consumption. This is
//     essential: a durable consumer's post-crash redelivery is not ordered (an
//     AckWait-held message can be overtaken by a newer one), which would let the
//     engine's idempotent guard permanently discard a never-applied event. Replaying
//     by sequence also re-reads any message that exhausted MaxDeliver on the durable,
//     so a checkpoint outage plus a crash cannot silently drop events.
//
// Only after replay reaches the head does the live loop run, consuming the durable
// reader. Redelivered duplicates (seq <= the replayed head) are dropped by the
// engine's guard and acked; new events advance it.
//
// Single-writer discipline: all engine mutation AND snapshotting happen on one
// goroutine (replay runs on the startup goroutine before the live loop launches;
// thereafter only the live loop touches the engine), so the engine needs no lock.
//
// The loop feeds each resolved event through the rule fan-out (runtime.Plan): it selects
// the rules the event applies to (by tenant + profile version), builds the metric-scoped
// per-rule events, and hands them to the engine as one message-sequenced batch. Emitted
// detections are drained onto a buffer that the next checkpoint DELIVERS as subscribe-able
// derived events BEFORE it commits/acks past the producing message (deliver-before-
// checkpoint), enforcing the runtime tenant backstop at the publish boundary. With no rules
// (the scaffold path) the loop still advances the watermark and checkpoints its position,
// emitting nothing. When the consumer is caught up to the live tail the ticker also drives
// wall-clock idle advance (idleAdvance), which fires a silent series' absence/duration/session
// timer that no later event would otherwise trigger — gated on BROKER-CONFIRMED emptiness (the
// durable's pending + ack-pending backlog is zero), not read-loop silence, so a restored timer
// never fires ahead of an unconsumed backlog during an outage, re-bind, or rolling-update overlap.
type ResolvedEventsProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	Replay               ReplayOpener
	Store                *model.SnapshotStore

	// RuleUpdatesReader is the durable consumer of device-management's published-rule fact
	// stream (ADR-051 slice 4b-3). When set, a goroutine drains it, persists each fact's rules
	// to the durable projection (RuleStore) and marshals the compiled rules onto the
	// single-writer loop as a ruleUpdate, so live rule changes are durable AND never race the
	// fan-out. Nil disables live rule updates (the scaffold/test path); the startup rule set
	// still comes from the registry.
	RuleUpdatesReader messaging.MessageReader

	// RuleStore is the durable rule projection the fact consumer persists to before acking a
	// fact (ADR-051 slice 4b-3), so the rule set survives a restart independent of the
	// finite-retention fact stream. Nil disables persistence (the scaffold/test path).
	RuleStore *model.DetectRuleStore

	// RosterReader is the durable consumer of device-management's device-roster fact stream
	// (ADR-051 slice 4c-2b). When set, a goroutine drains it, persists each fact to the DeviceRoster
	// projection (RosterStore) before acking, and marshals the device's authoritative membership onto
	// the single-writer loop so the armer arms its dead-man (slice 4c-2b-2b). Nil disables it (the
	// scaffold/test path).
	RosterReader messaging.MessageReader

	// EntityDeletedReader is the durable consumer of device-management's entity-deleted fact
	// stream (ADR-044). When set, a goroutine drains it and, for a DELETED DEVICE, removes the
	// device's roster row (RosterStore.Delete) so its dead-man arming is cancelled rather than
	// firing a false absence forever. Non-device deletions are acked and ignored. Nil disables it.
	EntityDeletedReader messaging.MessageReader

	// RosterStore is the durable roster projection the roster/entity-deleted consumers maintain
	// before acking (ADR-051 slice 4c-2b), so the dead-man arming survives a restart independent
	// of the finite-retention fact streams. Nil disables persistence (the scaffold/test path).
	RosterStore *model.DeviceRosterStore

	// ProfileActiveStore is the durable "active published version per profile token" projection
	// (ADR-051 slice 4c-2b). The published-rule fact consumer maintains it alongside the rule
	// projection (last-fact-wins), so the arming path can resolve a roster entry's stable profile
	// token to the active version's absence rules and their grace base. Nil disables it.
	ProfileActiveStore *model.ProfileActiveStore

	// AttributeReader is the durable consumer of device-management's device-attribute fact stream
	// (ADR-051 slice 4c-3). When set, a goroutine drains it and persists each numeric, platform-set
	// attribute (or its removal) to the DeviceAttribute projection (AttributeStore) before acking, so
	// a detection rule can resolve a dynamic threshold from a device's own attribute (the CEL "attr"
	// var, wired in slice 4c-3b-2). Nil disables it (the scaffold/test path).
	AttributeReader messaging.MessageReader

	// AttributeStore is the durable device-attribute projection the attribute/entity-deleted
	// consumers maintain before acking (ADR-051 slice 4c-3), so a dynamic threshold's source value
	// survives a restart independent of the finite-retention fact stream. The entity-deleted consumer
	// also purges a deleted device's attributes and drops its resurrection fence here. When set, the
	// loop-owned attrView is reconciled from it at startup and re-reads it per-device on a recheck
	// (slice 4c-3b-2). Nil disables persistence + the dynamic-threshold view (the scaffold/test path).
	AttributeStore *model.DeviceAttributeStore

	// armer arms dead-man absence timers for never-seen devices, cross-referencing the roster +
	// active-version read-models (ADR-051 slice 4c-2b-2b). It is built in ExecuteStart once the
	// engine is final (after replay), reconciled from the durable projections, then driven live on
	// the single-writer loop by the fact consumers (armUpdates / applyRuleUpdate). Nil on the
	// scaffold/test path (no stores), where all arming is skipped.
	armer *runtime.DeadmanArmer
	// armUpdates carries one device's authoritative post-merge membership (from the roster /
	// entity-deleted consumers, after they re-read the projection) to the single-writer loop, where
	// the armer arms or disarms the device. Buffered so a burst of device facts does not block the
	// consumers on the loop between resolved-event batches.
	armUpdates chan armUpdate
	// armRetries holds device arming rechecks whose RosterStore.Load failed transiently
	// (applyArmRecheck), to be re-attempted on the ticker (retryArmRechecks) — the exact mirror of
	// attrRetries. It closes the one-DB-blip window in which the driving fact is already
	// persisted-and-acked but the armer kept stale membership: the argument is STRICTER than the
	// attribute case, because the fact that drove the recheck is often the device's LAST fact ever
	// (an entity-deleted tombstone), so there is no "next fact" to heal it — an un-retried drop leaves
	// a deleted device dead-man-armed (a published FALSE absence) or a created device never armed (a
	// missed absence) until the next restart. Loop-owned (only applyArmRecheck / retryArmRechecks
	// touch it); bounded by distinct devices, so an outage cannot grow it with event volume.
	armRetries map[armUpdate]struct{}

	// attrView is the loop-owned in-memory dynamic-threshold projection (ADR-051 slice 4c-3b-2): each
	// device's flattened SERVER-over-SHARED attribute map, which the fan-out binds as predicate.Input.
	// Attr so a rule's dynamic threshold resolves from the device's own attribute. Built + reconciled
	// from AttributeStore in ExecuteStart (parallel to the dead-man armer), then kept live on the
	// single-writer loop by rechecks the attribute + entity-deleted consumers signal. Nil on the
	// scaffold/test path (no AttributeStore), where every dynamic threshold reads an empty map (a
	// presence-guarded clean non-match).
	attrView *runtime.DeviceAttributeView
	// attrUpdates carries a device-identity RECHECK (NOT a value delta) from the attribute /
	// entity-deleted consumers to the single-writer loop, where applyAttrRecheck re-reads the
	// authoritative projection for that device and REPLACES its view entry. Same convergence
	// discipline as armUpdates: the loop reads the monotonically-converged projection, so the two
	// consumers cannot install a stale value out of lifecycle order. Buffered generously (attribute
	// facts can burst on a fleet config push); the loop drains one per resolved-event-batch iteration.
	attrUpdates chan attrUpdate
	// attrRetries holds device rechecks whose LoadDevice failed transiently (applyAttrRecheck), to be
	// re-attempted on the ticker (retryAttrRechecks). It closes the one-DB-blip window in which the
	// driving fact is already persisted-and-acked but the view kept a stale bound: without a retry the
	// staleness would last until the device's NEXT fact (which may never come) or a restart. Loop-owned
	// (only applyAttrRecheck / retryAttrRechecks touch it); bounded by distinct devices, so an outage
	// cannot grow it with event volume.
	attrRetries map[attrUpdate]struct{}

	cfg       Config
	registry  *runtime.RuleRegistry
	publisher *runtime.Publisher
	clock     detectcore.Clock
	metrics   *detectMetrics
	// backlogProbe reports the broker-confirmed pending + ack-pending backlog on the resolved-
	// events consumer, gating idle-advance on positive caught-up evidence (see consumerBacklog).
	// It is resolved from ResolvedEventsReader in ExecuteStart (a test may pre-set it); a nil
	// probe fails idle-advance safe (it never advances).
	backlogProbe consumerBacklog
	// ruleUpdates carries compiled rule changes from the fact consumer goroutine to the
	// single-writer loop, where applyRuleUpdate mutates the registry and engine together.
	ruleUpdates chan ruleUpdate

	// engine and the checkpoint bookkeeping below are owned exclusively by the
	// startup goroutine (restore/replay) and then the live loop goroutine; nothing
	// else reads or writes them.
	engine         *detectcore.Engine
	pendingAcks    []messaging.Message
	pendingDets    []detectcore.Detection
	lastCheckpoint time.Time
	// lastBudgetSample is the wall-clock time of the most recent per-tenant state-budget
	// measurement (slice 6c). The budget is sampled from the ticker on its OWN cadence, decoupled
	// from checkpoint() — which every call site activity-gates, so a rule-budget breach that changes
	// no snapshot state would otherwise never be measured on a quiet stream.
	lastBudgetSample time.Time
	// lastLiveRead is the wall-clock time of the most recent live read-pump delivery (a
	// message or a read error). It drains the reader's local fetch buffer before idle-advance
	// (the authoritative caught-up signal is the broker backlog, not this timer): idle-advance
	// waits until the loop has been quiet for cfg.IdleAdvanceGuard so a message the pump already
	// fetched but the loop has not yet received is not missed by the backlog check.
	lastLiveRead time.Time
	dirty        bool
	// idleUncommitted latches when idleAdvance moved the watermark off the wall clock but its
	// checkpoint could NOT commit (broker/store outage). The wall-clock advance is not
	// replayable, so a live event applied against the uncommitted inflated frontier would
	// diverge on replay — while it is set, the loop refuses to apply further live events until a
	// checkpoint commits (see the items branch and checkpoint).
	idleUncommitted bool
	// stale latches when Save refuses a backward checkpoint (ErrStaleCheckpoint) — proof that
	// another writer owns this partition (split-brain: a rolling-update overlap or operator
	// error). A stale writer has no legitimate future, so the loop halts: this bounds its
	// derived-event emissions (which now precede the Save, unlike the fully-contained Slice-2a
	// path) to the single detecting cycle instead of one per checkpoint forever.
	stale bool

	// Shutdown coordination (A5): procCancel stops the loop and the pump; readerWG
	// lets ExecuteStop wait for the loop to exit before the reader is torn down.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup

	lifecycle core.LifecycleManager
}

// readItem is one result off the read pump: a message or a read error.
type readItem struct {
	msg messaging.Message
	err error
}

// ruleUpdate is one live change to the rule set, applied on the single-writer loop
// (ADR-051 slice 4b-3). upserts are the compiled rules of one published-rule fact
// (add-or-replace; a redelivered fact is an idempotent no-op that preserves running state).
// removals evict rules by id. The published-rule path only ever produces upserts — versions
// are immutable and superseded ones are retained — so removals stay empty in this slice; the
// field exists so the removal primitive is reachable on the loop for the deferred governance/
// teardown path (ADR-023/052) without an engine change.
type ruleUpdate struct {
	upserts  []runtime.ScopedRule
	removals []string
	// active, when set, is the authoritative active-version of the fact's profile (re-read from the
	// ProfileActive projection after persist). applyRuleUpdate hands it to the dead-man armer AFTER
	// installing the fact's rule cores in the engine — the engine-first wiring contract SetExpected
	// depends on (a rule must be in the engine before a dead-man referencing it is armed). Nil for a
	// rule fact with no resolvable active version (malformed token) or the scaffold path.
	active *runtime.ActiveEntry
}

// armUpdate is a signal to RE-CHECK one device's dead-man arming on the single-writer loop (ADR-051
// slice 4c-2b-2b). It carries only the device identity, NOT a membership snapshot: the loop re-reads
// the authoritative roster projection and arms/disarms from THAT (applyArmRecheck). Doing the read on
// the loop — not in the fact consumers — is what keeps the roster and entity-deleted consumers (two
// goroutines over reorderable streams) from applying their observations out of lifecycle order. Each
// consumer's read of the row and its channel send are not atomic, so their sends can reach the loop
// in the reverse of commit order; but because the loop re-reads the monotonically-converged
// projection on every recheck, whichever recheck it processes last installs the converged truth, and
// a stale fact can never corrupt the projection (the store's monotonic guard), so no in-memory
// ordering guard is needed.
type armUpdate struct {
	tenant      string
	deviceToken string
}

// attrUpdate is a signal to RE-CHECK one device's dynamic-threshold attributes on the single-writer
// loop (ADR-051 slice 4c-3b-2). Like armUpdate it carries only the device identity, NOT the changed
// value: the loop re-reads the authoritative DeviceAttribute projection for the device and replaces
// its attrView entry (applyAttrRecheck). Doing the read on the loop — not in the consumer that
// signaled it — keeps the attribute and entity-deleted consumers (separate goroutines over
// reorderable streams) from installing an observation out of lifecycle order: the loop always reads
// the monotonically-converged projection, so whichever recheck it runs last reflects the settled
// truth, and a stale fact (which the store's monotonic guard bars from mutating the row) is never
// seen as current.
type attrUpdate struct {
	tenant      string
	deviceToken string
}

// NewResolvedEventsProcessor creates a checkpointing resolved-events processor. The
// engine is built (or restored) from the registry's core rule set at ExecuteStart; the
// registry (built from a RuleSource) and the derived-event writer are supplied by the
// caller. A nil registry is treated as the empty set (the scaffold path — no rules, no
// detections). The publisher is built over the writer + registry so it and the loop share
// this processor's bounded-cardinality metrics.
func NewResolvedEventsProcessor(ms *core.Microservice, reader messaging.MessageReader,
	replay ReplayOpener, store *model.SnapshotStore, registry *runtime.RuleRegistry,
	derivedWriter messaging.MessageWriter, cfg Config,
	callbacks core.LifecycleCallbacks) *ResolvedEventsProcessor {
	if cfg.Clock == nil {
		cfg.Clock = detectcore.RealClock{}
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = time.Second
	}
	if registry == nil {
		registry = runtime.NewRuleRegistry(nil)
	}
	rp := &ResolvedEventsProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		Replay:               replay,
		Store:                store,
		cfg:                  cfg,
		registry:             registry,
		clock:                cfg.Clock,
		metrics:              newDetectMetrics(ms),
		// Buffered so a burst of published-rule facts does not block the fact consumer on the
		// loop between resolved-event batches; the loop drains it promptly (rule updates are rare).
		ruleUpdates: make(chan ruleUpdate, 64),
		// Buffered generously: device create/delete/re-type facts can arrive in bursts (a fleet
		// provisioning run), and the loop drains one per resolved-event-batch iteration.
		armUpdates: make(chan armUpdate, 256),
		// Buffered generously for the same reason as armUpdates: an attribute config push across a
		// fleet bursts, and the loop drains one recheck per resolved-event-batch iteration.
		attrUpdates: make(chan attrUpdate, 256),
	}
	rp.publisher = runtime.NewPublisher(derivedWriter, registry, rp.metrics)
	rp.metrics.setRulesActive(registry.Count())
	rpname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "resolved-events-proc")
	rp.lifecycle = core.NewLifecycleManager(rpname, rp, callbacks)
	return rp
}

// Initialize component.
func (rp *ResolvedEventsProcessor) Initialize(ctx context.Context) error {
	return rp.lifecycle.Initialize(ctx)
}

// ExecuteInitialize sets up the cancelable loop context.
func (rp *ResolvedEventsProcessor) ExecuteInitialize(ctx context.Context) error {
	rp.procCtx, rp.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rp *ResolvedEventsProcessor) Start(ctx context.Context) error {
	return rp.lifecycle.Start(ctx)
}

// ExecuteStart restores the engine from the last committed snapshot, replays the
// stream in order up to the head, then launches the live loop. A store or replay
// error aborts startup (fail-closed) rather than silently starting from a stale or
// partial state and re-deriving false/missed detections.
func (rp *ResolvedEventsProcessor) ExecuteStart(ctx context.Context) error {
	if err := rp.restore(ctx); err != nil {
		return err
	}
	// Catch the MEMBERSHIP fact projections (roster, entity-deleted, published-rule) up to head FIRST,
	// before the views build and replay runs, so the absence gate reads CURRENT membership. A device
	// deleted / re-typed / superseded (or created) while this deployment was DOWN has its fact still
	// unconsumed; without draining it first, the gate reads a frozen projection during replay — it would
	// publish a false absence for a departed device, or drop a real one for a created-while-down device.
	// This persists the backlog to the projections without arming or signaling (the loop is not running);
	// startDeadmanGate + the reconciles below then see it, and the live consumers (launched after replay)
	// resume from the advanced durable position. NOTE it drains the entity-deleted stream (which also
	// purges attributes), so a deleted device's dynamic bound is current too; it does NOT drain the
	// attribute-SET stream, so a bound CHANGED (not purged) while down still replays against its pre-down
	// value — the accepted dynamic-threshold non-determinism documented at startAttributeView, unchanged.
	if err := rp.catchUpFactProjections(); err != nil {
		return err
	}
	// Build the dynamic-threshold view BEFORE replay (ADR-051 slice 4c-3b-2): replayToHead re-feeds
	// events through applyResolved, which reads attrView to resolve a rule's dynamic bound. If the
	// view were built after replay, every replayed dynamic-threshold evaluation would see an empty
	// map and never match — worse than a miss: a Match=false on a dynamic Duration rule DELETES a
	// snapshot-restored hold and cancels its timer (engine.go), so an in-flight hold that should fire
	// AFTER the replay window would be destroyed. Building first avoids that.
	//
	// REPLAY IS NON-DETERMINISTIC FOR DYNAMIC THRESHOLDS, by accepted design. The view holds CURRENT
	// projection values (LoadAll), so a replayed evaluation resolves against the attribute's value NOW,
	// not its value when the event first processed — because attributes are external mutable state, not
	// part of the replayable event stream (embedding the resolved bound into the ResolvedEvent upstream
	// at resolution time is the known determinism fix, deferred). Consequences to be honest about:
	//   - a detection that fired at event time but was still buffered (unpublished) when the process
	//     crashed is SILENTLY LOST if the attribute changed across the crash, since replay now resolves
	//     the new value and does not re-fire;
	//   - conversely a detection that did NOT fire originally can be re-derived and PHANTOM-published at
	//     the old event time (downstream dedup keys on rule+series+kind+time, so it does not collapse);
	//   - the window is "small" only on the healthy path; under a checkpoint outage (broker/store down,
	//     which the loop is designed to ride out) it spans the whole outage.
	// This is device/rule-scoped divergence (unlike a watermark divergence, which corrupts every
	// tenant's windows), and it mirrors the dead-man armer's reconcile-from-current-projection stance.
	// See the checkpoint() re-derivability note, which is qualified for this reason.
	if err := rp.startAttributeView(ctx); err != nil {
		return err
	}
	// Build the dead-man armer and load its membership views BEFORE replay (ADR-051 slice 4c-2b; the
	// whole-Slice-4 hardening), symmetric with startAttributeView. The publish-time absence gate
	// (dropStaleAbsences → AbsenceLive) must be live DURING replay: a replayed watermark advance can
	// fire a stale heartbeat absence timer for a device deleted or superseded while the process was
	// down, and replay publishes its detections through checkpoint — so without the gate active here
	// that false absence escapes before the post-replay reconcile could sweep the timer. This only
	// loads the membership maps (no arming — arming needs the final post-replay engine, done in
	// reconcileDeadmanArming).
	if err := rp.startDeadmanGate(ctx); err != nil {
		return err
	}
	if err := rp.replayToHead(); err != nil {
		return err
	}
	if rp.stale {
		// A stale refusal on replay's final checkpoint means another writer already owns a
		// higher checkpoint: fail startup rather than launch a pod whose DETECT loop is halted
		// but whose health endpoints read green (a silently-dead singleton). The leader keeps
		// consuming; this pod backs off and retries into a clean single-writer replay.
		return fmt.Errorf("event-processing: DETECT checkpoint is stale at startup for partition %q (another writer owns it); not starting", rp.cfg.PartitionId)
	}
	rp.lastCheckpoint = rp.clock.Now()
	// Seed the idle-advance clock at startup so the first guard interval of live consumption
	// (which may still be draining a live backlog past the replay head) suppresses idle-advance
	// until the stream genuinely goes quiet.
	rp.lastLiveRead = rp.clock.Now()
	// Resolve the broker-backlog probe that gates idle-advance. A test may pre-set it; the
	// production reader (*messaging.NatsManager's durable reader) satisfies it. If idle-advance
	// is enabled but the reader cannot report the backlog, FAIL startup (fail-closed) rather than
	// silently disable absence-on-silence — the production reader always satisfies it, so this
	// only trips if a future change wraps the reader without forwarding Backlog.
	if rp.backlogProbe == nil {
		if cb, ok := rp.ResolvedEventsReader.(consumerBacklog); ok {
			rp.backlogProbe = cb
		} else if rp.cfg.IdleAdvanceGuard > 0 {
			return fmt.Errorf("event-processing: idle-advance is enabled but the resolved-events reader does not report Backlog; cannot gate absence-on-silence safely")
		}
	}
	// Refresh the dynamic-threshold view from the freshest projection now replay is done: the pre-
	// replay snapshot drove replay, but the live path should start from CURRENT values so a peer pod's
	// attribute committed during our replay (a rolling-update overlap) is not served stale for a full
	// window (see reconcileAttributeView). Runs on this startup goroutine, before the loop launches.
	if err := rp.reconcileAttributeView(ctx); err != nil {
		return err
	}
	// Reconcile the rule registry from the freshest rule projection now replay is done, BEFORE the
	// dead-man armer reconciles against it. The registry was loaded once at Initialize; across the
	// RDB/NATS start + the whole replay a peer pod (rolling-update overlap, pre-Slice-6 singleton) can
	// commit-and-ack a published-rule fact this pod never receives — leaving the registry blind to a
	// whole profile version. That is the SAME rolling-overlap staleness reconcileAttributeView closes
	// for attributes, but with a strictly worse blast radius here: the armer's Reconcile below resolves
	// absence rules through this registry, so a stale registry makes it sweep away restored dead-man
	// timers for the fleet (a version it can't see has no absence rules) AND live events for the new
	// version match no rules at all. Runs on this startup goroutine, before the loop launches.
	if err := rp.reconcileRegistry(ctx); err != nil {
		return err
	}
	// Arm the dead-man over the FINAL (post-replay) engine and reconcile it from the durable
	// read-models BEFORE any live consumption (ADR-051 slice 4c-2b-2b): the restored snapshot is
	// authoritative for FIRED state, the projections for MEMBERSHIP. The armer was already built for
	// the gate (startDeadmanGate); this binds it to the final engine and arms. It runs on this startup
	// goroutine (single-writer) so it never races the loop or the fact consumers launched below.
	if err := rp.reconcileDeadmanArming(ctx); err != nil {
		return err
	}
	rp.readerWG.Add(1)
	go rp.run()
	// Launch the live rule-update consumer after the loop is running (so the ruleUpdates
	// channel has a receiver) and after replay is complete (so the startup rule set is
	// already in place). Both goroutines stop on procCancel and are joined by readerWG.
	if rp.RuleUpdatesReader != nil {
		rp.readerWG.Add(1)
		go rp.runRuleConsumer()
	}
	// The roster and entity-deleted consumers maintain the dead-man read-models (ADR-051 slice
	// 4c-2b) and, after persisting, marshal each device's authoritative membership onto the single-
	// writer loop (armUpdates) so the armer arms/disarms it (slice 4c-2b-2b). They persist and ack
	// independently of the resolved stream (the roster projection's monotonic ordering resolves
	// cross-fact races, not the stream order), so they need no ordering against it; each stops on
	// procCancel and is joined by readerWG.
	if rp.RosterReader != nil {
		rp.readerWG.Add(1)
		go rp.runRosterConsumer()
	}
	if rp.EntityDeletedReader != nil {
		rp.readerWG.Add(1)
		go rp.runEntityDeletedConsumer()
	}
	// The device-attribute consumer maintains the dynamic-threshold projection (ADR-051 slice 4c-3),
	// persisting each fact before acking and then signaling the loop to re-read the device into the
	// live attrView (slice 4c-3b-2), so a rule's dynamic bound tracks the current value. It stops on
	// procCancel and is joined by readerWG.
	if rp.AttributeReader != nil {
		rp.readerWG.Add(1)
		go rp.runAttributeConsumer()
	}
	return nil
}

// catchUpFactProjections drains the membership fact streams (roster, entity-deleted, published-rule) to
// head and persists each fact to its durable projection BEFORE the dead-man gate loads and replay runs.
// It closes the down-window staleness the publish-time absence gate (dropStaleAbsences) would otherwise
// hit: a device deleted / re-typed / superseded while THIS deployment was DOWN has its fact sitting
// unconsumed in the stream, so a projection loaded without draining it is frozen — the gate would then
// publish a false absence for a departed device (or drop a real one for a created-while-down device)
// during replay, before the post-replay reconcile could correct it. Draining here makes the projection
// current so startDeadmanGate's membership load and the post-replay reconciles reflect the down-window
// departures. It persists WITHOUT signaling the loop (handleXFact with signal=false; the loop is not
// running yet); the live consumers, launched after replay, resume from the advanced durable position for
// incremental updates. Runs on the startup goroutine, before startDeadmanGate + replay.
func (rp *ResolvedEventsProcessor) catchUpFactProjections() error {
	if rp.RosterReader != nil {
		if err := rp.drainFactToHead(rp.RosterReader, "device-roster",
			func(m messaging.Message) bool { return rp.handleRosterFact(m, false) }); err != nil {
			return err
		}
	}
	if rp.EntityDeletedReader != nil {
		if err := rp.drainFactToHead(rp.EntityDeletedReader, "entity-deleted",
			func(m messaging.Message) bool { return rp.handleEntityDeletedFact(m, false) }); err != nil {
			return err
		}
	}
	if rp.RuleUpdatesReader != nil {
		if err := rp.drainFactToHead(rp.RuleUpdatesReader, "published-rule",
			func(m messaging.Message) bool { return rp.handleRuleFact(m, false) }); err != nil {
			return err
		}
	}
	return nil
}

// drainFactToHead reads a durable fact reader forward until it is caught up to the broker's live tail,
// applying handle (persist-before-ack, no loop signal) to each message. Each outer pass probes the
// broker (Backlog): when NOTHING is undelivered AND nothing is in flight (pending==0 && ackPending==0)
// it is fully caught up. Otherwise it drains every currently-available message in a tight inner loop —
// undelivered ones (a Fetch pulls them) and any already in the local fetch buffer — bounding each read
// so it cannot block forever, then re-probes. It concludes early ONLY when a read yields nothing AND
// the last probe reported pending==0: the remaining backlog is then delivered-unacked, which is not
// redelivered here — a PEER's during a rolling-update overlap, OR (see the ackWait caveat below) this
// pod's OWN dead incarnation's. Either way it lands in the shared projection later (the peer persists
// it; a dead-incarnation fact redelivers after ackWait and the live consumer processes it).
//
// FAIL-CLOSED on trouble, matching the Backlog-probe error: a genuine read error, or a bounded read
// that yields nothing while the probe still reports UNDELIVERED work (pending>0) — a degraded broker
// that can answer ConsumerInfo but not serve a Fetch, a re-bind that outran the read budget — fails
// startup rather than proceed with a knowingly-stale gate.
//
// ackWait CAVEAT (residual, narrows toward zero at Slice-6): a restart WITHIN the durable's ackWait
// leaves this pod's previous incarnation's delivered-unacked facts (one mid-persist, or a whole
// prefetched batch) in ackPending, indistinguishable pre-Slice-6 from a live peer's — so those specific
// in-flight facts are NOT reflected in the projection this catch-up produces. They redeliver after
// ackWait and the live consumer applies them then, so the exposure is a false/dropped absence only for
// a device whose departure/creation fact was in flight at the exact moment of death, and only until the
// redelivery heals it. The Slice-6 singleton makes ackPending self-attributable and closes it.
//
// A reader that cannot report its backlog (the scaffold/test fake) is skipped — catch-up is a
// production durability concern and those paths wire no store to persist into anyway.
func (rp *ResolvedEventsProcessor) drainFactToHead(reader messaging.MessageReader, kind string, handle func(messaging.Message) bool) error {
	bl, ok := reader.(consumerBacklog)
	if !ok {
		return nil // reader cannot report backlog (scaffold/test): no durable projection to catch up
	}
	applied := 0
	for {
		if rp.procCtx.Err() != nil {
			return rp.procCtx.Err() // shutdown during catch-up
		}
		pctx, cancel := context.WithTimeout(rp.procCtx, backlogProbeTimeout)
		pending, ackPending, err := bl.Backlog(pctx)
		cancel()
		if err != nil {
			return fmt.Errorf("catch-up backlog probe for %s facts: %w", kind, err)
		}
		if pending == 0 && ackPending == 0 {
			// Fully drained AND acked: nothing undelivered, nothing in our fetch buffer or in flight.
			log.Info().Str("kind", kind).Int("applied", applied).Msg("Caught up fact projection to head before replay.")
			return nil
		}
		// Work remains — undelivered messages, or ones already fetched into our local buffer. Drain
		// everything currently available in one inner pass (re-probing per message would cost an RPC
		// each), bounding each read so it cannot block forever.
		read := 0
		for {
			rctx, rcancel := context.WithTimeout(rp.procCtx, factCatchUpReadTimeout)
			msg, rerr := reader.ReadMessage(rctx)
			rcancel()
			if rerr != nil {
				if rp.procCtx.Err() != nil {
					return rp.procCtx.Err() // real shutdown, not the bounded read timeout
				}
				if !errors.Is(rerr, io.EOF) {
					// A genuine read error (not the bounded-read deadline): fail closed.
					return fmt.Errorf("catch-up read of %s facts: %w", kind, rerr)
				}
				break // bounded read yielded nothing available right now
			}
			if !handle(msg) {
				return rp.procCtx.Err() // shutdown mid-persist
			}
			applied++
			read++
		}
		if read == 0 {
			// Nothing was available to read this pass. If the probe reported UNDELIVERED work we could
			// not fetch, the broker is degraded — fail closed rather than proceed stale. Otherwise the
			// only remaining backlog is delivered-unacked (a peer's, or our dead incarnation's within
			// ackWait); conclude caught up as far as this consumer can get.
			if pending > 0 {
				return fmt.Errorf("catch-up of %s facts stalled with %d undelivered pending (broker degraded)", kind, pending)
			}
			log.Info().Str("kind", kind).Int("applied", applied).
				Msg("Caught up fact projection to head before replay (remaining backlog is delivered-unacked; peer or in-flight).")
			return nil
		}
		// Made progress this pass; re-probe to catch anything that arrived or newly delivered.
	}
}

// startDeadmanGate builds the dead-man armer and loads its membership views from the durable roster +
// active-version projections BEFORE replay, so the publish-time absence gate (dropStaleAbsences →
// AbsenceLive) is live DURING replay (ADR-051 slice 4c-2b; the whole-Slice-4 hardening). It is a no-op
// unless BOTH read-model stores are wired (the scaffold/test path leaves the armer nil, and every
// arming and gate site guards on it). It does NOT arm any dead-man — arming needs the final post-replay
// engine and is done in reconcileDeadmanArming; this only populates the maps AbsenceLive reads. The
// armer is constructed over the current (restored) engine, which reconcileDeadmanArming rebinds to the
// final engine (BindEngine) in case replay reset it on a snapshot-ahead-of-head truncation.
func (rp *ResolvedEventsProcessor) startDeadmanGate(ctx context.Context) error {
	if rp.RosterStore == nil || rp.ProfileActiveStore == nil {
		return nil
	}
	rp.armer = runtime.NewDeadmanArmer(rp.registry, rp.engine)
	rosters, err := rp.RosterStore.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load roster projection for dead-man gate: %w", err)
	}
	actives, err := rp.ProfileActiveStore.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load active-version projection for dead-man gate: %w", err)
	}
	rp.armer.LoadMembership(toRosterEntries(rosters), toActiveEntries(actives))
	return nil
}

// reconcileDeadmanArming arms the dead-man over the FINAL (post-replay) engine and reconciles it from
// the durable roster + active-version projections (ADR-051 slice 4c-2b-2b). The armer itself was built
// pre-replay (startDeadmanGate) for the gate; here it is bound to the final engine (replay may have
// reset it on a snapshot-ahead-of-head truncation) and its arming is reconciled. It is a no-op unless
// the armer was built (both stores wired). Reconcile arms every still-rostered device under its active
// version — the engine's once-per-epoch SetExpected suppresses re-firing an already-fired dead-man —
// and sweeps engine dead-man AND heartbeat-armed absence timers whose membership is gone (a device
// deleted / a version superseded while down).
//
// It RELOADS the projections rather than reusing the gate's pre-replay load so a peer pod's roster or
// active-version fact committed DURING our replay (a rolling-update overlap) is armed from the freshest
// membership — the same freshness reconcileAttributeView / reconcileRegistry take.
//
// It deliberately does NOT force a checkpoint: the reconciled arming is fully re-derivable from the
// (durable) projections on every restart, and the once-fired latches live in the restored snapshot,
// so the output need not be persisted here. It becomes durable at the first natural checkpoint (a
// live event, or the idle-advance that fires a never-seen device's dead-man). This also sidesteps
// committing an equal-sequence snapshot against the monotonic Save guard.
func (rp *ResolvedEventsProcessor) reconcileDeadmanArming(ctx context.Context) error {
	// The armer is built (and non-nil) only when both stores are wired, so armer!=nil is the normal
	// gate; the store checks are a defensive backstop so this never derefs a nil store even if a future
	// wiring/test leaves the armer set without them (it LoadAlls both below).
	if rp.armer == nil || rp.RosterStore == nil || rp.ProfileActiveStore == nil {
		return nil
	}
	rp.armer.BindEngine(rp.engine)
	rosters, err := rp.RosterStore.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load roster projection for dead-man reconcile: %w", err)
	}
	actives, err := rp.ProfileActiveStore.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load active-version projection for dead-man reconcile: %w", err)
	}
	rp.armer.Reconcile(toRosterEntries(rosters), toActiveEntries(actives))
	log.Info().Int("rostered", len(rosters)).Int("profiles", len(actives)).
		Msg("Reconciled DETECT dead-man arming from the roster + active-version projections.")
	return nil
}

// reconcileRegistry re-reads the whole durable rule projection and diff-applies it into the live
// registry + engine on the startup goroutine (single-writer), AFTER replay and BEFORE the dead-man
// armer reconciles. It closes the rolling-update overlap in which a peer pod acked a published-rule
// fact this pod never received (the fact readers are shared durables — exactly-once across pods — and
// the registry was loaded once at Initialize, so a fact in that window never reaches this registry).
// It uses the SAME projection + compile path as the boot load and the live consumer (StoreRuleSource
// → CompilePublishedRules), so a rule reconciled here is byte-identical to one that arrived live.
//
// The diff mirrors applyRuleUpdate: an unchanged id is left intact (preserving the running rule's
// snapshot-restored engine state), a changed body GCs the stale keyed state before re-installing (the
// reused-token reset-on-change), and an id the projection no longer holds is removed. The ADD /
// unchanged paths force no checkpoint: they are fully re-derivable — the reconcile re-runs from the
// durable projection on every restart (the same reasoning as reconcileAttributeView). The GC paths (a
// changed body, or a removal) are NOT self-re-derivable — a restart rebuilds the registry from the same
// projection, so the def then matches / the id is already gone and the branch no-ops while the old
// snapshot state would resurrect — so, exactly as applyRuleUpdate does on the live path, a GC here
// marks the loop dirty (below) to force the first natural checkpoint to persist the drop (finding E; the
// def-change-durability hardening, now closed on both paths). A rule ADDED here missed its share of the
// replay window (replay ran under the pre-reconcile registry), which is acceptable: it starts fresh from
// live consumption, the same current-value posture the attribute view accepts. No-op on the scaffold/
// test path (no RuleStore).
func (rp *ResolvedEventsProcessor) reconcileRegistry(ctx context.Context) error {
	if rp.RuleStore == nil {
		return nil
	}
	scoped, err := NewStoreRuleSource(rp.RuleStore).Load(ctx)
	if err != nil {
		return fmt.Errorf("reload rule projection for post-replay registry reconcile: %w", err)
	}
	fresh := make(map[string]struct{}, len(scoped))
	upserted, changed, removed := 0, 0, 0
	for i := range scoped {
		sr := scoped[i]
		if sr.Compiled == nil {
			continue
		}
		fresh[sr.Compiled.ID] = struct{}{}
		// A reused profile token can re-mint an existing id with a DIFFERENT body; GC the stale keyed
		// state before re-installing so a hold/accumulator is never grafted onto a rule that changed
		// meaning (the same comparison applyRuleUpdate makes on the live path).
		if old, ok := rp.registry.Lookup(sr.Compiled.ID); ok && old.Definition != sr.Definition {
			rp.engine.RemoveRule(sr.Compiled.ID)
			changed++
		}
		if rp.registry.Upsert(sr) {
			rp.engine.UpsertRule(sr.Compiled.Core)
			upserted++
		}
	}
	// Evict any rule the projection no longer holds (a teardown that acked on a peer pod during the
	// overlap). Snapshot the ids first so Remove does not mutate the map being ranged (registry.IDs).
	for _, id := range rp.registry.IDs() {
		if _, ok := fresh[id]; ok {
			continue
		}
		if rp.registry.Remove(id) {
			rp.engine.RemoveRule(id)
			removed++
		}
	}
	if changed > 0 || removed > 0 {
		// A GC above dropped serializable keyed state that a restart would otherwise resurrect (the def
		// then matches / the id is gone, so the rebuild no-ops). Mark dirty so the first loop checkpoint
		// persists the drop — the same durability the live applyRuleUpdate GC gets. Safe here: this runs
		// pre-loop on the startup goroutine; the equal-seq/equal-watermark Save the ticker then issues is
		// accepted by the monotonic guard (it refuses only a backward move).
		rp.dirty = true
	}
	rp.metrics.setRulesActive(rp.registry.Count())
	log.Info().Int("upserted", upserted).Int("changed", changed).Int("removed", removed).Int("active", rp.registry.Count()).
		Msg("Reconciled DETECT rule registry from the durable rule projection (post-replay).")
	return nil
}

// toRosterEntries / toActiveEntries map the persistence rows to the armer's model-free input shapes.
func toRosterEntries(rows []model.DeviceRoster) []runtime.RosterEntry {
	out := make([]runtime.RosterEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, runtime.RosterEntry{Tenant: r.Tenant, DeviceToken: r.DeviceToken, ProfileToken: r.ProfileToken, ExpectedSince: r.ExpectedSince})
	}
	return out
}

func toActiveEntries(rows []model.ProfileActive) []runtime.ActiveEntry {
	out := make([]runtime.ActiveEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, runtime.ActiveEntry{Tenant: r.Tenant, ProfileToken: r.ProfileToken, ActiveVersionToken: r.ActiveVersionToken, PublishedAt: r.PublishedAt})
	}
	return out
}

// startAttributeView builds the loop-owned dynamic-threshold view and reconciles it from the durable
// DeviceAttribute projection (ADR-051 slice 4c-3b-2), parallel to startDeadmanArmer. It is a no-op
// unless AttributeStore is wired (the scaffold/test path leaves attrView nil, and every read/recheck
// site guards on it). Like the armer's reconcile it does NOT force a checkpoint: the view is fully
// re-derivable from the (durable) projection on every restart, so nothing here need be persisted.
func (rp *ResolvedEventsProcessor) startAttributeView(ctx context.Context) error {
	if rp.AttributeStore == nil {
		return nil
	}
	rp.attrView = runtime.NewDeviceAttributeView()
	return rp.reconcileAttributeView(ctx)
}

// reconcileAttributeView (re)loads the whole device-attribute projection and rebuilds the view from
// it. It runs TWICE at startup: once before replay (startAttributeView, so replayed dynamic-threshold
// evaluations resolve against real values) and once AFTER replay (so the live path starts from the
// FRESHEST projection). The second read narrows the rolling-update window in which a peer pod could
// commit-and-ack an attribute AFTER this pod's pre-replay LoadAll — a fact that never redelivers here,
// so the pre-replay snapshot alone would serve a stale bound across the whole replay and beyond.
// No-op when the view is disabled (scaffold/test path).
func (rp *ResolvedEventsProcessor) reconcileAttributeView(ctx context.Context) error {
	if rp.attrView == nil {
		return nil
	}
	rows, err := rp.AttributeStore.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load device-attribute projection for dynamic-threshold view: %w", err)
	}
	rp.attrView.Reconcile(toAttrEntries(rows))
	log.Info().Int("attributes", len(rows)).Msg("Reconciled DETECT dynamic-threshold view from the device-attribute projection.")
	return nil
}

// toAttrEntries maps the persistence rows to the view's model-free input shape. LoadAll/LoadDevice
// return only LIVE rows (tombstones excluded), so every entry is a value to weigh.
func toAttrEntries(rows []model.DeviceAttribute) []runtime.AttrEntry {
	out := make([]runtime.AttrEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, runtime.AttrEntry{Tenant: r.Tenant, DeviceToken: r.DeviceToken, Scope: r.Scope, Key: r.AttrKey, Value: r.Value})
	}
	return out
}

// restore loads the durable checkpoint and rebuilds the engine from it, or builds a
// fresh empty engine when none exists (a new Instance). It runs before any loop, so
// it needs no synchronization.
func (rp *ResolvedEventsProcessor) restore(ctx context.Context) error {
	start := rp.clock.Now()
	snap, ok, err := rp.Store.Load(ctx, rp.cfg.PartitionId)
	if err != nil {
		return fmt.Errorf("load snapshot for partition %q: %w", rp.cfg.PartitionId, err)
	}
	if !ok {
		rp.engine = detectcore.NewEngine(rp.registry.Cores(), rp.cfg.Lateness)
		rp.metrics.recordRestore(0, 0)
		log.Info().Str("partition", rp.cfg.PartitionId).Msg("No prior DETECT snapshot; starting from empty engine.")
		return nil
	}
	engine, err := detectcore.Restore(rp.registry.Cores(), rp.cfg.Lateness, snap.Payload)
	if err != nil {
		return fmt.Errorf("restore engine from snapshot for partition %q: %w", rp.cfg.PartitionId, err)
	}
	rp.engine = engine
	rp.metrics.recordRestore(rp.clock.Now().Sub(start).Seconds(), uint64(snap.StreamSeq))
	log.Info().Str("partition", rp.cfg.PartitionId).Uint64("lastSeq", engine.LastSeq()).
		Time("watermark", engine.Watermark()).Msg("Restored DETECT engine from snapshot.")
	return nil
}

// replayToHead reads the stream in order from the restored sequence up to the head
// captured at open, feeding the engine so its state reflects every event through the
// head BEFORE live consumption begins. It checkpoints periodically so a crash mid-
// replay resumes further along. If the stream head is behind the snapshot (a
// re-created/truncated stream vs. a stale snapshot — an operator error pre-GA), it
// resets the engine to empty rather than replaying against sequences the stream no
// longer holds.
func (rp *ResolvedEventsProcessor) replayToHead() error {
	startSeq := rp.engine.LastSeq() + 1
	reader, head, err := rp.Replay.NewReplayReader(rp.cfg.Suffix, startSeq)
	if err != nil {
		return fmt.Errorf("open replay reader from seq %d: %w", startSeq, err)
	}
	defer func() { _ = reader.Close() }()

	if head < rp.engine.LastSeq() {
		log.Warn().Uint64("snapshotSeq", rp.engine.LastSeq()).Uint64("streamHead", head).
			Msg("Snapshot is ahead of the stream head (re-created/truncated stream); resetting DETECT engine to empty.")
		rp.engine = detectcore.NewEngine(rp.registry.Cores(), rp.cfg.Lateness)
		rp.dirty = false
		// Clear the stale row (the deliberate backward move Save's monotonic guard
		// refuses); live consumption then writes a fresh checkpoint from sequence 1.
		if err := rp.Store.Reset(rp.procCtx, rp.cfg.PartitionId); err != nil {
			return fmt.Errorf("reset stale snapshot for partition %q: %w", rp.cfg.PartitionId, err)
		}
		return nil
	}

	replayed := 0
	for {
		msg, err := reader.Read(rp.procCtx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("replay read: %w", err)
		}
		// Replay owns its ephemeral acks (via the replay reader), so nothing is
		// buffered for the durable here — only the engine is advanced.
		if rp.applyResolved(msg) {
			replayed++
		}
		if replayed >= rp.cfg.CheckpointEvents {
			rp.checkpoint(rp.procCtx)
			replayed = 0
		}
	}
	rp.checkpoint(rp.procCtx) // commit the final replay position
	if head >= startSeq {
		log.Info().Uint64("fromSeq", startSeq).Uint64("toHead", head).Uint64("lastSeq", rp.engine.LastSeq()).
			Msg("Replayed DETECT engine to stream head; starting live consumption.")
	}
	return nil
}

// run is the single-writer live loop. It selects over the read pump, a checkpoint-
// interval ticker (so a quiet stream still commits and releases buffered acks), and
// shutdown. On a NORMAL exit it flushes a final checkpoint; message processing and
// checkpointing never overlap — both run here.
func (rp *ResolvedEventsProcessor) run() {
	defer rp.readerWG.Done()

	items := make(chan readItem)
	pumpDone := make(chan struct{})
	go rp.readPump(items, pumpDone)
	// Always cancel and join the pump on the way out (including a panic unwind).
	defer func() { rp.procCancel(); <-pumpDone }()

	ticker := time.NewTicker(rp.cfg.TickInterval)
	defer ticker.Stop()

	for {
		// While parked on an uncommitted idle advance, STOP receiving live messages by disabling
		// the items case (a nil channel blocks forever in select). Applying a live event against
		// the uncommitted inflated frontier would diverge on replay — and receive-and-skip would
		// be worse: a skipped, unacked message redelivers only at AckWait, by which time fresh
		// traffic has advanced the engine past its sequence, so it is then guard-dropped as a
		// duplicate AND acked — silently lost. Not receiving instead makes the pump HOLD the next
		// message in delivery order; the ticker retries the commit and, once it succeeds and
		// clears the park, the held message is delivered and applied in order, nothing skipped.
		liveItems := items
		if rp.idleUncommitted {
			liveItems = nil
		}
		select {
		case <-rp.procCtx.Done():
			rp.finalCheckpoint()
			return
		case upd := <-rp.ruleUpdates:
			// Rule-set mutation on the single writer: a plain add/replace changes only which rules
			// run, not the engine's serializable state (the rule set is not in the snapshot — it is
			// rebuilt from the durable rule projection on restart), so it needs no checkpoint of its
			// own. The ONE exception is a RemoveRule (a reused-id def change, or a removal): it GCs
			// serializable keyed state, so applyRuleUpdate marks the loop dirty in that case to force
			// a checkpoint (otherwise a restart would replay the GC'd state back to life).
			rp.applyRuleUpdate(upd)
		case au := <-rp.armUpdates:
			// Device membership recheck on the single writer: re-read the authoritative roster
			// projection and arm/disarm. Like a rule update it mutates engine timer state but not the
			// checkpoint sequence; it is made durable by the next checkpoint (or re-derived by
			// reconcile on restart), so it needs no checkpoint of its own. A nil armUpdates channel
			// (scaffold path) never selects here.
			rp.applyArmRecheck(au)
		case at := <-rp.attrUpdates:
			// Dynamic-threshold recheck on the single writer: re-read the authoritative attribute
			// projection for the device and replace its view entry (applyAttrRecheck). It mutates only
			// the in-memory view — NOT engine state or the checkpoint sequence (the view is re-derived
			// from the durable projection on restart, never snapshotted) — so it needs no checkpoint.
			rp.applyAttrRecheck(at)
		case item := <-liveItems:
			// Stamp the read time on ANY pump delivery — a message or a read error. For a
			// message it means the reader had work; for an error it is the fail-safe posture:
			// an error (outage / re-bind) is no evidence of an empty tail, so treat it as
			// activity and let the backlog gate — not this timer — decide caught-up. Either
			// way idle-advance holds off until the loop has been quiet for the full guard.
			rp.lastLiveRead = rp.clock.Now()
			if item.err != nil {
				if errors.Is(item.err, io.EOF) {
					log.Info().Msg("Detected EOF on resolved events stream")
					rp.finalCheckpoint()
					return
				}
				rp.ResolvedEventsReader.HandleResponse(item.err)
				continue
			}
			rp.handle(item.msg)
			// Flush by buffered-ack count (bounds in-flight acks regardless of whether
			// messages advanced the engine, were duplicates, or were poison).
			if len(rp.pendingAcks) >= rp.cfg.CheckpointEvents {
				rp.checkpoint(rp.procCtx)
			}
		case <-ticker.C:
			// Read the clock through cfg.Clock (not the ticker's wall-clock value) so every
			// time comparison in the loop — the guard, the interval, the idle-advance target —
			// shares one injectable clock; a ManualClock test then drives the loop coherently.
			now := rp.clock.Now()
			// Fire silent-series timers off the wall clock when the stream is caught up, then
			// checkpoint. idleAdvance itself commits (and delivers) whenever it moved state, so
			// the interval checkpoint below only handles the case idle-advance did not: buffered
			// acks/detections from live messages, or a suppressed (not-caught-up) advance.
			rp.idleAdvance(rp.procCtx, now)
			if (rp.dirty || len(rp.pendingAcks) > 0 || len(rp.pendingDets) > 0) && now.Sub(rp.lastCheckpoint) >= rp.cfg.CheckpointInterval {
				rp.checkpoint(rp.procCtx)
			}
			// Per-tenant state-budget accounting (ADR-023 amendment, slice 6c), sampled on the ticker
			// on its own cadence — NOT from checkpoint(), whose every call site is activity-gated
			// (dirty / buffered acks / idle-advance-moved-state). A plain rule upsert changes the rule
			// count but dirties nothing and buffers nothing, so on a quiet stream a rule-budget breach
			// would never be measured or logged if this rode checkpoint(). It is a governance read on
			// the single-writer loop; gating to the checkpoint interval bounds its O(state) cost.
			if now.Sub(rp.lastBudgetSample) >= rp.cfg.CheckpointInterval {
				rp.recordStateBudget()
				rp.lastBudgetSample = now
			}
			// Retry any recheck a transient store error deferred (bounded, idempotent) — dynamic-threshold
			// attribute rechecks and dead-man arming rechecks alike. Both share ONE per-tick wall-clock
			// budget so that even a black-holed DB (every read burning loopReadTimeout) cannot stall the
			// single-writer loop for cap×timeout: whatever does not fit carries to the next tick.
			recheckDeadline := now.Add(loopRecheckBudget)
			rp.retryAttrRechecks(recheckDeadline)
			rp.retryArmRechecks(recheckDeadline)
		}
	}
}

// readPump repeatedly reads one message and hands it (or the error) to the loop. It
// selects on ctx.Done when sending so a shutdown mid-read never deadlocks against a
// loop that has already stopped receiving.
func (rp *ResolvedEventsProcessor) readPump(items chan<- readItem, done chan<- struct{}) {
	defer close(done)
	for {
		msg, err := rp.ResolvedEventsReader.ReadMessage(rp.procCtx)
		select {
		case items <- readItem{msg: msg, err: err}:
		case <-rp.procCtx.Done():
			return
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			// A non-EOF read error was handed to the loop (which logs it via
			// HandleResponse); back off before the next read so a persistent,
			// instantly-returning error (e.g. a bad subscription the reader's own
			// self-heal does not cover) cannot hot-spin the CPU and flood logs.
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
		}
	}
}

// handle processes one LIVE message: buffer it for a post-checkpoint ack, then feed
// it to the engine (or drop it if unprocessable). Poison/duplicate messages do not
// advance the engine; they are acked at the next checkpoint (safe — they carry no
// new state).
func (rp *ResolvedEventsProcessor) handle(msg messaging.Message) {
	rp.pendingAcks = append(rp.pendingAcks, msg)
	rp.applyResolved(msg)
}

// applyResolved parses a resolved-event message, fans it out across the applicable rules
// (metric-scoped feed, runtime.Plan), and feeds the resulting per-rule events to the engine
// as ONE message-sequenced batch. It returns true iff the message advanced the engine. A
// message with no stream sequence (StreamSeq==0), no parseable tenant, or an unparseable
// payload is unprocessable and dropped — redelivery/replay cannot make it processable. A
// redelivered/replayed message at or below the engine's sequence is skipped before fan-out
// (the engine's message-level guard would drop it anyway; skipping early avoids the wasted
// predicate evaluations) — still acked upstream, but not counted or marked dirty.
func (rp *ResolvedEventsProcessor) applyResolved(msg messaging.Message) bool {
	if msg.StreamSeq == 0 {
		// A JetStream-delivered message always carries a sequence; a 0 means the
		// broker metadata was unreadable. Treat it as unprocessable rather than feed
		// the engine a 0 the idempotent guard would silently swallow.
		log.Warn().Str("subject", msg.Subject).Msg("Dropping resolved event with no stream sequence (unreadable metadata).")
		return false
	}
	_, tenant, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping resolved event with no parseable tenant in subject %q", msg.Subject)
		return false
	}
	event, err := dmproto.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping resolved event that could not be parsed from subject %q", msg.Subject)
		return false
	}
	if msg.StreamSeq <= rp.engine.LastSeq() {
		return false // duplicate/replayed message — acked, but no re-fan-out
	}

	// Bound device-reported future skew against the server-stamped processed time before it
	// drives the shared watermark: an unclamped far-future occurred time would advance the one
	// frontier decades forward and fire every tenant's timers at once (persistently — the
	// watermark is snapshotted). The clamp is deterministic under replay (both times are
	// immutable in the payload).
	occurred := runtime.EffectiveEventTime(event.OccurredTime, event.ProcessedTime, rp.cfg.MaxFutureSkew)

	// Resolve the source device's dynamic-threshold attributes (SERVER-over-SHARED, slice 4c-3b-2)
	// once for the whole fan-out: the flattened map the loop-owned view holds, bound onto every
	// sample's Input so a dynamic comparison reads the device's own bound. Nil on the scaffold path
	// (no view) or for a device with no attributes — a presence-guarded clean non-match either way.
	var attr map[string]float64
	if rp.attrView != nil {
		attr = rp.attrView.For(tenant, event.SourceDeviceToken)
	}
	// Fan out: select the rules this event feeds (by tenant + profile version) and build one
	// core event per applicable rule per sample it carries. The batch shares the message
	// sequence — the single idempotency + checkpoint unit — so N same-seq events cannot each
	// trip the guard; all events share the clamped event time.
	plan := rp.registry.Plan(msg.StreamSeq, tenant, event, occurred, attr)
	rp.metrics.RecordFanout(len(plan.Events), plan.EvalErrors)
	prev := rp.engine.LastSeq()
	rp.engine.ProcessResolved(msg.StreamSeq, occurred, plan.Events)
	rp.drainDetections()
	if rp.engine.LastSeq() > prev {
		rp.dirty = true
		rp.metrics.recordApplied()
		return true
	}
	return false
}

// drainDetections moves the engine's emitted detections into the pending-detections buffer,
// which the next checkpoint publishes BEFORE it commits/acks past the producing message
// (deliver-before-checkpoint, see checkpoint). Draining marks the loop dirty so a checkpoint
// is guaranteed to fire and flush them even if the engine's serializable state did not
// otherwise change (e.g. a pure Threshold rule that emits without retaining state).
func (rp *ResolvedEventsProcessor) drainDetections() {
	if dets := rp.engine.Drain(); len(dets) > 0 {
		rp.pendingDets = append(rp.pendingDets, dets...)
		rp.dirty = true
	}
}

// consumerBacklog reports the resolved-events durable consumer's UNDELIVERED (pending) and
// DELIVERED-BUT-UNACKED (ackPending) message counts from the broker — the authoritative "is
// anyone still working the tail?" signal. *messaging.NatsManager's reader satisfies it (via a
// ConsumerInfo call). It is the crux of a SOUND idle-advance: a quiet read loop is NOT proof of
// caught-up (the loop is equally quiet during a broker outage or a consumer re-bind while
// messages pile up), so idle-advance gates on these broker counts, not on read-loop silence.
// ackPending additionally surfaces messages a PEER consumer took during a rolling-update
// overlap (invisible to pending), narrowing the split-brain window.
type consumerBacklog interface {
	Backlog(ctx context.Context) (pending, ackPending uint64, err error)
}

// backlogProbeTimeout bounds the ConsumerInfo call so a broker black-hole (a request parked in
// the reconnect buffer awaiting a reply) cannot wedge the single-writer loop indefinitely.
const backlogProbeTimeout = 2 * time.Second

// loopReadTimeout bounds a per-device projection read issued ON the single-writer loop (an arming or
// dynamic-threshold recheck) so a slow / black-holed DB call cannot wedge the loop — the same posture
// as backlogProbeTimeout for the idle-advance probe. On timeout the recheck defers to the ticker retry
// set exactly as any other transient store error does, so nothing is lost.
const loopReadTimeout = 2 * time.Second

// loopRecheckDrainCap bounds how many deferred rechecks the ticker re-attempts per tick, so a large
// backlog (a long DB outage spanning many devices) is worked down at a steady rate instead of firing
// hundreds of loop-blocking reads in a single tick. The remainder carries to the next tick; map
// iteration order varies, so successive ticks cover different entries until the set drains.
const loopRecheckDrainCap = 64

// factCatchUpReadTimeout bounds each catch-up read so the drain cannot block forever when the only
// remaining backlog is a PEER's delivered-unacked messages (a rolling-update overlap) that are never
// redelivered to this consumer. A buffered/undelivered message returns well within it; on timeout the
// drain concludes caught up as far as this consumer can get (drainFactToHead).
const factCatchUpReadTimeout = 2 * time.Second

// loopRecheckBudget bounds the TOTAL wall-clock a single tick spends re-attempting deferred rechecks
// (arming + dynamic-threshold combined). The count cap alone bounds reads to 2×cap, but under a
// black-holed DB each read burns the full loopReadTimeout, so cap×timeout could stall the single-writer
// loop for minutes; this deadline caps the per-tick stall regardless of set size. It only bites while
// the DB is already degraded (healthy reads are far under it and drain the whole cap), and the loop
// cannot checkpoint against a dead DB anyway, so throttling recovery here loses nothing (unacked
// messages redeliver). The remainder retries next tick.
const loopRecheckBudget = 2 * time.Second

// idleAdvance moves the engine's logical clock forward off the wall clock when the resolved
// stream is genuinely caught up, so a silent series' absence/duration/session timer fires — the
// timer that no later event would otherwise trigger (a device that STOPS reporting produces no
// event to advance the event-time watermark past its deadline). It runs on the single-writer
// loop (the ticker branch), so it never races message processing.
//
// THE CAUGHT-UP GATE IS THE CORRECTNESS CRUX (and the naive form is unsound). Idle-advance is
// wall-clock-derived; if it advanced the frontier past a deadline whose re-arming heartbeat sat
// unconsumed, it would fire a FALSE absence and then durably commit the inflated watermark
// (late-dropping the heartbeat on recovery). Read-loop silence cannot distinguish "caught up"
// from "broker outage / consumer re-bind with a growing backlog," so the gate is layered:
//   - not already parked on an uncommitted advance (idleUncommitted), and there is pending work
//     to fire (HasPendingWork) — otherwise a bare watermark move would checkpoint every interval
//     forever on an idle engine with nothing to fire;
//   - quiet for cfg.IdleAdvanceGuard — drains the reader's local fetch buffer, so a message the
//     pump already fetched but the loop has not yet received is not missed by the backlog check
//     below (that check counts only what the broker has NOT delivered);
//   - the checkpoint interval has elapsed — rate-limit, AND the discipline that the frontier only
//     ever moves at a commit point (see below); the interval also lets our own prior acks settle
//     so the ackPending count reflects a PEER, not us;
//   - Backlog reports pending == 0 AND ackPending == 0 — the BROKER confirms nothing is undelivered
//     and nothing is in flight at any consumer (a rolling-update peer's delivered-unacked message
//     shows as ackPending). This is the positive evidence read-loop silence cannot provide; an
//     unavailable/erroring probe fails safe (no advance). It still cannot see an event in flight
//     UPSTREAM of the stream (device → ingest → resolution): that latency budget is cfg.Lateness,
//     documented on WatermarkLatenessSeconds.
//
// Passing `now` advances the frontier to now-lateness (symmetric with an event arriving at now).
//
// COMMIT-BEFORE-EVENT. The wall-clock advance is not replayable (it carries no stream sequence).
// If a later event were applied against an idle-inflated frontier that was never committed, a
// crash would replay that event against the LOWER event-derived frontier and diverge (different
// window eviction / session merge). So idle-advance runs only at a commit point and checkpoints
// immediately: on success the committed snapshot carries the inflated watermark and replay's
// monotonic watermark reproduces it exactly. If that checkpoint CANNOT commit (broker/store
// outage), it latches idleUncommitted, and the loop refuses to apply further live events until a
// checkpoint commits — so an event is never applied against an uncommitted inflated frontier.
// Fired detections publish before the commit (deliver-before-checkpoint), so a crash re-derives
// rather than drops them.
func (rp *ResolvedEventsProcessor) idleAdvance(ctx context.Context, now time.Time) {
	if rp.stale || rp.cfg.IdleAdvanceGuard <= 0 || rp.idleUncommitted {
		return // halted / disabled / already parked on an uncommitted advance (a checkpoint owes)
	}
	if !rp.engine.HasPendingWork() {
		return // nothing armed to fire: leave the frontier at rest (no perpetual idle checkpoint)
	}
	if now.Sub(rp.lastLiveRead) < rp.cfg.IdleAdvanceGuard {
		return // recent read-loop activity: the reader's fetch buffer may still hold messages
	}
	if now.Sub(rp.lastCheckpoint) < rp.cfg.CheckpointInterval {
		return // hold to the commit cadence: the frontier moves only at commit points
	}
	if rp.backlogProbe == nil {
		return // no way to confirm the broker tail is empty: fail safe, never advance blind
	}
	pctx, cancel := context.WithTimeout(ctx, backlogProbeTimeout)
	pending, ackPending, err := rp.backlogProbe.Backlog(pctx)
	cancel()
	if err != nil || pending > 0 || ackPending > 0 {
		// A backlog remains (a pause/outage let messages pile up), a peer holds delivered-unacked
		// messages, or the broker/consumer is unreachable: we are NOT caught up. Suppress.
		if err != nil {
			log.Debug().Err(err).Msg("Idle-advance suppressed: cannot confirm the resolved-events tail is empty.")
		}
		return
	}
	// Broker-confirmed caught up. Fence a split brain BEFORE emitting: idle detections are
	// wall-clock-fabricated (not replay-duplicates of real events), so a stale co-writer emitting
	// one injects a false, non-collapsible signal. detectStaleOwner halts this writer if another
	// owns a higher checkpoint — the pre-publish fence Save's post-hoc guard cannot provide.
	if rp.detectStaleOwner(ctx) {
		return
	}
	changed := rp.engine.Advance(now)
	if dets := rp.engine.Drain(); len(dets) > 0 {
		rp.pendingDets = append(rp.pendingDets, dets...)
		rp.metrics.recordIdleAdvance(len(dets))
	}
	if changed {
		rp.dirty = true
		rp.checkpoint(ctx) // commit the inflated frontier (and deliver any fired detections first)
		if rp.dirty {
			// The checkpoint deferred (broker/store outage): the frontier moved in-memory but is
			// NOT durable. Park live-event application until a later checkpoint commits it, so no
			// event is applied against an uncommitted, non-replayable frontier.
			rp.idleUncommitted = true
		}
	}
}

// detectStaleOwner reports whether another writer has committed a checkpoint AHEAD of this
// engine's applied sequence — proof this pod lost a split brain (a rolling-update overlap where
// the peer consumes the heartbeats and commits past us). It reads only the committed SEQUENCE
// (not the state payload) and does not mutate it, so it fences an idle firing BEFORE it publishes
// a fabricated detection, which checkpoint's post-hoc Save guard (publish-then-Save) cannot. On
// detection it latches stale and halts the loop (matching checkpoint's split-brain handling). A
// read error returns false — a transient store blip must not halt a healthy writer; the checkpoint
// Save guard remains the hard backstop.
//
// Residual: this catches only a peer that has already COMMITTED past us. A peer that has consumed
// heartbeats but not yet committed its first post-overlap checkpoint is invisible here (committed
// seq still equals ours) — the ackPending gate in idleAdvance covers the common case (the peer's
// delivered-unacked messages), but a peer that fetched then crashed leaves a sub-AckWait window
// where one fabricated detection can still escape. It is bounded to one cycle (stale latches on
// the next fence/Save), and the definitive fix is the Slice-6 singleton deploy (one writer, no
// overlap); this fence + the Save equal-seq/watermark guards are the pre-Slice-6 runtime backstop.
func (rp *ResolvedEventsProcessor) detectStaleOwner(ctx context.Context) bool {
	seq, ok, err := rp.Store.LoadCommittedSeq(ctx, rp.cfg.PartitionId)
	if err != nil || !ok {
		return false
	}
	if uint64(seq) > rp.engine.LastSeq() {
		log.Error().Str("partition", rp.cfg.PartitionId).Int64("committedSeq", seq).
			Uint64("appliedSeq", rp.engine.LastSeq()).
			Msg("Idle-advance fenced: another writer owns a higher checkpoint (split brain); halting this writer.")
		rp.stale = true
		if rp.procCancel != nil {
			rp.procCancel()
		}
		return true
	}
	return false
}

// applyRuleUpdate mutates the registry and engine together on the single-writer loop
// (ADR-051 slice 4b-3), keeping the two consistent — the publisher's Lookup and the fan-out's
// RulesFor both read the registry, so a rule present in one but not the other would
// mis-attribute or orphan detections. An upsert installs the rule and preserves any running
// state for a re-seen id (engine.UpsertRule); a removal GCs the rule's keyed state
// (engine.RemoveRule). Both are gated on the registry admitting/holding the rule so a rejected
// upsert (nil compiled / empty tenant) or an absent removal touches no engine state.
//
// NOTE on removal durability: a removal sweeps engine state that IS snapshotted, but rule
// updates do not checkpoint (see the loop), so a removal is durable only once a later
// checkpoint commits — and it is re-derivable only if the driving fact is re-applied on
// restart. In this slice nothing drives a removal (the publish path only upserts), so the
// concern is inert; the deferred teardown path (ADR-023/052) that adds removal-driving facts
// owns making them replay-durable.
func (rp *ResolvedEventsProcessor) applyRuleUpdate(upd ruleUpdate) {
	gcd := false
	for i := range upd.upserts {
		sr := upd.upserts[i]
		// A reused profile token can re-mint an existing rule id with a DIFFERENT body whose
		// change lives only in the predicate — invisible to the engine's core.Rule comparison.
		// Compare the raw definitions here (the authoritative, complete change test) and GC the
		// stale rule's keyed state before installing the replacement, so a Duration hold or a
		// window accumulator is never grafted onto a rule that now means something else. An
		// unchanged redelivery leaves state intact (the whole point of preserve-on-reupsert).
		if old, ok := rp.registry.Lookup(sr.Compiled.ID); ok && old.Definition != sr.Definition {
			rp.engine.RemoveRule(sr.Compiled.ID)
			gcd = true
		}
		if rp.registry.Upsert(sr) {
			rp.engine.UpsertRule(sr.Compiled.Core)
		}
	}
	for _, id := range upd.removals {
		if rp.registry.Remove(id) {
			rp.engine.RemoveRule(id)
			gcd = true
		}
	}
	if gcd {
		// A RemoveRule above GC'd serializable keyed state (a reused-id def change, or a removal).
		// Unlike a plain upsert — the rule SET is not snapshotted; it is rebuilt from the durable rule
		// projection on restart — dropping a rule's keyed windows/timers DOES change the snapshot. Mark
		// the loop dirty so the GC is captured by the next checkpoint; otherwise a checkpoint might not
		// fire before a restart, and replay would resurrect the GC'd state under the (reused) id — e.g.
		// a stale Duration hold firing a false detection (finding E).
		rp.dirty = true
	}
	rp.metrics.setRulesActive(rp.registry.Count())
	// Dead-man arming for the fact's profile, AFTER the rule cores are installed above (engine-first,
	// the contract SetExpected depends on): record the active version, disarm a superseded version's
	// dead-men across the fleet, and arm every rostered device of the profile. Nil active (malformed
	// token / scaffold) or nil armer skips it.
	if rp.armer != nil && upd.active != nil {
		rp.armer.ApplyActiveVersion(*upd.active)
	}
}

// applyArmRecheck re-reads a device's authoritative roster membership and arms or disarms its
// dead-man on the single-writer loop (ADR-051 slice 4c-2b-2b). The read runs HERE, not in the fact
// consumer that signaled the recheck, so the roster and entity-deleted consumers — separate
// goroutines over reorderable streams — cannot apply their observations out of lifecycle order: the
// loop always reads the current, monotonically-converged projection, so whichever recheck it
// processes last installs the converged truth, and a stale fact (which the store's monotonic guard
// prevents from mutating the row) can never be observed as current. A transient store error skips
// the recheck — the projection is durable, and the next fact for the device or the restart reconcile
// re-arms — rather than blocking the single-writer loop on a retry. A device with no row (never
// happens post-persist) or a tombstone reads as not-live and disarms.
func (rp *ResolvedEventsProcessor) applyArmRecheck(au armUpdate) {
	if rp.armer == nil || rp.RosterStore == nil {
		return
	}
	rctx, cancel := context.WithTimeout(rp.procCtx, loopReadTimeout)
	row, live, err := rp.RosterStore.Load(rctx, au.tenant, au.deviceToken)
	cancel()
	if err != nil {
		// A transient read error: the driving fact was already persisted-AND-acked, so a silently
		// dropped recheck would strand stale membership — and unlike a stale threshold the fact is
		// frequently the device's LAST fact ever (an entity-deleted tombstone), so "the next fact
		// heals it" is false: a deleted device would stay dead-man-armed and fire a FALSE absence, or
		// a created device would never arm (a missed absence), until a restart. Remember it and retry
		// on the ticker (retryArmRechecks) — the exact mirror of applyAttrRecheck.
		log.Warn().Err(err).Str("tenant", au.tenant).Str("device", au.deviceToken).
			Msg("Dead-man arming recheck failed to read the roster projection; will retry on the ticker.")
		if rp.armRetries == nil {
			rp.armRetries = map[armUpdate]struct{}{}
		}
		rp.armRetries[au] = struct{}{}
		return
	}
	// Clear any pending retry for this device: the load succeeded, so the membership below is current.
	delete(rp.armRetries, au)
	entry := runtime.RosterEntry{Tenant: au.tenant, DeviceToken: au.deviceToken}
	if live {
		entry.ProfileToken = row.ProfileToken
		entry.ExpectedSince = row.ExpectedSince
	}
	rp.armer.ApplyDeviceMembership(entry, live)
}

// retryArmRechecks re-attempts every arming recheck a prior RosterStore.Load error deferred, on the
// ticker so the retry rate is bounded to the tick cadence rather than hot-spinning the DB — the exact
// mirror of retryAttrRechecks. Each success clears the entry (applyArmRecheck); a continued failure
// re-adds it for the next tick. The pending set is bounded by distinct devices (an ADR-023 state-budget
// class, like the roster), so even a total DB outage cannot grow it with event volume. It runs only on
// the single-writer loop.
func (rp *ResolvedEventsProcessor) retryArmRechecks(deadline time.Time) {
	if len(rp.armRetries) == 0 {
		return
	}
	pending := make([]armUpdate, 0, min(len(rp.armRetries), loopRecheckDrainCap))
	for au := range rp.armRetries {
		pending = append(pending, au)
		if len(pending) >= loopRecheckDrainCap {
			break // cap per-tick loop-blocking reads; the remainder retries next tick
		}
	}
	for _, au := range pending {
		if rp.clock.Now().After(deadline) {
			break // per-tick wall-clock budget spent (shared with retryAttrRechecks); rest carries over
		}
		rp.applyArmRecheck(au)
	}
}

// applyAttrRecheck re-reads a device's authoritative live attributes and REPLACES its dynamic-
// threshold view entry on the single-writer loop (ADR-051 slice 4c-3b-2). Like applyArmRecheck the
// read runs HERE, not in the consumer that signaled it, so the attribute and entity-deleted consumers
// — separate goroutines over reorderable streams — cannot install a value out of lifecycle order: the
// loop reads the monotonically-converged projection, replacing (never merging) the entry, so a
// removed/purged attribute disappears and a stale straggler the store's guard rejected is never seen.
// A transient store error skips the recheck — the projection is durable, and the next fact for the
// device or the restart reconcile re-reads it — rather than blocking the single-writer loop on a retry.
func (rp *ResolvedEventsProcessor) applyAttrRecheck(at attrUpdate) {
	if rp.attrView == nil || rp.AttributeStore == nil {
		return
	}
	rctx, cancel := context.WithTimeout(rp.procCtx, loopReadTimeout)
	rows, err := rp.AttributeStore.LoadDevice(rctx, at.tenant, at.deviceToken)
	cancel()
	if err != nil {
		// A transient read error: the driving fact was already persisted-AND-acked, so a silently
		// dropped recheck would leave the view serving the PRE-fact bound until the device's next fact
		// (which may never come) or a restart. Remember it and retry on the ticker (retryAttrRechecks),
		// so one DB blip cannot strand a stale threshold.
		log.Warn().Err(err).Str("tenant", at.tenant).Str("device", at.deviceToken).
			Msg("Dynamic-threshold recheck failed to read the attribute projection; will retry on the ticker.")
		if rp.attrRetries == nil {
			rp.attrRetries = map[attrUpdate]struct{}{}
		}
		rp.attrRetries[at] = struct{}{}
		return
	}
	// Install the converged set and clear any pending retry for this device. This REPLACES the view
	// entry but deliberately GCs NO engine keyed state (a dynamic Duration hold, a windowed accumulator)
	// that used the old bound: the new bound takes effect at the device's NEXT event, not retroactively —
	// softer than applyRuleUpdate's GC-on-rule-definition-change. Note this is NOT replay-deterministic:
	// the view holds CURRENT projection values (attributes are external mutable state, not part of the
	// replayable event stream), so a replayed evaluation resolves against the value NOW, not at original
	// event time — the accepted divergence documented at startAttributeView (the determinism fix,
	// embedding the resolved bound into the ResolvedEvent upstream, is deferred).
	delete(rp.attrRetries, at)
	rp.attrView.ReplaceDevice(at.tenant, at.deviceToken, toAttrEntries(rows))
}

// retryAttrRechecks re-attempts every device recheck a prior LoadDevice error deferred, on the ticker
// so the retry rate is bounded to the tick cadence rather than hot-spinning the DB. Each success clears
// the entry (applyAttrRecheck); a continued failure re-adds it for the next tick. The pending set is
// bounded by distinct devices (an ADR-023 state-budget class, like the roster), so even a total DB
// outage cannot grow it with event volume. It runs only on the single-writer loop.
func (rp *ResolvedEventsProcessor) retryAttrRechecks(deadline time.Time) {
	if len(rp.attrRetries) == 0 {
		return
	}
	pending := make([]attrUpdate, 0, min(len(rp.attrRetries), loopRecheckDrainCap))
	for at := range rp.attrRetries {
		pending = append(pending, at)
		if len(pending) >= loopRecheckDrainCap {
			break // cap per-tick loop-blocking reads; the remainder retries next tick
		}
	}
	for _, at := range pending {
		if rp.clock.Now().After(deadline) {
			break // per-tick wall-clock budget spent (shared with retryArmRechecks); rest carries over
		}
		rp.applyAttrRecheck(at)
	}
}

// runRuleConsumer drains the published-rule fact stream (ADR-051 slice 4b-3). For each fact
// it (1) PERSISTS the rules to the durable projection, (2) compiles them (off the loop — pure,
// reads no engine state) and marshals them onto the single-writer loop as a ruleUpdate, then
// (3) acks. Persist-before-ack is the durability spine: the fact stream has finite retention,
// so the projection — not the stream — is what a restart rebuilds from; acking only after the
// rows are committed makes delivery at-least-once into the projection. A persist failure leaves
// the fact unacked to redeliver (idempotent upsert). Compilation drops uncompilable rules but
// still persists them, so the startup rebuild and the live path stay symmetric.
func (rp *ResolvedEventsProcessor) runRuleConsumer() {
	defer rp.readerWG.Done()
	for {
		msg, err := rp.RuleUpdatesReader.ReadMessage(rp.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.RuleUpdatesReader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
			continue
		}
		if !rp.handleRuleFact(msg, true) {
			return // shutdown mid-persist/-send: leave unacked; the rows rebuild it next start
		}
	}
}

// handleRuleFact persists one published-rule fact's rules (DetectRule projection) and its active-version
// row (ProfileActive projection), both persist-before-ack, then acks. When signal is true (the live
// consumer) it also compiles the rules and marshals them + the active-version arming trigger onto the
// single-writer loop (engine-first, in one ruleUpdate); the pre-replay catch-up (catchUpFactProjections)
// passes false — reconcileRegistry + reconcileDeadmanArming rebuild the registry and arming from the
// persisted projections afterward, so the loop signal (and its active-version re-read) is unneeded and
// the loop is not running yet. It returns false only on a shutdown mid-persist/-send.
func (rp *ResolvedEventsProcessor) handleRuleFact(msg messaging.Message, signal bool) bool {
	tenant, ev, ok := decodePublishedRuleFact(rp.procCtx, msg)
	if !ok {
		// Unparseable/poison: redelivery cannot fix it. Ack to stop it redelivering forever.
		rp.ackRuleFact(msg)
		return true
	}
	// Persist BEFORE ack (durability): the projection — not the finite-retention stream —
	// is the restart source of truth. Retry in-process until it commits (or shutdown) so a
	// DB outage cannot lose the rule to the broker's finite redelivery (MaxDeliver); the
	// fact stays unacked throughout, and a redelivery mid-retry re-persists idempotently.
	if rp.RuleStore != nil {
		rows := factRuleRows(tenant, ev)
		if !rp.persistBeforeAck("published rules "+tenant+"/"+ev.ProfileVersionToken,
			func() error { return rp.RuleStore.Upsert(rp.procCtx, rows) }) {
			return false // shutdown mid-retry: leave unacked; the rows redeliver next start
		}
	}
	// Maintain the active-version projection off the SAME fact (ADR-051 slice 4c-2b), before
	// ack and with the same retry discipline: it is where the grace-period base (PublishedAt)
	// lands durably, so a publish-then-restart does not lose it and re-burst the fleet. A
	// version token that does not split into "{profileToken}@{version}" yields no active-version
	// row (profileActiveFromFact returns ok=false) — the well-formed producer always mints the
	// "@"-joined form, so this only skips a forged/malformed fact; its rules (keyed by the whole
	// version token) may still install, they simply get no arming entry, which is inert.
	var activeEntry *runtime.ActiveEntry
	if rp.ProfileActiveStore != nil {
		if active, ok := profileActiveFromFact(tenant, ev); ok {
			if !rp.persistBeforeAck("profile-active "+active.Tenant+"/"+active.ProfileToken,
				func() error { return rp.ProfileActiveStore.Upsert(rp.procCtx, active) }) {
				return false // shutdown mid-retry: leave unacked; redelivers next start
			}
			// Re-read the AUTHORITATIVE active version for the armer: the upsert is monotonic on
			// publishedAt, so a stale rule fact does not regress the active version — the dead-man
			// must arm against the CURRENT active version (which the earlier fact's rules already
			// installed), not necessarily this fact's. Same persist-before-ack retry discipline;
			// only when arming is enabled and we intend to signal the loop.
			if signal && rp.armer != nil {
				var row model.ProfileActive
				var found bool
				if !rp.persistBeforeAck("profile-active-load "+active.Tenant+"/"+active.ProfileToken,
					func() (err error) {
						row, found, err = rp.ProfileActiveStore.Load(rp.procCtx, active.Tenant, active.ProfileToken)
						return err
					}) {
					return false // shutdown mid-retry: leave unacked; reconcile rebuilds arming next start
				}
				if found {
					activeEntry = &runtime.ActiveEntry{Tenant: row.Tenant, ProfileToken: row.ProfileToken,
						ActiveVersionToken: row.ActiveVersionToken, PublishedAt: row.PublishedAt}
				}
			}
		}
	}
	// Apply live to the single-writer loop: the compiled rules AND (engine-first, in the SAME
	// ruleUpdate so applyRuleUpdate installs before it arms) the active-version arming trigger.
	// Send when there is either work — an empty batch with an active version still triggers a
	// version-transition disarm. Skipped during catch-up (signal=false): the reconciles rebuild it.
	if signal {
		rules, _ := runtime.CompilePublishedRules(tenant, ev.ProfileVersionToken, ev.Rules)
		if len(rules) > 0 || activeEntry != nil {
			select {
			case rp.ruleUpdates <- ruleUpdate{upserts: rules, active: activeEntry}:
			case <-rp.procCtx.Done():
				return false // shutdown before hand-off: leave unacked; the persisted rows rebuild it
			}
		}
	}
	rp.ackRuleFact(msg)
	return true
}

// ackRuleFact acks a published-rule fact, logging a failed ack (harmless: a redelivered fact
// re-persists and re-applies idempotently).
func (rp *ResolvedEventsProcessor) ackRuleFact(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a published-rule fact; it will redeliver (idempotent).")
	}
}

// checkpoint durably delivers buffered detections, commits any new engine state to the
// snapshot store, and then acks every buffered message — IN THAT ORDER (deliver-before-
// checkpoint). Publishing the detections first is the correctness contract: a detection is
// re-derivable by replaying from the last checkpoint, so as long as the acked/committed
// sequence never advances PAST a message whose detections are not yet published, a crash
// re-emits them (at-least-once, dedup-collapsible downstream). A publish failure therefore
// aborts the whole checkpoint — nothing committed, nothing acked, retried next cycle.
//
// CAVEAT for dynamic-threshold detections (ADR-051 slice 4c-3b-2): re-derivability holds only
// when a rule's leaf is a pure function of the replayed event. A dynamic threshold resolves its
// bound from the device-attribute view, which holds CURRENT (not event-time) values, so if the
// attribute changed across the crash a buffered-but-unpublished dynamic detection is NOT re-emitted
// (silently lost) and a non-detection can be re-derived as a phantom — so those are at-least-once
// only under attribute stability across the replay window. See the ExecuteStart startAttributeView
// note for the full accounting and the deferred determinism fix.
//
// The snapshot commit runs only when engine state changed since the last checkpoint (dirty);
// when it did not — the buffer holds only redelivered duplicates or poison — the acks are
// released against the already-durable prior snapshot with no redundant write. Because a
// committed snapshot captures engine.LastSeq(), every buffered valid event is at or below the
// durable sequence and every poison message carries no state, so acking the whole buffer is
// safe. A commit failure leaves the messages unacked (they redeliver / are re-read on replay).
func (rp *ResolvedEventsProcessor) checkpoint(ctx context.Context) {
	if rp.stale {
		return // a split-brain-losing writer: no publish, no commit, no ack (see stale)
	}
	// Deliver-before-checkpoint: hand off every buffered detection first. If any fails
	// (retryable broker error), defer the entire checkpoint so the producing messages stay
	// unacked and a replay re-derives/re-emits.
	if !rp.publishPending(ctx) {
		return
	}
	if rp.dirty {
		start := rp.clock.Now()
		payload, err := rp.engine.Snapshot()
		if err != nil {
			log.Error().Err(err).Msg("Failed to serialize DETECT snapshot; deferring checkpoint")
			return
		}
		wm := rp.engine.Watermark()
		snap := &model.DetectSnapshot{
			PartitionId: rp.cfg.PartitionId,
			StreamSeq:   int64(rp.engine.LastSeq()),
			Watermark:   wm,
			Payload:     payload,
		}
		if err := rp.Store.Save(ctx, snap); err != nil {
			if errors.Is(err, model.ErrStaleCheckpoint) {
				// Another writer advanced the checkpoint past ours: we are the losing side of a
				// split brain and must stop — no ack, no further publish. Halt the loop so this
				// writer emits no more derived events (bounding the containment regression to this
				// one cycle). Our unacked messages redeliver to the leader on the shared durable,
				// so detection continues there; the durable single-writer guarantee is Slice-6's
				// singleton deploy, of which this is the runtime safety net.
				log.Error().Err(err).Str("partition", rp.cfg.PartitionId).
					Msg("DETECT checkpoint refused as stale (split-brain); halting this writer.")
				rp.stale = true
				if rp.procCancel != nil {
					rp.procCancel()
				}
				return
			}
			log.Error().Err(err).Msg("Failed to commit DETECT snapshot; messages remain unacked and will redeliver")
			return
		}
		rp.dirty = false
		// A committed snapshot makes any idle-advanced frontier durable, so the loop may resume
		// applying live events (see the items-branch park).
		rp.idleUncommitted = false

		// Observability (owned by this loop, ADR-051 thread).
		now := rp.clock.Now()
		rp.metrics.recordCheckpoint(rp.engine.LastSeq(), now.Sub(start).Seconds(), len(payload), now.Sub(wm).Seconds())
	}

	for _, m := range rp.pendingAcks {
		if err := m.Ack(); err != nil {
			// A failed ack redelivers that message; the idempotent replay guard makes
			// reprocessing safe, so log and move on rather than unwinding the commit.
			log.Warn().Err(err).Msg("Failed to ack a checkpointed resolved event; it will redeliver (idempotent).")
		}
	}
	rp.pendingAcks = rp.pendingAcks[:0]
	rp.lastCheckpoint = rp.clock.Now()
}

// stateBudgetStats is the bounded, tenant-label-free result of a per-tenant state-budget sample: the
// aggregate live-key total plus the COUNTS of tenants over each ceiling (never a per-tenant series —
// the ADR-023 G.3 DoS lesson). It is what the gauges publish.
type stateBudgetStats struct {
	totalLiveKeys    int
	tenantsOverRules int
	tenantsOverKeys  int
}

// recordStateBudget samples the per-tenant state budget and publishes the bounded gauges. Slice 6c-1
// measures only; slice 6c-2 acts on these same breaches.
func (rp *ResolvedEventsProcessor) recordStateBudget() {
	s := rp.computeStateBudget()
	rp.metrics.recordStateBudget(s.totalLiveKeys, s.tenantsOverRules, s.tenantsOverKeys)
}

// computeStateBudget rolls the engine's per-rule live-key counts and the registry's per-tenant rule
// counts up to per-tenant totals and returns the bounded stats, logging each over-budget tenant
// (rule-count and live-key breaches are distinct signals) so an operator can find the offender
// without a high-cardinality metric. It is split from the metric emission so the rollup + over-budget
// logic is unit-testable without a Prometheus registry (metrics are nil in tests).
func (rp *ResolvedEventsProcessor) computeStateBudget() stateBudgetStats {
	liveByRule := rp.engine.LiveKeyCounts()
	liveByTenant := make(map[string]int, len(liveByRule))
	total := 0
	for ruleID, n := range liveByRule {
		total += n
		// A rule id with no parseable tenant prefix is a mis-minted rule the backstop already
		// contains at publish; attribute its keys to the aggregate total but not to any tenant.
		if tenant, ok := runtime.RuleTenant(ruleID); ok {
			liveByTenant[tenant] += n
		}
	}
	rulesByTenant := rp.registry.RuleCountsByTenant()

	stats := stateBudgetStats{totalLiveKeys: total}
	if rp.cfg.MaxRulesPerTenant > 0 {
		for tenant, n := range rulesByTenant {
			if n > rp.cfg.MaxRulesPerTenant {
				stats.tenantsOverRules++
				log.Warn().Str("tenant", tenant).Int("rules", n).Int("budget", rp.cfg.MaxRulesPerTenant).
					Msg("Tenant is over its DETECT rule-count budget (ADR-023); enforcement lands in slice 6c-2.")
			}
		}
	}
	if rp.cfg.MaxLiveKeysPerTenant > 0 {
		for tenant, n := range liveByTenant {
			if n > rp.cfg.MaxLiveKeysPerTenant {
				stats.tenantsOverKeys++
				log.Warn().Str("tenant", tenant).Int("liveKeys", n).Int("budget", rp.cfg.MaxLiveKeysPerTenant).
					Msg("Tenant is over its DETECT live-key budget (ADR-023); enforcement lands in slice 6c-2.")
			}
		}
	}
	return stats
}

// dropStaleAbsences removes buffered ABSENCE detections whose device left the rule's scope after the
// timer was armed — deleted (roster row gone), re-typed (roster profile differs), or version superseded
// (active version moved past the rule's). It is the publish-time membership gate (whole-Slice-4
// hardening, finding C/D) that stops a stale wheel timer emitting a false absence: a heartbeat-armed
// timer a straggler re-armed after disarm, or one a replayed watermark advance fired for a device
// removed while the process was down. It runs at the single publish choke point (every event-driven,
// idle-advance, and replay fire flows through publishPending), so one check covers them all, evaluated
// against the loop-owned roster + active views at publish time — so a membership change since arming is
// honored. Other detection kinds and the scaffold path (no armer, no read-models wired) pass through
// untouched. A dropped stale absence is a TERMINAL drop (like the tenant backstop), so it never wedges
// the loop. Fail-safe: AbsenceLive treats an unknown device/rule as NOT live, so at worst a genuinely
// live but not-yet-known device's absence is missed (a delayed roster fact) rather than a false one
// published.
func (rp *ResolvedEventsProcessor) dropStaleAbsences() {
	if rp.armer == nil {
		return
	}
	kept := rp.pendingDets[:0]
	for _, d := range rp.pendingDets {
		key := detectcore.SeriesKey{Rule: d.RuleID, Series: d.Series}
		if d.Kind == detectcore.Absence && !rp.armer.AbsenceLive(key) {
			// Terminal drop of a stale-roster absence. If it is a Raised, clear the engine's two-edge
			// latch so a later legitimate fire (once the roster catches up and re-arms) is not
			// suppressed by a latch set for a raise nothing downstream ever saw (ADR-057, review
			// H2/F5). A Resolved drop needs no clear — resolve already cleared the latch on emit.
			if d.Edge == detectcore.EdgeRaised {
				rp.engine.ClearRaised(key)
			}
			rp.metrics.recordStaleAbsenceDropped()
			continue
		}
		kept = append(kept, d)
	}
	rp.pendingDets = kept
}

// publishPending durably delivers every buffered detection, in order, on its owning tenant's
// derived-event subject. It returns true when the buffer is fully flushed (empty on entry
// counts). A Publish that returns an error is a retryable broker failure: the successfully-
// published prefix is dropped so a retry does not re-emit it, the unpublished tail is
// retained, and false is returned so the caller defers the checkpoint. Terminal drops
// (tenant-backstop rejects, orphan rules) return nil from Publish and advance past the
// detection — they must never wedge the loop.
func (rp *ResolvedEventsProcessor) publishPending(ctx context.Context) bool {
	rp.dropStaleAbsences()
	i := 0
	for i < len(rp.pendingDets) {
		if err := rp.publisher.Publish(ctx, rp.pendingDets[i]); err != nil {
			log.Error().Err(err).Msg("Failed to publish a derived event; deferring checkpoint (will retry).")
			rp.pendingDets = rp.pendingDets[i:]
			return false
		}
		i++
	}
	rp.pendingDets = rp.pendingDets[:0]
	return true
}

// finalCheckpoint flushes on a NORMAL shutdown so a clean stop delivers buffered detections,
// commits the latest state, and releases buffered acks. It is called only from the loop's
// normal exit paths — never from a deferred/panic-unwind path, so a mid-mutation panic can
// never commit a half-updated engine or ack the message that triggered it (the unacked
// message redelivers/replays instead). It uses a fresh, bounded context because the loop
// context is already cancelled by the time this runs.
func (rp *ResolvedEventsProcessor) finalCheckpoint() {
	if !rp.dirty && len(rp.pendingAcks) == 0 && len(rp.pendingDets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rp.checkpoint(ctx)
}

// Stop component.
func (rp *ResolvedEventsProcessor) Stop(ctx context.Context) error {
	return rp.lifecycle.Stop(ctx)
}

// ExecuteStop stops the loop and waits for it to exit (flushing a final checkpoint)
// before the reader is torn down (A5).
func (rp *ResolvedEventsProcessor) ExecuteStop(context.Context) error {
	if rp.procCancel != nil {
		rp.procCancel()
	}
	rp.readerWG.Wait()
	return nil
}

// Terminate component.
func (rp *ResolvedEventsProcessor) Terminate(ctx context.Context) error {
	return rp.lifecycle.Terminate(ctx)
}

// ExecuteTerminate is a no-op; the loop and pump are already stopped in ExecuteStop.
func (rp *ResolvedEventsProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
