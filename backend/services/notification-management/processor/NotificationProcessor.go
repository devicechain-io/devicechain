// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	// WORKER_COUNT is the number of notification dispatchers running in parallel. It is also
	// the alarm-events reader's capacity (messaging.ReaderWithCapacity, in main): the reader
	// holds no more alarms than there are dispatchers to start them, so none waits in a queue
	// while the broker's redelivery clock runs.
	WORKER_COUNT = 5
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
	// sink is configured, in which case the notification is dropped as it was before. The
	// sink stamps this service as the letter's source and counts a lost letter on the
	// process's dead_letter_lost_total.
	dead *deadletter.Sink
	// NotifyMetrics is EMBEDDED BY VALUE, and built ONCE in the initialize phase rather
	// than here. The processor itself is constructed inside the NATS manager's oncreate
	// callback, which is connection-scoped and is entered again by any start
	// retried after a failed one; a collector belongs to the PROCESS, and MustRegister
	// panics on the second registration.
	//
	// By value rather than by pointer because this struct is also assembled by literal in
	// tests that never run the constructor: a zero value gives them the all-nil
	// instruments the readers already tolerate, where a nil pointer would be dereferenced.
	NotifyMetrics

	messages chan messaging.Message

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups let
	// ExecuteStop drain the reader before closing the channel it feeds, so the reader
	// can never send on a closed channel at SIGTERM, and the workers drain the
	// remaining backlog to completion before exiting.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. Built on first use by pacer(), because this struct is also
	// assembled by literal in tests that never run the constructor.
	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
}

// pacer returns the read loop's error pacer, building it on first use. It is touched only
// by the single read goroutine, which is the pacer's own contract.
func (np *NotificationProcessor) pacer() *core.ReadPacer {
	if np.readPacer == nil {
		np.readPacer = core.NewReadPacer(np.Microservice, "alarm events")
	}
	return np.readPacer
}

// NotifyMetrics is every Prometheus instrument the notification processor exports.
//
// It is a type of its own so it can be built in a DIFFERENT PHASE from the processor
// that reads it. See NewNotifyMetrics.
type NotifyMetrics struct {
	// deadLettered counts alarms recorded as dead letters. The other outcome — a letter
	// that could not be written, the only one on this path where an alarm nobody was paged
	// about leaves no record at all — is counted by the dead-letter sink itself, on the
	// dead_letter_lost_total every producing service shares (deadletter.Producer).
	deadLettered prometheus.Counter

	// RED metrics for the per-message dispatch path (E13).
	metrics *core.ProcessorMetrics
}

// NewNotifyMetrics builds the notification processor's instruments.
//
// 🔴 CALL IT FROM THE INITIALIZE PHASE, WHICH RUNS ONCE. The processor is built inside
// the NATS manager's oncreate callback — it has to be, because it holds a reader bound
// to the connection — and that callback runs on EVERY start. A Prometheus collector
// built there belongs to the wrong lifetime: it is registered again whenever that
// callback is entered again — which any start retried after a failed one does — and
// MustRegister panics on the duplicate.
func NewNotifyMetrics(ms *core.Microservice) NotifyMetrics {
	return NotifyMetrics{
		metrics: ms.NewProcessorMetrics("notify"),
		deadLettered: ms.NewCounter("notifications_dead_lettered_total",
			"Alarms written to the dead-letter stream after every delivery attempt failed, so an "+
				"operator can see which pages were never sent (ADR-024)."),
	}
}

// NewNotificationProcessor creates a notification processor over the given reader
// and notifier.
//
// metrics is built once in the initialize phase (see NewNotifyMetrics) and shared by
// every processor this service constructs, because that callback is connection-scoped
// and the instruments are not.
func NewNotificationProcessor(ms *core.Microservice, reader messaging.MessageReader,
	callbacks core.LifecycleCallbacks, notifier Notifier, dead *deadletter.Sink,
	metrics NotifyMetrics) *NotificationProcessor {
	np := &NotificationProcessor{
		Microservice:  ms,
		Reader:        reader,
		Notifier:      notifier,
		NotifyMetrics: metrics,
		dead:          dead,
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
	// One place per dispatcher and no more: the reader already holds at most WORKER_COUNT
	// alarms, so a deeper channel would never fill — and a deeper one in front of a reader
	// WITHOUT capacity is how a burst came to sit past AckWait and be paged twice.
	np.messages = make(chan messaging.Message, WORKER_COUNT)
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
		np.readLoop(np.procCtx)
	}()
	return nil
}

// readLoop drains the alarm-events stream into the worker pool until the context is
// cancelled, the reader reports EOF, or a run of non-EOF read errors outlasts the pacer's
// budget — at which point the pacer has already ended the process, because a consumer that
// cannot read alarms pages nobody while reporting healthy, and a restart is the remedy for
// most of what causes it.
func (np *NotificationProcessor) readLoop(ctx context.Context) {
	messaging.RunConsumer(ctx, np.Reader, np.pacer(), func(msg messaging.Message) bool {
		return np.handOff(ctx, msg)
	})
}

// handOff gives one alarm event to the worker pool, and reports whether the read loop should
// carry on. It abandons the handoff on shutdown so the loop can exit instead of blocking on
// a full channel (A5). The message is unacked at that point, so it redelivers after restart.
func (np *NotificationProcessor) handOff(ctx context.Context, msg messaging.Message) bool {
	select {
	case np.messages <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// processMessages is the worker loop: it drains the messages channel and dispatches
// each alarm event. The A3 ack contract rides on each messaging.Message, and
// messaging.Process returns the message's reader slot when the dispatch returns, whichever
// disposition it took — including leaving it unacked for redelivery.
func (np *NotificationProcessor) processMessages(ctx context.Context) {
	for msg := range np.messages {
		messaging.Process(msg, func(m messaging.Message) { np.dispatchOne(ctx, m) })
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
	//
	// The context carries the message's AckDeadline — when the broker will redeliver it —
	// so the Notifier bounds the dispatch against the clock that actually started at fetch,
	// not against a fresh AckWait counted from now.
	if err := np.Notifier.Notify(messaging.WithAckDeadline(msgctx, msg), event); err != nil {
		// 🔴 ONE ERROR IS PERMANENT AND MUST NOT SPEND THE RETRY BUDGET: the tenant has been
		// deleted and this area's ADR-077 erasure fence refuses its writes. It reaches here
		// as an ordinary error, which everything below would read as transient — five
		// redeliveries an AckWait apart, refused identically every time, ending in a dead
		// letter that says an alarm reached nobody. For a deleted tenant there is nobody to
		// reach, and the dead letter would be a new row about a tenant being erased. Drop it.
		//
		// PolicyNotifier already classifies this on the two paths where it knows the write it
		// attempted (applyLifecycle, Escalate). This is the funnel: it holds for whatever
		// Notifier is behind the seam and for any future write the dispatcher grows, which is
		// the only reason to spend a check on a case a caller usually handles first.
		if errors.Is(err, rdb.ErrTenantPurged) {
			log.Info().Str("correlation", msg.CorrelationID()).Str("alarm", event.AlarmToken).
				Msg("Dropping an alarm notification for a deleted tenant; this area's data has " +
					"been erased, so no redelivery could ever be applied.")
			msg.Ack()
			done(core.ResultInvalid)
			return
		}
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
	// WriteFor fills the kind (alarm-events' declared one), subject, sequence, attempts and
	// correlation from msg, and the dedup id the max-delivery recorder shares.
	err := np.dead.WriteFor(ctx, msg, deadletter.Envelope{
		Reason: deadletter.ReasonExhausted,
		Summary: "an alarm could not be delivered to any configured channel after every " +
			"delivery attempt, so nobody was paged about it",
		Detail:     detail,
		Reference:  alarm,
		OccurredAt: time.Now().UTC(),
		Payload:    msg.Value,
	})
	if err != nil {
		// The loss is already counted, on dead_letter_lost_total, by the sink — see
		// deadletter.Producer.NewSink.
		log.Error().Err(err).Str("alarm", alarm).
			Msg("LOST notification: the alarm reached nobody and could not be dead-lettered.")
		return
	}
	if np.deadLettered != nil {
		np.deadLettered.Inc()
	}
}
