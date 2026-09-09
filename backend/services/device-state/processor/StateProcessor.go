// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

const (
	WORKER_COUNT         = 5   // Number of state mergers running in parallel
	MESSAGE_BACKLOG_SIZE = 100 // Number of messages that can be read and waiting to be processed
)

// StateProcessor is a second, independent consumer of the resolved-events stream
// (fan-out alongside event-management's persistence). For every resolved event it
// updates the originating device's live state projection, and a background
// monitor flips devices to inactive after an inactivity timeout.
//
// Like event-management's persistence processor, the single read loop only reads
// messages and hands them to a pool of workers (E6); the workers run the actual
// MergeDeviceState merge in parallel so projection throughput is not serialized.
// Per-device coalescing across workers is safe because MergeDeviceState is
// idempotent. Each message carries its own NATS ack handle, so the ack/leave-unacked
// disposition (A3) is performed by the worker that merges it.
type StateProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	Api                  model.DeviceStateApi

	// RED metrics for the per-message merge path (E13).
	metrics *core.ProcessorMetrics

	messages chan messaging.Message

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain the reader before closing the channel it feeds, so the
	// reader can never send on a closed channel at SIGTERM, and the workers drain
	// the remaining backlog to completion before exiting.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup

	// monitorWG tracks the inactivity monitor so ExecuteStop can WAIT for it rather than
	// merely signalling it. Closing quit stops the NEXT sweep from starting; it says
	// nothing about the one already running. See ExecuteStop.
	monitorWG sync.WaitGroup

	// inactivityInterval is the monitor's cadence, and ZERO MEANS "use the platform
	// default". There is no operator knob behind it — the constant in config is the only
	// production value — so it is unexported and the only thing that sets it is a test:
	// what has to hold at shutdown is that ExecuteStop does not return while a sweep is
	// still running, and a test cannot put a sweep in flight if it must first wait out a
	// one-minute tick.
	inactivityInterval time.Duration

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. Built on first use by pacer(), because this struct is also
	// assembled by literal in tests that never run the constructor.
	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// pacer returns the read loop's error pacer, building it on first use. It is touched only
// by the single read goroutine, which is the pacer's own contract.
func (sp *StateProcessor) pacer() *core.ReadPacer {
	if sp.readPacer == nil {
		sp.readPacer = core.NewReadPacer(sp.Microservice, "resolved events")
	}
	return sp.readPacer
}

// NewStateMetrics builds this processor's RED instrumentation.
//
// 🔴 IT IS SEPARATE FROM THE CONSTRUCTOR BECAUSE THE TWO RUN IN DIFFERENT PHASES.
// The processor is built inside the NATS manager's oncreate callback, which runs on
// EVERY start — it has to, because the processor holds a reader bound to the
// connection — while a Prometheus collector may be registered only once or the second
// registration panics. So the caller builds this in the INITIALIZE phase and hands the
// same instruments to every start's processor.
func NewStateMetrics(ms *core.Microservice) *core.ProcessorMetrics {
	return ms.NewProcessorMetrics("state")
}

// Create a new device-state processor.
//
// metrics is built once in the initialize phase (see NewStateMetrics) and shared by
// every processor this service constructs, because this constructor runs again on
// every start.
func NewStateProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api model.DeviceStateApi,
	metrics *core.ProcessorMetrics) *StateProcessor {
	sp := &StateProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		Api:                  api,
		metrics:              metrics,
	}

	// Create lifecycle manager.
	spname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "state-proc")
	sp.lifecycle = core.NewLifecycleManager(spname, sp, callbacks)
	return sp
}

// Initialize component.
func (sp *StateProcessor) Initialize(ctx context.Context) error {
	return sp.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (sp *StateProcessor) ExecuteInitialize(ctx context.Context) error {
	sp.quit = make(chan struct{})

	// Derive the cancelable context the read loop runs under (A5).
	sp.procCtx, sp.procCancel = context.WithCancel(ctx)

	// Initialize the pool of state-merge workers.
	sp.initializeWorkers()
	return nil
}

// Initialize pool of workers that merge device state in parallel.
func (sp *StateProcessor) initializeWorkers() {
	sp.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	for w := 1; w <= WORKER_COUNT; w++ {
		sp.workerWG.Add(1)
		// Workers run on a background context (not the cancelable read context)
		// so that on shutdown they drain the remaining buffered messages to
		// completion and ack them rather than aborting in-flight merges.
		go func() {
			defer sp.workerWG.Done()
			sp.processMessages(context.Background())
		}()
	}
}

// inactivityRecheckInterval is the monitor's cadence, falling back to the platform
// default. See the field for why it is settable at all.
func (sp *StateProcessor) inactivityRecheckInterval() time.Duration {
	if sp.inactivityInterval > 0 {
		return sp.inactivityInterval
	}
	return config.InactivityRecheckInterval * time.Second
}

// Start component.
func (sp *StateProcessor) Start(ctx context.Context) error {
	return sp.lifecycle.Start(ctx)
}

// Execute the read side of the processing loop. Reads one resolved event from the
// stream and hands it to the worker pool. Runs in a goroutine since it loops
// indefinitely. Returns true once the stream EOFs, the loop is being shut down, or a run of
// non-EOF read errors has outlasted the pacer's budget — in which case the pacer has already
// ended the process, because a projection that cannot read its source stream goes stale
// silently and a restart is the remedy for most of what causes it.
func (sp *StateProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := sp.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream")
			return true
		}
		sp.ResolvedEventsReader.HandleResponse(err)
		return sp.pacer().PauseAfterError(ctx, err)
	}
	sp.pacer().Succeeded()

	// Hand off to the workers, but abandon the handoff on shutdown so the loop
	// can exit instead of blocking on a full channel (A5). The message is unacked,
	// so it is redelivered after restart.
	select {
	case sp.messages <- msg:
	case <-ctx.Done():
		return true
	}
	return false
}

// processMessages is the worker loop: it drains the messages channel and merges
// each event's originating device state. The A3 ack contract rides on each
// messaging.Message, so the worker that performs the merge is the one that acks
// (success / poison) or leaves it unacked for redelivery (transient).
func (sp *StateProcessor) processMessages(ctx context.Context) {
	for msg := range sp.messages {
		sp.mergeOne(ctx, msg)
	}
	log.Debug().Msg("Device state merger received shutdown signal.")
}

// mergeOne updates the originating device's live state projection for a single
// resolved event and applies the A3 message disposition.
func (sp *StateProcessor) mergeOne(ctx context.Context, msg messaging.Message) {
	// RED metrics for this merge (E13): time the message and record its
	// disposition exactly once on whichever path it leaves by.
	done := sp.metrics.Start()

	// Derive the per-message tenant from the message subject and build a
	// tenant-scoped context. Without a parseable tenant the state can not be
	// updated safely (fail-closed) so the message is skipped.
	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", msg.Subject))
		// Poison message: redelivery will not make the tenant parseable, so ack
		// to drop it rather than redeliver up to MaxDeliver times.
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// Attempt to unmarshal the resolved event. A projection has no failed-events
	// channel, so an unparseable message is logged and dropped.
	event, err := dmproto.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping resolved event that could not be parsed from subject %q", msg.Subject))
		// Poison message: redelivery will not make it parseable, so ack to drop.
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// disposeTransient applies the A3 retry-or-drop disposition for a transient
	// (retryable) projection-write error: redeliver until the finite MaxDeliver
	// cap, then give up (the projection is reconstructable, so dropping beats
	// looping forever). Shared by every write below.
	disposeTransient := func(err error, detail string) {
		log.Error().Err(err).Str("correlation", msg.CorrelationID()).Msg(detail)
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Giving up on %s after %d attempts", detail, msg.NumDelivered))
			msg.Ack()
			// 🔑 "dropped", NOT "failed". This projection has no dead-letter path and wants
			// none: it is rebuildable from the event history, so a state change nobody could
			// apply is discarded rather than kept for inspection. Reporting it as "failed"
			// would put it in the same bucket as the consumers that DO record what they gave
			// up on, and an operator counting dead letters would be counting this too.
			done(core.ResultDropped)
		} else {
			// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
			// an immediate nak would burn MaxDeliver in ~1.4ms inside an outage.
			// Reference disposition: event-sources' settler (ADR-030).
			done(core.ResultRetry)
		}
	}

	pt, err := presenceTransitionFor(event)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping unmappable state-change event for device %s", event.SourceDeviceToken))
		// Poison message: redelivery cannot make the state mappable. Dropping it is the
		// only safe disposition — folding it as a plain data event would let an
		// unreadable presence claim act as a liveness heartbeat.
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// Update the originating device's live connectivity projection for every event.
	if _, err := sp.Api.MergeDeviceState(msgctx, event.SourceDeviceToken, event.OccurredTime, pt,
		model.DeviceIdentity{ExternalId: event.ExternalId, Source: event.Source}); err != nil {
		disposeTransient(err, fmt.Sprintf("device state projection update for device %s", event.SourceDeviceToken))
		return
	}

	// For a measurement event, also advance the per-key latest-value projection.
	if event.EventType == esmodel.Measurement {
		if err := sp.mergeLatestMeasurements(msgctx, event); err != nil {
			disposeTransient(err, fmt.Sprintf("latest-measurement projection update for device %s", event.SourceDeviceToken))
			return
		}
	}

	// For a location event, also advance the last-known-position projection. Note this
	// is IN ADDITION TO the MergeDeviceState above, not instead of it: a location is a
	// data event like any other, so it is still a liveness heartbeat. A device that
	// only ever reports its position must not be swept inactive for saying nothing.
	if event.EventType == esmodel.Location {
		if err := sp.mergeLatestLocation(msgctx, event); err != nil {
			disposeTransient(err, fmt.Sprintf("latest-location projection update for device %s", event.SourceDeviceToken))
			return
		}
	}

	// Projection updated successfully: ack so the message is not redelivered.
	msg.Ack()
	done(core.ResultOK)
}

// mergeLatestMeasurements extracts the numeric measurements from a resolved
// measurement event and upserts each into the per-key latest-value projection.
// Non-numeric values are skipped (v1 is numeric-only). Each sample is stamped at ITS
// OWN time, which the resolver already decided and bounded — this reads the entry's
// time and never falls back to the envelope or re-clamps, so the projection cannot
// disagree with the history table about the same reading. A measurement event whose
// payload is not the expected shape is skipped (connectivity was already updated)
// rather than treated as a retryable error.
func (sp *StateProcessor) mergeLatestMeasurements(ctx context.Context, event *dmmodel.ResolvedEvent) error {
	payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload)
	if !ok {
		return nil
	}
	inputs := make([]model.LatestMeasurementInput, 0)
	for _, entry := range payload.Entries {
		occurredAt := entry.OccurredTime
		for _, mx := range entry.Entries {
			f, err := strconv.ParseFloat(mx.Value, 64)
			if err != nil {
				// Not a numeric reading: outside the v1 latest-value projection.
				continue
			}
			var classifier *uint
			if mx.Classifier != nil {
				c := uint(*mx.Classifier)
				classifier = &c
			}
			inputs = append(inputs, model.LatestMeasurementInput{
				Name:         mx.Name,
				Value:        sql.NullFloat64{Float64: f, Valid: true},
				Classifier:   classifier,
				Unit:         mx.Unit,
				DataType:     mx.DataType,
				OccurredTime: occurredAt,
			})
		}
	}
	return sp.Api.MergeLatestMeasurements(ctx, event.SourceDeviceToken, inputs)
}

// nullableFloat parses one optional coordinate off a resolved location entry. The
// values arrive as strings and were already range-checked at decode, so a value that
// is absent OR unparseable here yields NULL rather than a zero: a projection column
// reading 0.0 is a claim (the equator, sea level, stationary, due north), and NULL is
// the only honest encoding of "the device did not report this".
func nullableFloat(val *string) sql.NullFloat64 {
	if val == nil {
		return sql.NullFloat64{}
	}
	f, err := strconv.ParseFloat(*val, 64)
	if err != nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

// mergeLatestLocation extracts the fixes from a resolved location event and upserts
// them into the last-known-position projection. Each fix is stamped at ITS OWN time,
// already decided and bounded at resolution, mirroring the measurement path — and that
// time is also the ordering key the merge's newer-wins guard compares on, which is why
// it must arrive bounded: an unbounded future value would win forever and no real fix
// could ever supersede it.
//
// An entry carrying neither a parseable latitude nor a parseable longitude is skipped:
// it locates the device nowhere, so storing it would replace a real position with an
// empty one and stamp it as the newest fix. A location event whose payload is not the
// expected shape is skipped entirely (connectivity was already updated) rather than
// treated as a retryable error — redelivery cannot change its shape.
func (sp *StateProcessor) mergeLatestLocation(ctx context.Context, event *dmmodel.ResolvedEvent) error {
	payload, ok := event.Payload.(*dmmodel.ResolvedLocationsPayload)
	if !ok {
		return nil
	}
	inputs := make([]model.LatestLocationInput, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		in := model.LatestLocationInput{
			Latitude:     nullableFloat(entry.Latitude),
			Longitude:    nullableFloat(entry.Longitude),
			Elevation:    nullableFloat(entry.Elevation),
			Accuracy:     nullableFloat(entry.Accuracy),
			Speed:        nullableFloat(entry.Speed),
			Heading:      nullableFloat(entry.Heading),
			OccurredTime: entry.OccurredTime,
		}
		if !in.Latitude.Valid && !in.Longitude.Valid {
			continue
		}
		inputs = append(inputs, in)
	}
	return sp.Api.MergeLatestLocations(ctx, event.SourceDeviceToken, inputs)
}

// Lifecycle callback that runs startup logic.
func (sp *StateProcessor) ExecuteStart(ctx context.Context) error {
	// Read loop for inbound resolved events. Runs under the cancelable context so
	// ExecuteStop can stop it before the channel it feeds is closed.
	sp.readerWG.Add(1)
	go func() {
		defer sp.readerWG.Done()
		for {
			eof := sp.ProcessMessage(sp.procCtx)
			if eof {
				break
			}
		}
	}()

	// Background inactivity monitor, tracked so ExecuteStop can join it.
	//
	// 🔴 THE Add IS OUTSIDE THE `go` ON PURPOSE, as it is for readerWG and workerWG
	// above. sync.WaitGroup requires that an Add raising the counter from zero
	// happen-before the Wait meant to hold for it; an Add moved inside the goroutine can be
	// scheduled after Wait has already seen a zero counter and returned, which is the
	// "clean stop reported over work still running" this join exists to prevent.
	sp.monitorWG.Add(1)
	go func() {
		defer sp.monitorWG.Done()
		sp.runInactivityMonitor(ctx)
	}()
	return nil
}

// runInactivityMonitor periodically flips devices that have gone quiet to inactive. The
// sweep runs under a system context so it spans all tenants in a single pass (each row
// keeps its own tenant_id on save).
//
// Extracted from ExecuteStart so both the cadence and the shutdown behaviour are
// reachable: inline, the only way to put a sweep in flight was to wait out a real tick.
func (sp *StateProcessor) runInactivityMonitor(ctx context.Context) {
	ticker := time.NewTicker(sp.inactivityRecheckInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sp.quit:
			return
		case <-ticker.C:
			count, err := sp.Api.SweepInactive(core.WithSystemContext(ctx), time.Now())
			if err != nil {
				log.Error().Err(err).Msg("Inactivity sweep failed")
			} else if count > 0 {
				log.Info().Msg(fmt.Sprintf("Inactivity monitor marked %d device(s) inactive", count))
			}
		}
	}
}

// Stop component.
func (sp *StateProcessor) Stop(ctx context.Context) error {
	return sp.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic. It unwinds the pipeline in
// dependency order so no goroutine ever sends on a closed channel (A5): stop the
// reader, then close the channel it feeds, then wait for the workers it feeds to
// drain the backlog and exit. The inactivity monitor is stopped independently.
//
// 🔴 IT WAITS FOR THE MONITOR, AND SIGNALLING IT IS NOT THE SAME THING. Closing quit
// stops the next sweep from starting; it says nothing about the one already running. The
// caller is beforeMicroserviceStopped, which goes on to stop the RdbManager — and the pool
// is closed in RdbManager.ExecuteTerminate. Without the join, a sweep that was mid-flight
// when quit closed is still issuing queries when its pool closes, from a service that has
// already reported a clean stop.
//
// 🔑 THE WAIT IS BOUNDED BY THE ROOT CANCELLATION THAT ALREADY HAPPENED. Microservice
// shutdown cancels the root context BEFORE calling Stop, and that root context is the one
// ExecuteStart handed to the monitor, so a sweep in flight is already unwinding against a
// cancelled context by the time this wait begins. No deadline is taken from ExecuteStop's
// own context because it is context.Background(): a bounded wait on it could never fire.
func (sp *StateProcessor) ExecuteStop(context.Context) error {
	if sp.procCancel != nil {
		sp.procCancel()
	}
	sp.readerWG.Wait() // reader stopped: no more sends to messages
	close(sp.messages) //
	sp.workerWG.Wait() // workers drained + exited

	close(sp.quit)      // stop the inactivity monitor
	sp.monitorWG.Wait() // ...and wait for a sweep it may have been in the middle of
	return nil
}

// Terminate component.
func (sp *StateProcessor) Terminate(ctx context.Context) error {
	return sp.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (sp *StateProcessor) ExecuteTerminate(context.Context) error {
	return nil
}

// presenceTransitionFor maps a resolved event onto an authoritative presence transition
// (ADR-067), or nil when the event is a plain data heartbeat. The transition time is the
// event's occurred time — one canonical clock, set by the producer.
//
// 🔴 IT RETURNS AN ERROR RATHER THAN A nil, and the difference is not cosmetic. A nil
// here does not mean "nothing to do": it means "plain data event", which MergeDeviceState
// folds as an implicit heartbeat that advances LastActivityTime. So a StateChange this
// function could not map would arrive at the projection disguised as a heartbeat and
// keep an asserted device looking alive on the strength of an event nobody could read.
// An unmappable presence state is a deterministic producer defect; it is dropped as a
// poison message, loudly.
//
// 🔑 EXTRACTED SO THE SEAM CAN BE TESTED AS THE CALLER, NOT RE-IMPLEMENTED BY A TEST.
// This is a hand-written field-by-field mapping at the end of a chain of four other
// hand-written mappings, which makes it exactly the kind of place a newly added field is
// silently dropped: every layer still compiles, every existing test still passes, and the
// field simply never arrives. A test that rebuilt this mapping itself would agree with
// whatever the mapping forgot. See TestARegressedSessionSurvivesTheWholeChain.
func presenceTransitionFor(event *dmmodel.ResolvedEvent) (*model.PresenceTransition, error) {
	if event.EventType != esmodel.StateChange {
		return nil, nil
	}
	p, ok := event.Payload.(*dmmodel.ResolvedStateChangePayload)
	if !ok {
		return nil, fmt.Errorf("state-change event carries a %T payload", event.Payload)
	}
	claim, ok := p.Claim()
	if !ok {
		return nil, fmt.Errorf("state-change event carries unmappable presence state %q", p.State)
	}
	return &model.PresenceTransition{
		Claim:             claim,
		SessionId:         p.SessionId,
		ExpectedSessionId: p.ExpectedSessionId,
		OccurredAt:        event.OccurredTime,
	}, nil
}
