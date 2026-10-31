// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"time"
)

// Batched persistence.
//
// Each commit on a replicated event store waits for a standby, and that wait — not the
// database's work — is what limited how fast one event per transaction could be stored. A
// writer therefore commits the messages already waiting for it in ONE transaction. It
// first writes them grouped (persist_grouped.go): each table once per tenant in the batch,
// every statement bound to a message of its own tenant, rather than each message's
// statements in turn. What does not change:
//
//   - What is stored: the grouped write keeps every ON CONFLICT arbiter and the
//     alternate-id skip, and writes state changes one at a time, so it stores what writing
//     each message with PersistEvent's statements stores.
//   - A message is acknowledged only after the transaction holding it has committed.
//   - Nothing a message writes commits without the rest of it: a message whose statement
//     fails takes the whole transaction down, so a half-written message never commits
//     beside its batch-mates.
//   - A refused message is disposed of by the unchanged per-message path — PersistEvent in
//     a transaction of its own, then dispose — so it is retried, reported or acknowledged
//     exactly as it was before batching existed.
//   - A refused message does not take its batch-mates with it. When the failure belongs to
//     one message, that message goes the per-message path and the others are committed
//     again as a batch without it. When the erasure fence refused it, the refusal is the
//     TENANT's, so every message of that tenant is settled with it at once (dispose, which
//     asks messaging.Dispose: an erased tenant's message is acked and reported nowhere) —
//     none is written again on its own, because the fence would refuse each of them the
//     same way. Only a failure no message can be blamed for (BEGIN, COMMIT, a lost
//     connection) sends every message down the per-message path.
//   - What the grouped write adds: when the database refuses a statement that carried
//     several messages' rows, it does not say which row, so no message can be blamed yet.
//     The same batch is then written again in a NEW transaction one message at a time
//     (writeEach, the statements PersistEvent runs), which blames the message exactly as
//     above. Such a refusal costs its batch one more transaction, and counts twice in
//     persist_batch_fallbacks_total. A new transaction rather than a savepoint, because a
//     savepoint would cost every batch a round trip, put every event row in a
//     subtransaction, and roll back the share lock of a fence read whose memoised answer
//     would survive the rollback. A message whose rows cannot be built (a value that is not
//     a number, say) is blamed before anything is sent — which also means that a
//     redelivered event whose alternate id is already stored, and whose payload no longer
//     builds, costs one fallback before the per-message path skips it.
//   - Replaying is safe: every insert carries an ON CONFLICT arbiter and the event id is
//     derived from the content, so a message written again after a rollback — or after a
//     COMMIT whose outcome was lost — adds nothing twice.
//   - The erasure fence is still read inside the writing transaction, once per tenant per
//     transaction (the memo is per tenant). A batch transaction is longer than a
//     single-message one, and it is bounded by the batch cap; the fence's own argument
//     assumes no writing transaction outlives the purge's settle window.

// pendingEvent is one admitted message waiting for its batch.
type pendingEvent struct {
	msg    messaging.Message
	ctx    context.Context // the message's own tenant-scoped context
	tenant string
	event  *resolvedevent.ResolvedEvent
	done   func(result string)
	// progressAt is when the broker was last told this message is in progress while held.
	progressAt time.Time
}

// admit runs the checks a message must pass before it can be persisted: derive its tenant
// from the subject and decode it. A message that fails them is disposed of here — acked
// and recorded invalid, as it always was — and joins no batch.
func (ep *EventPersistenceWorker) admit(ctx context.Context, msg messaging.Message) (pendingEvent, bool) {
	// Mark the message in-flight and record its result+duration on every
	// disposition path (ADR-022 E13). start() is nil-safe.
	done := ep.metrics.start()

	log.Debug().Int("worker", ep.WorkerId).Str("correlation", msg.CorrelationID()).
		Msg("Event persistence handled by worker")

	// Derive the per-message tenant from the message subject and build a
	// tenant-scoped context. Without a parseable tenant the message can
	// not be persisted safely (fail-closed) so it is skipped rather than
	// written without a tenant. The tenant string is carried onto the
	// failed channel so the downstream producer scopes its publish to the
	// same tenant.
	msgctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Msg(fmt.Sprintf("Skipping message with no parseable tenant in subject %q", msg.Subject))
		// Poison message: a message with no parseable tenant can not be
		// persisted and redelivery can not help, so ack it to drop it.
		msg.Ack()
		done(core.ResultInvalid)
		return pendingEvent{}, false
	}

	// Attempt to unmarshal event.
	event, err := resolvedevent.UnmarshalResolvedEvent(msg.Value)
	if err != nil {
		ep.Invalid(err, msg)
		// Terminal: reported on the failed-events stream (which nothing
		// consumes — see ErrDeterministic), so ack to drop it.
		msg.Ack()
		done(core.ResultInvalid)
		return pendingEvent{}, false
	}

	if log.Debug().Enabled() {
		jevent, err := json.MarshalIndent(event, "", "  ")
		if err == nil {
			log.Debug().Msg(fmt.Sprintf("Received %s event:\n%s", event.EventType.String(), jevent))
		}
	}
	return pendingEvent{msg: msg, ctx: msgctx, tenant: tenant, event: event, done: done}, true
}

// collect fills the next batch from the channel the read loop feeds (messaging.CollectBatch,
// the one definition every batching writer shares). Messages admit refuses do not count.
func (ep *EventPersistenceWorker) collect(ctx context.Context) (batch []pendingEvent, open bool) {
	return messaging.CollectBatch(ep.Unpersisted, ep.MaxBatch, ep.Linger,
		func(msg messaging.Message) (pendingEvent, bool) { return ep.admit(ctx, msg) })
}

// persistBatch commits batch in one transaction, written grouped, and acknowledges it, or
// — when that transaction does not commit — writes its messages again without the one to
// blame, first finding it one message at a time when the grouped write could not name it
// (see the file comment).
//
// ctx is the WORKER's context and carries no tenant, deliberately: every statement binds
// the context of a message of its own tenant, and a statement that ever forgot to would
// fail closed with ErrNoTenant rather than write under a batch-mate's tenant.
func (ep *EventPersistenceWorker) persistBatch(ctx context.Context, batch []pendingEvent) {
	started := time.Now()
	// Each pass either returns or sets at least one message aside, so the loop ends: the
	// one-message-at-a-time write that finds an unattributed refusal is part of the same
	// pass, not a pass of its own.
	for len(batch) > 1 {
		failedAt, err := ep.writeBatch(ctx, batch, ep.writeGrouped)
		if err == nil {
			ep.acknowledge(batch, started)
			return
		}
		ep.metrics.fallback()
		if ep.outage(ctx, err) {
			// The store refused the write (full, read-only, unreachable): nothing in the
			// batch is to blame and every message of it would be refused the same way, so
			// the whole batch is held until the store takes writes again.
			log.Warn().Err(err).Int("events", len(batch)).
				Msg("The event store cannot take writes; holding the batch until it can")
			for _, p := range batch {
				ep.hold(p, err)
			}
			return
		}
		if errors.Is(err, errGroupUnattributed) {
			// The database refused a statement carrying several events. Find which one by
			// writing the same batch again, one event at a time, in a new transaction.
			log.Warn().Err(err).Int("events", len(batch)).
				Msg("A grouped event write was refused; writing the batch again one event at a time")
			failedAt, err = ep.writeBatch(ctx, batch, ep.writeEach)
			if err == nil {
				ep.acknowledge(batch, started)
				return
			}
			ep.metrics.fallback()
			if ep.outage(ctx, err) {
				for _, p := range batch {
					ep.hold(p, err)
				}
				return
			}
		}
		if failedAt < 0 {
			// Nothing in the batch is to blame — the transaction could not begin or commit,
			// or the connection went — so every message is written on its own, as it
			// would have been before batching. During an outage each of those fails fast
			// and takes its ordinary retry.
			log.Warn().Err(err).Int("events", len(batch)).
				Msg("An event batch did not commit; persisting its events one at a time")
			for _, p := range batch {
				ep.persistOne(p)
			}
			return
		}
		// The message to blame is set aside. When the erasure fence refused it, the fence
		// refuses every other message of that tenant too — it is per tenant, not per
		// message — so all of them are set aside together, and settled with the refusal
		// rather than each written again alone only to be refused the same way. Setting them
		// aside one failed batch at a time would re-run every live batch-mate ahead of each,
		// which for a deleted tenant still sending grows with the square of its share of the
		// batch.
		refused := batch[failedAt].tenant
		wholeTenant := errors.Is(err, rdb.ErrTenantPurged)
		log.Warn().Err(err).Int("events", len(batch)).Str("tenant", refused).Bool("tenantPurged", wholeTenant).
			Msg("An event in a batch was refused; persisting it on its own and committing the rest without it")
		// A fresh slice: the remainder must not alias the batch it came from.
		rest := make([]pendingEvent, 0, len(batch)-1)
		for i, p := range batch {
			if wholeTenant && p.tenant == refused {
				ep.dispose(p, err)
				continue
			}
			if i == failedAt {
				ep.persistOne(p)
				continue
			}
			rest = append(rest, p)
		}
		batch = rest
	}
	if len(batch) == 1 {
		ep.persistOne(batch[0])
	}
}

// eventRows is how many rows a message stores: one per measurement entry, derived entries
// included, and one for any other event. It is what lets rows/s and latency per row be
// compared between a profile with calculated metrics and one without, where events/s and
// latency per event are not.
func eventRows(event *resolvedevent.ResolvedEvent) int {
	if payload, ok := event.Payload.(*resolvedevent.ResolvedMeasurementsPayload); ok && payload != nil {
		n := 0
		for _, sample := range payload.Entries {
			n += len(sample.Entries)
		}
		if n > 0 {
			return n
		}
	}
	return 1
}

// writeBatch writes batch with write in one transaction, returning the index of the
// message write blamed (or -1) with the transaction's error.
func (ep *EventPersistenceWorker) writeBatch(ctx context.Context, batch []pendingEvent,
	write func(*gorm.DB, []pendingEvent) (int, error)) (int, error) {
	failedAt := -1
	ep.writeStart = time.Now()
	err := ep.Api.PersistInTx(ctx, func(tx *gorm.DB) error {
		var err error
		failedAt, err = write(tx, batch)
		return err
	})
	return failedAt, err
}

// acknowledge acknowledges every message of a batch whose transaction committed — after
// COMMIT, never inside the transaction.
func (ep *EventPersistenceWorker) acknowledge(batch []pendingEvent, started time.Time) {
	rows := 0
	for _, p := range batch {
		rows += eventRows(p.event)
	}
	ep.metrics.committed(len(batch), rows, time.Since(started))
	for _, p := range batch {
		p.msg.Ack()
		p.done(core.ResultOK)
	}
}

// persistOne is the per-message path: PersistEvent in a transaction of its own, then
// dispose. A batch of one takes it, and so does every message a batch gives up on.
func (ep *EventPersistenceWorker) persistOne(p pendingEvent) {
	started := time.Now()
	ep.writeStart = started
	_, err := ep.PersistEvent(p.ctx, *p.event)
	if err == nil {
		ep.metrics.committed(1, eventRows(p.event), time.Since(started))
	}
	ep.dispose(p, err)
}

// dispose acknowledges, reports or leaves a message for redelivery according to the
// outcome of its own persist.
func (ep *EventPersistenceWorker) dispose(p pendingEvent, err error) {
	if err == nil {
		// Durably persisted: ack so the message is not redelivered.
		p.msg.Ack()
		p.done(core.ResultOK)
		return
	}
	err = classifyPersistFailure(err)
	// The store cannot take writes: HOLD the message. This comes before the delivery budget
	// is consulted on purpose. The budget is for a message that is the problem; here the
	// store is, and acking-and-reporting an event on its fifth delivery because the disk was
	// full for five minutes throws away data the platform already acknowledged.
	if !errors.Is(err, ErrDeterministic) && !errors.Is(err, rdb.ErrTenantPurged) && ep.outage(p.ctx, err) {
		ep.hold(p, err)
		return
	}
	disposition := messaging.Dispose(p.msg, err)
	// An erased tenant's event is settled at once and reported NOWHERE: the fence refuses
	// that tenant on every delivery, and a failed-events copy of its payload would be a new
	// record of the very data being erased. It is decided first, so neither arm below can
	// publish one.
	//
	// A deterministic failure (bad data) can never succeed on redelivery,
	// so give up on the first failure (ADR-024). A transient failure is
	// retried via redelivery up to the cap and then given up on the same
	// way. Both report the event on the failed-events stream, which is a
	// report and not a queue — see ErrDeterministic.
	switch {
	case disposition == messaging.TenantErased:
		log.Info().Str("tenant", p.tenant).Str("correlation", p.msg.CorrelationID()).
			Msg("Settling an event for a deleted tenant; this area's data has been erased.")
		p.msg.Ack()
		p.done(core.ResultErased)
	case errors.Is(err, ErrDeterministic):
		ep.Failed(p.tenant, uint(resolvedevent.FailureReason_Invalid), *p.event, err, p.msg.CorrelationID())
		p.msg.Ack()
		p.done(core.ResultFailed)
	case disposition == messaging.Exhausted:
		ep.Failed(p.tenant, uint(resolvedevent.FailureReason_ApiCallFailed), *p.event, err, p.msg.CorrelationID())
		p.msg.Ack()
		p.done(core.ResultFailed)
	default:
		// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
		// an immediate nak would burn MaxDeliver in ~1.4ms inside a Postgres
		// outage. Reference disposition: event-sources' settler (ADR-030).
		p.done(core.ResultRetry)
	}
}
