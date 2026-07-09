// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// ResolvedEventsProcessor is event-processing's tap on the resolved-events stream
// (ADR-051): a third, independent consumer fanning out alongside event-management's
// persistence and device-state's projection. In this scaffold slice it consumes
// and DROPS every event — the DETECT engine is wired in a later slice. Its purpose
// now is to stand up the durable consumer end-to-end (bound + self-healing per the
// #209 discipline, inherited from the core reader) and to exercise the new
// denormalized rule-scoping tokens on the wire (ADR-051).
//
// A single read loop reads one message at a time and acks it. There is no
// worker pool yet: dropping is trivial and must not fall behind. Each message
// carries its own ack handle, so the disposition rides on the message.
type ResolvedEventsProcessor struct {
	Microservice         *core.Microservice
	ResolvedEventsReader messaging.MessageReader

	// RED metrics for the per-message path (ADR-022 review E13).
	metrics *core.ProcessorMetrics

	// Shutdown coordination (A5): procCancel stops the read loop; readerWG lets
	// ExecuteStop wait for the loop to exit before the reader is torn down.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewResolvedEventsProcessor creates a new resolved-events processor.
func NewResolvedEventsProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks) *ResolvedEventsProcessor {
	rp := &ResolvedEventsProcessor{
		Microservice:         ms,
		ResolvedEventsReader: reader,
		metrics:              ms.NewProcessorMetrics("detect"),
	}
	rpname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "resolved-events-proc")
	rp.lifecycle = core.NewLifecycleManager(rpname, rp, callbacks)
	return rp
}

// Initialize component.
func (rp *ResolvedEventsProcessor) Initialize(ctx context.Context) error {
	return rp.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (rp *ResolvedEventsProcessor) ExecuteInitialize(ctx context.Context) error {
	rp.procCtx, rp.procCancel = context.WithCancel(ctx)
	return nil
}

// Start component.
func (rp *ResolvedEventsProcessor) Start(ctx context.Context) error {
	return rp.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic: the read loop, under the cancelable
// context so ExecuteStop can stop it cleanly.
func (rp *ResolvedEventsProcessor) ExecuteStart(context.Context) error {
	rp.readerWG.Add(1)
	go func() {
		defer rp.readerWG.Done()
		for {
			if rp.processMessage(rp.procCtx) {
				break
			}
		}
	}()
	return nil
}

// processMessage reads one resolved event and drops it (acks). Returns true once
// the stream EOFs or the loop is being shut down.
func (rp *ResolvedEventsProcessor) processMessage(ctx context.Context) bool {
	msg, err := rp.ResolvedEventsReader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on resolved events stream")
			return true
		}
		rp.ResolvedEventsReader.HandleResponse(err)
		return false
	}
	rp.dropOne(ctx, msg)
	return false
}

// dropOne acks a single resolved event. It parses the event only to log its
// denormalized rule-scoping tokens (ADR-051) at debug — proving the DETECT engine
// will have what it needs to scope rules read-free once it lands. A parse failure
// is logged and dropped: this consumer has no dead-letter path (a projection/tap,
// not the resolver).
func (rp *ResolvedEventsProcessor) dropOne(ctx context.Context, msg messaging.Message) {
	// RED metrics for this drop (E13): time the message and record its disposition
	// exactly once on whichever path it leaves by.
	done := rp.metrics.Start()

	// Fail-closed on an unparseable tenant: redelivery cannot help, so ack to drop.
	if _, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject); !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", msg.Subject))
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	event, err := dmproto.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		// Poison: redelivery cannot make it parseable, and this tap has no
		// dead-letter path, so ack to drop. Recorded as invalid (not ok) so an
		// unreadable-event spike is visible in the RED metrics.
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msg(fmt.Sprintf("Dropping resolved event that could not be parsed from subject %q", msg.Subject))
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	log.Debug().Str("correlation", msg.CorrelationID()).
		Str("device", event.SourceDeviceToken).
		Str("deviceType", event.DeviceTypeToken).
		Str("profileVersion", event.ProfileVersionToken).
		Msg("Consumed resolved event (DETECT not yet wired; dropping)")

	// Scaffold slice: every event is dropped once observed.
	msg.Ack()
	done(core.ResultOK)
}

// Stop component.
func (rp *ResolvedEventsProcessor) Stop(ctx context.Context) error {
	return rp.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic: stop the read loop and wait for it
// to exit before the reader is torn down (A5).
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

// Lifecycle callback that runs termination logic.
func (rp *ResolvedEventsProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
