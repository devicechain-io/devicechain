// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
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
// Failure handling is classification-free: any dispatch failure Naks the message for redelivery
// (the dispatcher never acks a partially-dispatched event), and a persistently-failing event is
// bounded by the JetStream redelivery cap (messaging.MaxDeliver), after which it is dropped as
// poison and counted — the same transient-then-give-up idiom every cross-service consumer here uses,
// rather than fragile per-error interpretation.
type ReactDispatcher struct {
	reader     messaging.MessageReader
	dispatcher *react.Dispatcher
	metrics    *reactMetrics

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
}

// NewReactDispatcher builds the REACT consumer over a derived-event reader, a rule resolver, and a
// command sink. A nil sink/resolver is a programming error (main wires them); the dispatcher is
// constructed here so the whole REACT wiring lives behind one type.
func NewReactDispatcher(ms *core.Microservice, reader messaging.MessageReader,
	resolver react.RuleResolver, commands react.CommandSink) *ReactDispatcher {
	m := newReactMetrics(ms)
	return &ReactDispatcher{
		reader:     reader,
		dispatcher: react.NewDispatcher(resolver, commands, m),
		metrics:    m,
	}
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

// run drains the derived-event stream, dispatching each event's actions. It mirrors the fact
// consumers' read loop: an EOF (reader closed) or a cancelled context exits; a transient read error
// backs off and retries.
func (rd *ReactDispatcher) run() {
	defer rd.wg.Done()
	for {
		msg, err := rd.reader.ReadMessage(rd.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rd.reader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rd.procCtx.Done():
				return
			}
			continue
		}
		rd.handle(msg)
		if rd.procCtx.Err() != nil {
			return
		}
	}
}

// handle dispatches one derived event and acks/naks it. An undecodable or tenant-inconsistent
// payload is poison (a retry cannot fix it) — acked so it stops redelivering. A dispatch that
// returns Retry Naks for redelivery, unless the redelivery cap is exhausted, in which case the event
// is dropped (acked) and counted as poison so a persistently-failing dispatch cannot redeliver
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
	// the rule id's tenant prefix MUST equal the event's tenant. Without it, an event forged onto
	// tenant X's derived-events subject but carrying tenant Y's rule id would resolve Y's rule
	// (LoadByID is a global point read) and enqueue Y's authored command content under X — a
	// cross-tenant leak of rule content. Reaching this needs broker write access (DETECT's own
	// publisher can never emit it), so it is defense-in-depth; drop fail-closed.
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
	// Retry: redeliver, unless the cap is exhausted — then give up (poison) so one un-dispatchable
	// event cannot redeliver forever.
	if msg.NumDelivered >= messaging.MaxDeliver {
		log.Error().Str("rule", ev.RuleID).Str("series", ev.Series).Int("attempts", msg.NumDelivered).
			Msg("Dropping derived event after the redelivery cap; its actions could not be dispatched (no dead-letter path yet).")
		rd.metrics.recordPoisonDropped()
		rd.ack(msg)
		return
	}
	if err := msg.Nak(); err != nil {
		log.Warn().Err(err).Str("rule", ev.RuleID).Msg("Failed to nak a derived event; it will redeliver on ack timeout.")
	}
}

// ack best-effort acks, logging a failed ack (a redelivery re-dispatches idempotently).
func (rd *ReactDispatcher) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a derived event; it will redeliver (idempotent).")
	}
}
