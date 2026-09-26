// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Batched persistence.
//
// Each commit on a replicated event store waits for a standby, and that wait — not the
// database's work — is what limited how fast one event per transaction could be stored. A
// writer therefore commits the messages already waiting for it in ONE transaction, running
// for each exactly the statements PersistEvent runs, under that message's own tenant
// context. What does not change:
//
//   - A message is acknowledged only after the transaction holding it has committed.
//   - Nothing a message writes commits without the rest of it: a message whose statement
//     fails takes the whole transaction down, so a half-written message never commits
//     beside its batch-mates.
//   - A refused message is disposed of by the unchanged per-message path — PersistEvent in
//     a transaction of its own, then dispose — so it is retried, reported or acknowledged
//     exactly as it was before batching existed.
//   - A refused message does not take its batch-mates with it. When the failure belongs to
//     one message, that message goes the per-message path and the others are committed
//     again as a batch without it. When the erasure fence refused it, every message of its
//     tenant goes the per-message path, since the fence refuses them all. Only a failure no
//     message can be blamed for (BEGIN, COMMIT, a lost connection) sends every message
//     down the per-message path.
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
	event  *dmmodel.ResolvedEvent
	done   func(result string)
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
	event, err := dmproto.UnmarshalResolvedEvent(msg.Value)
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

// collect blocks for one message, then takes whatever else is already waiting — and, with
// a Linger, whatever arrives within it — until the batch holds MaxBatch admitted messages.
// Messages that are not admitted do not count. open is false once the channel is closed;
// the batch returned alongside it is still to be persisted.
func (ep *EventPersistenceWorker) collect(ctx context.Context) (batch []pendingEvent, open bool) {
	msg, ok := <-ep.Unpersisted
	if !ok {
		return nil, false
	}
	limit := max(ep.MaxBatch, 1)
	batch = make([]pendingEvent, 0, limit)
	if p, ok := ep.admit(ctx, msg); ok {
		batch = append(batch, p)
	}
	var linger <-chan time.Time
	if ep.Linger > 0 {
		timer := time.NewTimer(ep.Linger)
		defer timer.Stop()
		linger = timer.C
	}
	for len(batch) < limit {
		select {
		case msg, ok := <-ep.Unpersisted:
			if !ok {
				return batch, false
			}
			if p, ok := ep.admit(ctx, msg); ok {
				batch = append(batch, p)
			}
			continue
		default:
		}
		// Nothing is waiting. Without a linger — or once it has run out — commit what
		// there is; a batch of one is simply a message persisted on its own.
		if linger == nil {
			return batch, true
		}
		select {
		case msg, ok := <-ep.Unpersisted:
			if !ok {
				return batch, false
			}
			if p, ok := ep.admit(ctx, msg); ok {
				batch = append(batch, p)
			}
		case <-linger:
			linger = nil
		}
	}
	return batch, true
}

// persistBatch commits batch in one transaction and acknowledges it, or — when that
// transaction does not commit — writes its messages again without the one to blame (see
// the file comment).
//
// ctx is the WORKER's context and carries no tenant, deliberately: every statement binds
// its own message's context, and a statement that ever forgot to would fail closed with
// ErrNoTenant rather than write under a batch-mate's tenant.
func (ep *EventPersistenceWorker) persistBatch(ctx context.Context, batch []pendingEvent) {
	for len(batch) > 1 {
		failedAt := -1
		err := ep.Api.PersistInTx(ctx, func(tx *gorm.DB) error {
			for i := range batch {
				p := &batch[i]
				pevent, err := ep.baseEvent(p.ctx, *p.event)
				if err == nil {
					_, err = ep.writeEvent(p.ctx, tx, pevent, *p.event)
				}
				if err != nil {
					// Stop here: on Postgres the transaction is already aborted, and
					// carrying on would commit the messages around a half-written one.
					failedAt = i
					return err
				}
			}
			return nil
		})
		if err == nil {
			ep.metrics.committed(len(batch))
			// After COMMIT, never inside the transaction.
			for _, p := range batch {
				p.msg.Ack()
				p.done(core.ResultOK)
			}
			return
		}
		ep.metrics.fallback()
		if failedAt < 0 || connectionFailure(err) {
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
		// message — so all of them are set aside together. Setting them aside one failed batch at a
		// time would re-run every live batch-mate ahead of each, which for a deleted tenant
		// still sending grows with the square of its share of the batch.
		refused := batch[failedAt].tenant
		wholeTenant := errors.Is(err, rdb.ErrTenantPurged)
		log.Warn().Err(err).Int("events", len(batch)).Str("tenant", refused).Bool("tenantPurged", wholeTenant).
			Msg("An event in a batch was refused; persisting it on its own and committing the rest without it")
		// A fresh slice: the remainder must not alias the batch it came from.
		rest := make([]pendingEvent, 0, len(batch)-1)
		for i, p := range batch {
			if i == failedAt || (wholeTenant && p.tenant == refused) {
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

// persistOne is the per-message path: PersistEvent in a transaction of its own, then
// dispose. A batch of one takes it, and so does every message a batch gives up on.
func (ep *EventPersistenceWorker) persistOne(p pendingEvent) {
	_, err := ep.PersistEvent(p.ctx, *p.event)
	if err == nil {
		ep.metrics.committed(1)
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
	// A deterministic failure (bad data) can never succeed on redelivery,
	// so give up on the first failure (ADR-024). A transient failure is
	// retried via redelivery up to the cap and then given up on the same
	// way. Both report the event on the failed-events stream, which is a
	// report and not a queue — see ErrDeterministic.
	switch {
	case errors.Is(err, ErrDeterministic):
		ep.Failed(p.tenant, uint(dmproto.FailureReason_Invalid), *p.event, err, p.msg.CorrelationID())
		p.msg.Ack()
		p.done(core.ResultFailed)
	case p.msg.NumDelivered >= messaging.MaxDeliver:
		ep.Failed(p.tenant, uint(dmproto.FailureReason_ApiCallFailed), *p.event, err, p.msg.CorrelationID())
		p.msg.Ack()
		p.done(core.ResultFailed)
	default:
		// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery —
		// an immediate nak would burn MaxDeliver in ~1.4ms inside a Postgres
		// outage. Reference disposition: event-sources' settler (ADR-030).
		p.done(core.ResultRetry)
	}
}

// connectionFailure reports whether err says the database connection failed rather than
// that a statement was refused, so no message in the batch can be blamed for it. It only
// decides how a failed batch is replayed — one message set aside, or every message on its
// own — never a message's disposition, which its own persist decides. A connection
// failure it does not recognise costs extra transactions, not correctness.
func connectionFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.Timeout(err) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return true
	}
	var cerr *pgconn.ConnectError
	if errors.As(err, &cerr) {
		return true
	}
	// Class 08 is connection exception; 57P01-57P05 are the server shutting down or
	// cancelling the session.
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return strings.HasPrefix(pgerr.Code, "08") || strings.HasPrefix(pgerr.Code, "57P")
	}
	return false
}
