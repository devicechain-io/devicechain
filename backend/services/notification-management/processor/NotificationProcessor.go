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
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
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
// Notifier. The A3 ack contract rides on each messaging.Message, so the worker
// that dispatches an event is the one that acks (success / poison) or leaves it
// unacked for redelivery (transient).
//
// The pool has no per-alarm partitioning, so transitions for one alarm can be
// dispatched out of order and (at-least-once) more than once. device-state tolerates
// this because its merge is idempotent and monotonic on OccurredTime; a Notifier has
// no such guard by default, so the ordering/idempotency burden is spelled out as the
// Notifier contract (notifier.go) for the real dispatcher to honor.
type NotificationProcessor struct {
	Microservice *core.Microservice
	Reader       messaging.MessageReader
	Notifier     Notifier

	// dead records an alarm that reached nobody (ADR-024). Nil when no dead-letter
	// writer is configured, in which case the notification is dropped as it was before.
	dead *deadletter.Sink
	// area names this service on the letters it writes. Carried rather than read off
	// Microservice at write time, because a dead letter is written on the failure path —
	// the one place a nil dereference turns a recoverable failure into a crash.
	area string
	// deadLettered and deadLetterLost are counted apart: the second is the only outcome
	// on this path where an alarm nobody was paged about leaves no record at all.
	deadLettered   prometheus.Counter
	deadLetterLost prometheus.Counter

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
	callbacks core.LifecycleCallbacks, notifier Notifier, dead deadletter.Writer) *NotificationProcessor {
	np := &NotificationProcessor{
		Microservice: ms,
		Reader:       reader,
		Notifier:     notifier,
		metrics:      ms.NewProcessorMetrics("notify"),
		deadLettered: ms.NewCounter("notifications_dead_lettered_total",
			"Alarms written to the dead-letter stream after every delivery attempt failed, so an "+
				"operator can see which pages were never sent (ADR-024).", nil),
		deadLetterLost: ms.NewCounter("notifications_dead_letter_lost_total",
			"Alarms that reached nobody AND could not be dead-lettered — the write failed on a "+
				"delivery that will not repeat. An alarm in this state is invisible everywhere.", nil),
	}
	np.area = ms.FunctionalArea
	if dead != nil {
		np.dead = deadletter.NewSink(dead, func(error) { np.deadLetterLost.Inc() })
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
// each alarm event. The A3 ack contract rides on each messaging.Message.
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

	// Dispatch to the notifier. The PolicyNotifier (N.C) owns a bounded in-line retry
	// per channel and returns an error ONLY when nothing was delivered (so a redelivery
	// can never double-send a channel that already succeeded); that error is transient, so
	// leave it unacked for AckWait-paced redelivery until the finite MaxDeliver cap.
	//
	// The give-up branch DEAD-LETTERS (ADR-024), which is what makes ResultFailed's
	// "dead-letter" label in core/metrics.go true for this consumer rather than
	// aspirational. The PolicyNotifier's in-line retry (attempts × timeout) is still the
	// primary reliability window and redelivery rides AckWait (~5 min across MaxDeliver,
	// ADR-030); the sink is what survives an outage longer than that. See notifier.go.
	if err := np.Notifier.Notify(msgctx, event); err != nil {
		log.Error().Err(err).Str("correlation", msg.CorrelationID()).Str("alarm", event.AlarmToken).Msg("Notification dispatch failed")
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Str("correlation", msg.CorrelationID()).Str("alarm", event.AlarmToken).
				Msgf("Dead-lettering notification after %d failed attempts; nobody was paged", msg.NumDelivered)
			np.deadLetter(msgctx, msg, event.AlarmToken, err)
			msg.Ack()
			done(core.ResultFailed)
		} else {
			// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
			// an immediate nak would burn MaxDeliver in ~1.4ms inside an outage.
			// Reference disposition: event-sources' settler (ADR-030).
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

// deadLetter records an alarm that reached nobody.
//
// 🔴 IT RUNS ONLY AT THE REDELIVERY CAP, so JetStream will not redeliver whatever this
// consumer does — leaving the message unacked would strand it rather than buy another
// attempt. The write therefore gets its own bounded in-process retries (core/deadletter),
// and one that still fails is counted as a LOSS instead of logged as a retry.
//
// The alarm token is the reference an operator searches by; the delivery error is the
// detail. Both are recorded, and what a reader is allowed to SEE of the detail is the read
// surface's decision, not this one's.
func (np *NotificationProcessor) deadLetter(ctx context.Context, msg messaging.Message,
	alarm string, cause error) {
	if np.dead == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	err := np.dead.Write(ctx, deadletter.Envelope{
		Kind:   deadletter.KindNotification,
		Reason: deadletter.ReasonExhausted,
		Source: np.area,
		Summary: "an alarm could not be delivered to any configured channel after every " +
			"delivery attempt, so nobody was paged about it",
		Detail:      detail,
		Attempts:    msg.NumDelivered,
		Subject:     msg.Subject,
		Sequence:    msg.StreamSeq,
		Correlation: msg.CorrelationID(),
		Reference:   alarm,
		OccurredAt:  time.Now().UTC(),
		Payload:     msg.Value,
	})
	if err != nil {
		// The counter moves in the sink's loss hook, not here — see deadletter.Sink.
		log.Error().Err(err).Str("alarm", alarm).
			Msg("LOST notification: the alarm reached nobody and could not be dead-lettered.")
		return
	}
	if np.deadLettered != nil {
		np.deadLettered.Inc()
	}
}
