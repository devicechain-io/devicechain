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
	// Clock is the wall clock used for checkpoint-interval timing; defaults to the
	// real clock.
	Clock detectcore.Clock
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
// emitting nothing. Wall-clock idle advance (firing absence/duration timers during a quiet
// stream) is deliberately NOT done here: it requires caught-up-to-live-tail detection to
// avoid firing restored timers prematurely, and lands with the ADR-044 roster read-model in
// slice 4c.
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

	cfg       Config
	registry  *runtime.RuleRegistry
	publisher *runtime.Publisher
	clock     detectcore.Clock
	metrics   *detectMetrics
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
	dirty          bool
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
	rp.readerWG.Add(1)
	go rp.run()
	// Launch the live rule-update consumer after the loop is running (so the ruleUpdates
	// channel has a receiver) and after replay is complete (so the startup rule set is
	// already in place). Both goroutines stop on procCancel and are joined by readerWG.
	if rp.RuleUpdatesReader != nil {
		rp.readerWG.Add(1)
		go rp.runRuleConsumer()
	}
	return nil
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
		select {
		case <-rp.procCtx.Done():
			rp.finalCheckpoint()
			return
		case upd := <-rp.ruleUpdates:
			// Rule-set mutation on the single writer: it changes which rules run, not the
			// engine's serializable state (rules are not in the snapshot — they are rebuilt from
			// the durable rule projection on restart), so it needs no checkpoint of its own.
			rp.applyRuleUpdate(upd)
		case item := <-items:
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
		case now := <-ticker.C:
			// Checkpoint on the interval when there is new state to persist OR buffered
			// acks to release (e.g. a quiet stream of duplicates after restart).
			if (rp.dirty || len(rp.pendingAcks) > 0 || len(rp.pendingDets) > 0) && now.Sub(rp.lastCheckpoint) >= rp.cfg.CheckpointInterval {
				rp.checkpoint(rp.procCtx)
			}
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

	// Fan out: select the rules this event feeds (by tenant + profile version) and build one
	// core event per applicable rule per sample it carries. The batch shares the message
	// sequence — the single idempotency + checkpoint unit — so N same-seq events cannot each
	// trip the guard; all events share the clamped event time.
	plan := rp.registry.Plan(msg.StreamSeq, tenant, event, occurred)
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
		}
		if rp.registry.Upsert(sr) {
			rp.engine.UpsertRule(sr.Compiled.Core)
		}
	}
	for _, id := range upd.removals {
		if rp.registry.Remove(id) {
			rp.engine.RemoveRule(id)
		}
	}
	rp.metrics.setRulesActive(rp.registry.Count())
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
		tenant, ev, ok := decodePublishedRuleFact(rp.procCtx, msg)
		if !ok {
			// Unparseable/poison: redelivery cannot fix it. Ack to stop it redelivering forever.
			rp.ackRuleFact(msg)
			continue
		}
		// Persist BEFORE ack (durability): the projection — not the finite-retention stream —
		// is the restart source of truth. Retry in-process until it commits (or shutdown) so a
		// DB outage cannot lose the rule to the broker's finite redelivery (MaxDeliver); the
		// fact stays unacked throughout, and a redelivery mid-retry re-persists idempotently.
		if rp.RuleStore != nil {
			rows := factRuleRows(tenant, ev)
			backoff := readErrorBackoff
			for rp.RuleStore.Upsert(rp.procCtx, rows) != nil {
				log.Error().Str("tenant", tenant).Str("profileVersion", ev.ProfileVersionToken).
					Msg("Failed to persist published rules; retrying (fact stays unacked).")
				select {
				case <-time.After(backoff):
				case <-rp.procCtx.Done():
					return // shutdown mid-retry: leave unacked; the rows redeliver next start
				}
				if backoff *= 2; backoff > maxRulePersistBackoff {
					backoff = maxRulePersistBackoff
				}
			}
		}
		// Apply live to the running engine on the single-writer loop.
		if rules, _ := runtime.CompilePublishedRules(tenant, ev.ProfileVersionToken, ev.Rules); len(rules) > 0 {
			select {
			case rp.ruleUpdates <- ruleUpdate{upserts: rules}:
			case <-rp.procCtx.Done():
				return // shutdown before hand-off: leave unacked; the persisted rows rebuild it
			}
		}
		rp.ackRuleFact(msg)
	}
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

// publishPending durably delivers every buffered detection, in order, on its owning tenant's
// derived-event subject. It returns true when the buffer is fully flushed (empty on entry
// counts). A Publish that returns an error is a retryable broker failure: the successfully-
// published prefix is dropped so a retry does not re-emit it, the unpublished tail is
// retained, and false is returned so the caller defers the checkpoint. Terminal drops
// (tenant-backstop rejects, orphan rules) return nil from Publish and advance past the
// detection — they must never wedge the loop.
func (rp *ResolvedEventsProcessor) publishPending(ctx context.Context) bool {
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
