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

const (
	// WORKER_COUNT is the number of notification dispatchers running in parallel.
	WORKER_COUNT = 5
	// MESSAGE_BACKLOG_SIZE bounds how many read alarm events wait to be dispatched.
	MESSAGE_BACKLOG_SIZE = 100
)

// NotificationProcessor is a durable, load-balanced consumer of the alarm-events
// stream (ADR-041) that drives the alarm→human last mile (ADR-017). It is a second,
// independent consumer fanning out alongside the graphql-ws alarm subscription: the
// subscription is an ephemeral live feed to a connected operator, while this is a
// durable pull consumer so an alarm that fires while the service is briefly down is
// still delivered once it comes back (a critical alarm must not page nobody because
// a pod was restarting). The shared durable name (per instance + area, not per
// replica) makes JetStream deliver each event to exactly one replica, so scaling out
// does not double-send.
//
// Like the device-state processor, a single read loop only reads messages and hands
// them to a pool of workers (E6); each worker decodes one event and hands it to the
// Notifier. The A3 ack/nak contract rides on each messaging.Message, so the worker
// that dispatches an event is the one that acks (success / poison) or naks
// (transient) it.
type NotificationProcessor struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Notifier     Notifier

	// RED metrics for the per-message dispatch path (E13).
	metrics *core.ProcessorMetrics

	messages chan messaging.Message

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups let
	// ExecuteStop drain the reader before closing the channel it feeds, so the reader
	// can never send on a closed channel at SIGTERM, and the workers drain the
	// remaining backlog to completion before exiting.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewNotificationProcessor creates a notification processor over the given reader
// and notifier.
func NewNotificationProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, notifier Notifier) *NotificationProcessor {
	np := &NotificationProcessor{
		Microservice: ms,
		Reader:       reader,
		Notifier:     notifier,
		metrics:      ms.NewProcessorMetrics("notify"),
	}
	npname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "notify-proc")
	np.lifecycle = core.NewLifecycleManager(npname, np, callbacks)
	return np
}

// Initialize component.
func (np *NotificationProcessor) Initialize(ctx context.Context) error {
	return np.lifecycle.Initialize(ctx)
}

// ExecuteInitialize runs initialization logic.
func (np *NotificationProcessor) ExecuteInitialize(ctx context.Context) error {
	// Derive the cancelable context the read loop runs under (A5).
	np.procCtx, np.procCancel = context.WithCancel(ctx)
	np.initializeWorkers()
	return nil
}

// initializeWorkers starts the pool of dispatch workers.
func (np *NotificationProcessor) initializeWorkers() {
	np.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	for w := 1; w <= WORKER_COUNT; w++ {
		np.workerWG.Add(1)
		// Workers run on a background context (not the cancelable read context) so
		// that on shutdown they drain the remaining buffered messages to completion
		// and ack them rather than aborting in-flight dispatches.
		go func() {
			defer np.workerWG.Done()
			np.processMessages(context.Background())
		}()
	}
}

// Start component.
func (np *NotificationProcessor) Start(ctx context.Context) error {
	return np.lifecycle.Start(ctx)
}

// ExecuteStart starts the read loop feeding the worker pool.
func (np *NotificationProcessor) ExecuteStart(ctx context.Context) error {
	np.readerWG.Add(1)
	go func() {
		defer np.readerWG.Done()
		for {
			if eof := np.ProcessMessage(np.procCtx); eof {
				break
			}
		}
	}()
	return nil
}

// ProcessMessage reads one alarm event and hands it to the worker pool. Returns true
// once the stream EOFs or the loop is shutting down.
func (np *NotificationProcessor) ProcessMessage(ctx context.Context) bool {
	msg, err := np.Reader.ReadMessage(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			log.Info().Msg("Detected EOF on alarm events stream")
			return true
		}
		np.Reader.HandleResponse(err)
		return false
	}

	// Hand off to the workers, but abandon the handoff on shutdown so the loop can
	// exit instead of blocking on a full channel (A5). The message is unacked, so it
	// is redelivered after restart.
	select {
	case np.messages <- msg:
	case <-ctx.Done():
		return true
	}
	return false
}

// processMessages is the worker loop: it drains the messages channel and dispatches
// each alarm event. The A3 ack/nak contract rides on each messaging.Message.
func (np *NotificationProcessor) processMessages(ctx context.Context) {
	for msg := range np.messages {
		np.dispatchOne(ctx, msg)
	}
	log.Debug().Msg("Notification dispatcher received shutdown signal.")
}

// dispatchOne decodes a single alarm state-change event and hands it to the Notifier,
// applying the A3 message disposition.
func (np *NotificationProcessor) dispatchOne(ctx context.Context, msg messaging.Message) {
	// RED metrics for this dispatch (E13): time the message and record its
	// disposition exactly once on whichever path it leaves by.
	done := np.metrics.Start()

	// Derive the per-message tenant from the message subject and build a tenant-scoped
	// context. Without a parseable tenant the notification cannot be routed safely
	// (fail-closed), so the message is dropped rather than redelivered.
	msgctx, _, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping alarm event with no parseable tenant in subject %q", msg.Subject))
		// Poison: redelivery will not make the tenant parseable, so ack to drop it.
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// Decode the alarm state-change envelope. An unparseable message will never
	// parse on redelivery, so it is dropped rather than looped.
	event, err := dmproto.UnmarshalAlarmStateChangeEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Msg(fmt.Sprintf("Skipping alarm event that could not be parsed from subject %q", msg.Subject))
		msg.Ack()
		done(core.ResultInvalid)
		return
	}

	// Dispatch to the notifier. A returned error is transient (a channel hiccup):
	// nak for redelivery until the finite MaxDeliver cap, then give up so a stuck
	// notification does not loop forever.
	if err := np.Notifier.Notify(msgctx, event); err != nil {
		log.Error().Err(err).Str("correlation", msg.CorrelationID()).Str("alarm", event.AlarmToken).Msg("Notification dispatch failed")
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Str("correlation", msg.CorrelationID()).Str("alarm", event.AlarmToken).Msg(fmt.Sprintf("Giving up on notification after %d attempts", msg.NumDelivered))
			msg.Ack()
			done(core.ResultFailed)
		} else {
			msg.Nak()
			done(core.ResultRetry)
		}
		return
	}

	// Delivered: ack so the event is not redelivered.
	msg.Ack()
	done(core.ResultOK)
}

// Stop component.
func (np *NotificationProcessor) Stop(ctx context.Context) error {
	return np.lifecycle.Stop(ctx)
}

// ExecuteStop unwinds the pipeline in dependency order so no goroutine ever sends on
// a closed channel (A5): stop the reader, then close the channel it feeds, then wait
// for the workers to drain the backlog and exit.
func (np *NotificationProcessor) ExecuteStop(context.Context) error {
	if np.procCancel != nil {
		np.procCancel()
	}
	np.readerWG.Wait() // reader stopped: no more sends to messages
	close(np.messages) //
	np.workerWG.Wait() // workers drained + exited
	return nil
}

// Terminate component.
func (np *NotificationProcessor) Terminate(ctx context.Context) error {
	return np.lifecycle.Terminate(ctx)
}

// ExecuteTerminate runs termination logic.
func (np *NotificationProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
