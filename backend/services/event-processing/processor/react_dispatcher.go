// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// ReactDispatcher is the REACT stage's consumer (ADR-051 slice 5b / ADR-054): an independent,
// near-stateless durable consumer of the derived-event stream that dispatches each detection's
// authored actions. It is deliberately SEPARATE from the DETECT single-writer processor — DETECT is
// a stateful replay-correct loop; REACT is an at-least-once, queue-group-ready worker whose only
// durability requirement is that each dispatch be idempotent under redelivery (carried by the
// deterministic command token, slice 5b-1). It owns its own reader goroutine and lifecycle, wired in
// main.go alongside the DETECT processor.
//
// Failure handling is classification-free: any dispatch failure leaves the message unacked for
// AckWait-paced redelivery (the dispatcher never acks a partially-dispatched event), and a persistently-failing event is
// bounded by the JetStream redelivery cap (messaging.MaxDeliver), after which it is dropped as
// poison and counted — the same transient-then-give-up idiom every cross-service consumer here uses,
// rather than fragile per-error interpretation.
type ReactDispatcher struct {
	reader     messaging.MessageReader
	dispatcher *react.Dispatcher
	metrics    *ReactMetrics
	// dead records an event whose actions could not be dispatched (ADR-024). Nil when no
	// dead-letter sink is configured, in which case the event is dropped as before. The sink
	// stamps this service as the letter's source and counts a lost letter on the process's
	// dead_letter_lost_total.
	dead *deadletter.Sink

	// newPacer builds the read pacer that bounds a run of failing reads and ends the
	// process once they stop looking transient.
	//
	// A FACTORY rather than a pacer, so each run() gets a fresh one. A pacer's budget
	// measures one unbroken run; a dispatcher started again after a stop would otherwise
	// inherit the failure count and start time of the run that ended, and its first error
	// would be measured against a window that closed long ago. Nothing restarts a
	// dispatcher today, which is why this is worth closing now rather than after something
	// does.
	//
	// It is also the seam the pacing tests use: a pacer on the real clock makes a test that
	// measures the retry budget take the retry budget.
	newPacer func() *core.ReadPacer

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
}

// NewReactDispatcher builds the REACT consumer over a derived-event reader, a rule resolver, and the
// action sinks. Any sink may be nil to disable that action kind (a nil commands sink disables
// send-command; a nil alarms sink disables raise-alarm; a nil connectors sink disables httpCall/publish,
// ADR-060); main decides which are configured. connectorRate is the SOURCE-side per-tenant outbound
// egress cost-gate (ADR-060 SD-3); a nil gate disables source-charging (connector dispatches metered
// only by the downstream outbound-connectors egress limiter). The dispatcher is constructed here so
// the whole REACT wiring lives behind one type.
//
// m is built once in the initialize phase (see NewReactMetrics) and shared by every
// dispatcher this service constructs, because that callback is connection-scoped and
// the counters are not.
func NewReactDispatcher(ms *core.Microservice, reader messaging.MessageReader,
	resolver react.RuleResolver, commands react.CommandSink, alarms react.AlarmSink, connectors react.ConnectorSink,
	connectorRate react.ConnectorRateGate, dead *deadletter.Sink, m *ReactMetrics) *ReactDispatcher {
	rd := &ReactDispatcher{
		reader:     reader,
		dispatcher: react.NewDispatcher(resolver, commands, alarms, connectors, connectorRate, m),
		metrics:    m,
		dead:       dead,
	}
	rd.newPacer = func() *core.ReadPacer { return core.NewReadPacer(ms, "react dispatch") }
	return rd
}

// Start launches the consumer goroutine. It is called after the NATS manager is started (the reader
// is live) from main's afterMicroserviceStarted.
func (rd *ReactDispatcher) Start(ctx context.Context) error {
	rd.procCtx, rd.procCancel = context.WithCancel(context.Background())
	rd.wg.Add(1)
	go rd.run()
	return nil
}

// Stop cancels the consumer and waits for it to exit before the reader is torn down.
func (rd *ReactDispatcher) Stop(ctx context.Context) error {
	if rd.procCancel != nil {
		rd.procCancel()
	}
	rd.wg.Wait()
	return nil
}

// run drains the derived-event stream, dispatching each event's actions.
//
// The loop's own post-handle shutdown check is gone because messaging.RunConsumer re-checks
// the context BEFORE each read, which is the same stop one iteration earlier.
func (rd *ReactDispatcher) run() {
	defer rd.wg.Done()
	messaging.RunConsumer(rd.procCtx, rd.reader, rd.pacer(), func(msg messaging.Message) bool {
		rd.handle(msg)
		return true
	})
}

// pacer builds a read pacer for one run of the loop, falling back to a reportless one when
// this dispatcher was assembled by struct literal rather than by NewReactDispatcher.
//
// Called once, from run(), on the goroutine that owns the loop. A ReadPacer is
// single-goroutine by contract and this does not change that.
func (rd *ReactDispatcher) pacer() *core.ReadPacer {
	if rd.newPacer == nil {
		return core.NewReadPacer(nil, "react dispatch")
	}
	return rd.newPacer()
}

// handle dispatches one derived event and acks it or leaves it unacked. An undecodable or
// tenant-inconsistent payload is poison (a retry cannot fix it) — acked so it stops redelivering. A
// dispatch that returns Retry is left unacked for AckWait-paced redelivery, unless the redelivery cap
// is exhausted, in which case the event is dropped (acked) and counted as poison so a
// persistently-failing dispatch cannot redeliver
// forever. A Done dispatch is acked.
func (rd *ReactDispatcher) handle(msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(rd.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping derived event with no parseable tenant in subject %q", msg.Subject)
		rd.ack(msg)
		return
	}
	var ev runtime.DerivedEvent
	if err := json.Unmarshal(msg.Value, &ev); err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping undecodable derived event from subject %q", msg.Subject)
		rd.ack(msg)
		return
	}
	// Defense in depth: the payload tenant must match the tenant the subject scoped it to (DETECT
	// stamps them equal and backstop-validates at publish). A mismatch is a forged/corrupt event —
	// drop it rather than dispatch an action attributed to the wrong tenant.
	if ev.Tenant != tenant {
		log.Error().Str("subjectTenant", tenant).Str("payloadTenant", ev.Tenant).Str("rule", ev.RuleID).
			Msg("Dropping derived event whose payload tenant disagrees with its subject.")
		rd.ack(msg)
		return
	}
	// The runtime tenant backstop, mirrored on the consume side (derived.go enforces it at publish):
	// the rule id's tenant prefix MUST equal the event's tenant. An event forged onto tenant X's
	// derived-events subject but carrying tenant Y's rule id would otherwise be dispatched under X.
	//
	// It was written when LoadByID was a GLOBAL POINT READ, and it was then the only thing stopping
	// that forged event from resolving Y's rule and enqueueing Y's authored command content under X.
	// LoadByID is tenant-scoped now, so the storage layer refuses it too and this check is genuine
	// defense in depth rather than the sole barrier. It stays: the two fail independently, and
	// reaching either needs broker write access (DETECT's own publisher can never emit it). Drop
	// fail-closed.
	if idTenant, ok := runtime.RuleTenant(ev.RuleID); !ok || idTenant != tenant {
		log.Error().Str("tenant", tenant).Str("rule", ev.RuleID).
			Msg("Dropping derived event whose rule-id tenant disagrees with the event tenant (backstop).")
		rd.ack(msg)
		return
	}

	if rd.dispatcher.Dispatch(tctx, ev) == react.Done {
		rd.ack(msg)
		return
	}
	// Retry: redeliver, unless the cap is exhausted — then give up so one un-dispatchable
	// event cannot redeliver forever. Giving up now means DEAD-LETTERING (ADR-024): the
	// event's actions are recorded where they can be inspected rather than vanishing into
	// a log line and a counter.
	if msg.NumDelivered >= messaging.MaxDeliver {
		log.Error().Str("rule", ev.RuleID).Str("series", ev.Series).Int("attempts", msg.NumDelivered).
			Msg("Dead-lettering derived event after the redelivery cap; its actions could not be dispatched.")
		rd.deadLetter(tctx, msg, ev)
		rd.metrics.recordPoisonDropped()
		rd.ack(msg)
		return
	}
	// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery — an
	// immediate nak would burn MaxDeliver in ~1.4ms inside an outage, and the
	// derived event redelivers on ack timeout. Reference: event-sources' settler
	// (ADR-030).
}

// deadLetter records an un-dispatchable derived event on the dead-letter stream.
//
// 🔴 THE ACK HAPPENS EITHER WAY, and that is deliberate rather than sloppy. This runs only
// at the redelivery cap, so JetStream will not redeliver whatever the consumer does —
// leaving the message unacked here would not buy another attempt, it would leave it
// dangling. So the write gets its own bounded in-process retries (core/deadletter), and a
// write that still fails is counted as a LOSS rather than logged as something that will
// be retried.
//
// A nil sink is a TEST shape, not a deployment one: main fails startup if it cannot create
// the stream, so no running service reaches here with one. It is tolerated because every
// other test in this package builds a dispatcher without it, and a nil check is cheaper
// than making them all care about a sink they are not testing.
func (rd *ReactDispatcher) deadLetter(tctx context.Context, msg messaging.Message, ev runtime.DerivedEvent) {
	if rd.dead == nil {
		return
	}
	// WriteFor fills the kind (derived-events' declared one), subject, sequence, attempts and
	// correlation from msg, and the dedup id the max-delivery recorder shares.
	err := rd.dead.WriteFor(tctx, msg, deadletter.Envelope{
		Reason: deadletter.ReasonExhausted,
		Summary: "a detection fired and its authored actions could not be dispatched after " +
			"every delivery attempt",
		Reference:  ev.RuleID,
		OccurredAt: time.Now().UTC(),
		Payload:    msg.Value,
	})
	if err != nil {
		// The loss is already counted, on dead_letter_lost_total, by the sink — see
		// deadletter.Producer.NewSink.
		log.Error().Err(err).Str("rule", ev.RuleID).
			Msg("LOST derived event: it could be neither dispatched nor dead-lettered.")
		return
	}
	rd.metrics.recordDeadLettered()
}

// ack best-effort acks, logging a failed ack (a redelivery re-dispatches idempotently).
func (rd *ReactDispatcher) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a derived event; it will redeliver (idempotent).")
	}
}
