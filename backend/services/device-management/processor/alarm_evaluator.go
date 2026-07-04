// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

const (
	ALARM_WORKER_COUNT         = 5   // parallel alarm evaluators
	ALARM_MESSAGE_BACKLOG_SIZE = 100 // resolved-event messages buffered ahead of the workers
)

// AlarmEvaluator is the discrete NATS consumer that runs the SIMPLE alarm evaluator
// (ADR-041). It reads the same resolved-events stream device-management produces
// (its own output, consumed back), and for each resolved MEASUREMENT event evaluates
// the originating device's alarm rules and upserts the resulting alarm state. It is a
// separate consumer from the persistence/state pipelines by design: alarm evaluation
// must not add latency to, or share a failure fate with, event resolution.
//
// Unlike the event-persistence consumer there is no dead-letter path: alarm state is
// a projection of the measurement stream, so a message that repeatedly fails to
// evaluate is dropped after the redelivery cap — the next measurement re-derives the
// correct state. This keeps a transient DB blip from wedging the stream while never
// persisting a bad alarm.
type AlarmEvaluator struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader
	Api                  model.DeviceManagementApi

	messages chan messaging.Message
	metrics  *core.ProcessorMetrics

	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewAlarmEvaluator creates the alarm-evaluation consumer over the resolved-events
// reader.
func NewAlarmEvaluator(ms *core.Microservice, resolved messaging.MessageReader,
	callbacks core.LifecycleCallbacks, api model.DeviceManagementApi) *AlarmEvaluator {
	ae := &AlarmEvaluator{
		Microservice:         ms,
		ResolvedEventsReader: resolved,
		Api:                  api,
		metrics:              ms.NewProcessorMetrics("alarm-eval"),
	}
	name := fmt.Sprintf("%s-%s", ms.FunctionalArea, "alarm-eval-proc")
	ae.lifecycle = core.NewLifecycleManager(name, ae, callbacks)
	return ae
}

// Initialize component.
func (ae *AlarmEvaluator) Initialize(ctx context.Context) error {
	return ae.lifecycle.Initialize(ctx)
}

// ExecuteInitialize builds the cancelable read context and the worker pool.
func (ae *AlarmEvaluator) ExecuteInitialize(ctx context.Context) error {
	ae.procCtx, ae.procCancel = context.WithCancel(ctx)
	ae.messages = make(chan messaging.Message, ALARM_MESSAGE_BACKLOG_SIZE)
	for w := 1; w <= ALARM_WORKER_COUNT; w++ {
		ae.workerWG.Add(1)
		// Workers run on a background context (not the cancelable read context) so
		// on shutdown they drain the buffered messages to completion rather than
		// abandoning an in-flight evaluation.
		go func(workerId int) {
			defer ae.workerWG.Done()
			ae.processLoop(context.Background(), workerId)
		}(w)
	}
	return nil
}

// Start component.
func (ae *AlarmEvaluator) Start(ctx context.Context) error {
	return ae.lifecycle.Start(ctx)
}

// ExecuteStart launches the read loop that feeds the workers.
func (ae *AlarmEvaluator) ExecuteStart(ctx context.Context) error {
	ae.readerWG.Add(1)
	go func() {
		defer ae.readerWG.Done()
		for {
			if eof := ae.readMessage(ae.procCtx); eof {
				return
			}
		}
	}()
	return nil
}

// readMessage reads one resolved-event message and hands it to a worker. It returns
// true when the stream is exhausted or shutting down.
func (ae *AlarmEvaluator) readMessage(ctx context.Context) bool {
	msg, err := ae.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream (alarm evaluator)")
			return true
		}
		ae.ResolvedEventsReader.HandleResponse(err)
		return false
	}
	select {
	case ae.messages <- msg:
	case <-ctx.Done():
		// Abandon the handoff on shutdown; the message is unacked and redelivered.
		return true
	}
	return false
}

// processLoop is one worker: it evaluates messages until the channel is closed.
func (ae *AlarmEvaluator) processLoop(ctx context.Context, workerId int) {
	for msg := range ae.messages {
		ae.evaluateMessage(ctx, workerId, msg)
	}
	log.Debug().Int("worker", workerId).Msg("Alarm evaluator received shutdown signal")
}

// evaluateMessage evaluates a single resolved-event message and acks/naks it. Only
// MEASUREMENT events carry alarm-relevant data; every other event type is acked and
// skipped. A message with no parseable tenant or an unparseable body is a poison
// message (redelivery cannot help) and is dropped. A transient evaluation failure is
// naked for redelivery up to the cap, then dropped — the next measurement re-derives
// the state.
func (ae *AlarmEvaluator) evaluateMessage(ctx context.Context, workerId int, msg messaging.Message) {
	done := ae.metrics.Start()

	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Msgf("Alarm evaluator dropping message with no parseable tenant in subject %q", msg.Subject)
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	event, err := proto.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Msg("Alarm evaluator dropping unparseable resolved event")
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	if event.EventType != esmodel.Measurement {
		msg.Ack()
		done(core.ResultOK)
		return
	}

	payload, ok := event.Payload.(*model.ResolvedMeasurementsPayload)
	if !ok {
		log.Warn().Msg("Alarm evaluator dropping measurement event with non-measurement payload")
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	if err := ae.Api.EvaluateMeasurementAlarms(msgctx, event.SourceDeviceId, payload, event.OccurredTime); err != nil {
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Err(err).Int("worker", workerId).
				Msg("Alarm evaluation failed past redelivery cap; dropping (state re-derives from next measurement)")
			msg.Ack()
			done(core.ResultFailed)
			return
		}
		log.Warn().Err(err).Int("worker", workerId).Msg("Alarm evaluation failed; will retry on redelivery")
		msg.Nak()
		done(core.ResultRetry)
		return
	}

	msg.Ack()
	done(core.ResultOK)
}

// Stop component.
func (ae *AlarmEvaluator) Stop(ctx context.Context) error {
	return ae.lifecycle.Stop(ctx)
}

// ExecuteStop unwinds the pipeline in dependency order so no goroutine ever sends on
// a closed channel: stop the reader, then close the channel it feeds, then wait for
// the workers to drain it.
func (ae *AlarmEvaluator) ExecuteStop(context.Context) error {
	if ae.procCancel != nil {
		ae.procCancel()
	}
	ae.readerWG.Wait()
	close(ae.messages)
	ae.workerWG.Wait()
	return nil
}

// Terminate component.
func (ae *AlarmEvaluator) Terminate(ctx context.Context) error {
	return ae.lifecycle.Terminate(ctx)
}

// ExecuteTerminate is a no-op; the reader is owned by the NATS manager.
func (ae *AlarmEvaluator) ExecuteTerminate(context.Context) error {
	return nil
}
