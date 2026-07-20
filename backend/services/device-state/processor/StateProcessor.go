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

	lifecycle core.LifecycleManager
	quit      chan struct{}
}

// Create a new device-state processor.
func NewStateProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api model.DeviceStateApi) *StateProcessor {
	sp := &StateProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		Api:                  api,
		metrics:              ms.NewProcessorMetrics("state"),
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

// Start component.
func (sp *StateProcessor) Start(ctx context.Context) error {
	return sp.lifecycle.Start(ctx)
}

// Execute the read side of the processing loop. Reads one resolved event from the
// stream and hands it to the worker pool. Runs in a goroutine since it loops
// indefinitely. Returns true once the stream EOFs or the loop is being shut down.
func (sp *StateProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := sp.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream")
			return true
		}
		sp.ResolvedEventsReader.HandleResponse(err)
		return false
	}

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
			done(core.ResultFailed)
		} else {
			// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
			// an immediate nak would burn MaxDeliver in ~1.4ms inside an outage.
			// Reference disposition: event-sources' settler (ADR-030).
			done(core.ResultRetry)
		}
	}

	// Update the originating device's live connectivity projection for every event.
	if _, err := sp.Api.MergeDeviceState(msgctx, event.SourceDeviceToken, event.OccurredTime); err != nil {
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

	// Projection updated successfully: ack so the message is not redelivered.
	msg.Ack()
	done(core.ResultOK)
}

// mergeLatestMeasurements extracts the numeric measurements from a resolved
// measurement event and upserts each into the per-key latest-value projection.
// Non-numeric values are skipped (v1 is numeric-only); a per-entry occurred time
// overrides the event's when present. A measurement event whose payload is not
// the expected shape is skipped (connectivity was already updated) rather than
// treated as a retryable error.
func (sp *StateProcessor) mergeLatestMeasurements(ctx context.Context, event *dmmodel.ResolvedEvent) error {
	payload, ok := event.Payload.(*dmmodel.ResolvedMeasurementsPayload)
	if !ok {
		return nil
	}
	inputs := make([]model.LatestMeasurementInput, 0)
	for _, entry := range payload.Entries {
		occurredAt := event.OccurredTime
		if entry.OccurredTime != nil {
			if t, err := time.Parse(time.RFC3339, *entry.OccurredTime); err == nil {
				occurredAt = t
			}
		}
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

	// Background inactivity monitor: periodically flip devices that have gone
	// quiet to inactive. The sweep runs under a system context so it spans all
	// tenants in a single pass (each row keeps its own tenant_id on save).
	go func() {
		ticker := time.NewTicker(config.InactivityRecheckInterval * time.Second)
		defer ticker.Stop()
		for {
			select {
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
	}()
	return nil
}

// Stop component.
func (sp *StateProcessor) Stop(ctx context.Context) error {
	return sp.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic. It unwinds the pipeline in
// dependency order so no goroutine ever sends on a closed channel (A5): stop the
// reader, then close the channel it feeds, then wait for the workers it feeds to
// drain the backlog and exit. The inactivity monitor is stopped independently.
func (sp *StateProcessor) ExecuteStop(context.Context) error {
	if sp.procCancel != nil {
		sp.procCancel()
	}
	sp.readerWG.Wait() // reader stopped: no more sends to messages
	close(sp.messages) //
	sp.workerWG.Wait() // workers drained + exited

	close(sp.quit) // stop the inactivity monitor
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
