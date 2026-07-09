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
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// readErrorBackoff caps the retry rate after a non-EOF read error the reader's own
// self-heal does not resolve, so a persistent instant error cannot hot-spin.
const readErrorBackoff = 200 * time.Millisecond

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
// This slice wires the durable spine with the rule set the loop is constructed with
// (empty in the scaffold; multi-tenant rule CRUD is a later slice), so with no rules
// the loop advances the watermark by event time and checkpoints its position without
// emitting detections. Wall-clock idle advance (firing absence/duration timers
// during a quiet stream) is deliberately NOT done here: it is only meaningful once
// rules exist and requires caught-up-to-live-tail detection to avoid firing restored
// timers prematurely — both land with the rule runtime in a later slice. The
// REACT/emit path is likewise later; detections drained here are dropped.
type ResolvedEventsProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	Replay               ReplayOpener
	Store                *model.SnapshotStore

	cfg     Config
	rules   []detectcore.Rule
	clock   detectcore.Clock
	metrics *detectMetrics

	// engine and the checkpoint bookkeeping below are owned exclusively by the
	// startup goroutine (restore/replay) and then the live loop goroutine; nothing
	// else reads or writes them.
	engine         *detectcore.Engine
	pendingAcks    []messaging.Message
	lastCheckpoint time.Time
	dirty          bool

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

// NewResolvedEventsProcessor creates a checkpointing resolved-events processor. The
// engine is built (or restored) from rules at ExecuteStart; the scaffold slice
// passes an empty set.
func NewResolvedEventsProcessor(ms *core.Microservice, reader messaging.MessageReader,
	replay ReplayOpener, store *model.SnapshotStore, rules []detectcore.Rule, cfg Config,
	callbacks core.LifecycleCallbacks) *ResolvedEventsProcessor {
	if cfg.Clock == nil {
		cfg.Clock = detectcore.RealClock{}
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = time.Second
	}
	rp := &ResolvedEventsProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		Replay:               replay,
		Store:                store,
		cfg:                  cfg,
		rules:                rules,
		clock:                cfg.Clock,
		metrics:              newDetectMetrics(ms),
	}
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
	rp.lastCheckpoint = rp.clock.Now()
	rp.readerWG.Add(1)
	go rp.run()
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
		rp.engine = detectcore.NewEngine(rp.rules, rp.cfg.Lateness)
		rp.metrics.recordRestore(0, 0)
		log.Info().Str("partition", rp.cfg.PartitionId).Msg("No prior DETECT snapshot; starting from empty engine.")
		return nil
	}
	engine, err := detectcore.Restore(rp.rules, rp.cfg.Lateness, snap.Payload)
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
		rp.engine = detectcore.NewEngine(rp.rules, rp.cfg.Lateness)
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
			if (rp.dirty || len(rp.pendingAcks) > 0) && now.Sub(rp.lastCheckpoint) >= rp.cfg.CheckpointInterval {
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

// applyResolved parses a resolved-event message and feeds it to the engine in
// stream-sequence order, returning true iff it advanced the engine. A message with
// no stream sequence (metadata-less, StreamSeq==0), no parseable tenant, or an
// unparseable payload is unprocessable and dropped — redelivery/replay cannot make
// it processable, and this tap has no dead-letter path.
func (rp *ResolvedEventsProcessor) applyResolved(msg messaging.Message) bool {
	if msg.StreamSeq == 0 {
		// A JetStream-delivered message always carries a sequence; a 0 means the
		// broker metadata was unreadable. Treat it as unprocessable rather than feed
		// the engine a 0 the idempotent guard would silently swallow.
		log.Warn().Str("subject", msg.Subject).Msg("Dropping resolved event with no stream sequence (unreadable metadata).")
		return false
	}
	if _, _, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject); !ok {
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
	// Feed the engine. In this slice the engine carries no rules, so this advances
	// the watermark and applied sequence (the checkpoint position) without emitting;
	// the rule fan-out (one resolved event → N (rule, series) evaluations) lands with
	// the predicate layer in a later slice. A redelivered message at or below the
	// engine's sequence is dropped by the idempotent guard (no advance) — still acked
	// upstream, but not counted or marked dirty.
	prev := rp.engine.LastSeq()
	rp.engine.ProcessEvent(detectcore.Event{Seq: msg.StreamSeq, Time: event.OccurredTime})
	rp.drainDetections()
	if rp.engine.LastSeq() > prev {
		rp.dirty = true
		rp.metrics.recordApplied()
		return true
	}
	return false
}

// drainDetections empties the engine's emitted detections. The REACT/emit path is a
// later slice; for now detections are dropped, but they are drained on the same
// goroutine so a future emit sits BEFORE the checkpoint that makes them durable.
func (rp *ResolvedEventsProcessor) drainDetections() {
	if dets := rp.engine.Drain(); len(dets) > 0 {
		rp.dirty = true
	}
}

// checkpoint commits any new engine state to the snapshot store and then acks every
// buffered message. The commit runs only when the state changed since the last
// checkpoint (dirty); when it did not — the buffer holds only redelivered duplicates
// or poison — the acks are released against the already-durable prior snapshot with
// no redundant write. Because a committed snapshot captures engine.LastSeq(), every
// buffered valid event is at or below the durable sequence (durable) and every
// poison message carries no state, so acking the whole buffer is safe. A commit
// failure leaves the messages unacked (they redeliver / are re-read on replay) and
// the loop continues.
func (rp *ResolvedEventsProcessor) checkpoint(ctx context.Context) {
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

// finalCheckpoint flushes on a NORMAL shutdown so a clean stop commits the latest
// state and releases buffered acks. It is called only from the loop's normal exit
// paths — never from a deferred/panic-unwind path, so a mid-mutation panic can never
// commit a half-updated engine or ack the message that triggered it (the unacked
// message redelivers/replays instead). It uses a fresh, bounded context because the
// loop context is already cancelled by the time this runs.
func (rp *ResolvedEventsProcessor) finalCheckpoint() {
	if !rp.dirty && len(rp.pendingAcks) == 0 {
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
