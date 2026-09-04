// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// readErrorBackoff is how long the read loop waits after a transient read error before retrying, so
// a flapping broker connection does not hot-spin the loop.
const readErrorBackoff = time.Second

// deadLetterWriteBackoff is the short pause between bounded dead-letter write retries on the final
// delivery (see deadLetter).
const deadLetterWriteBackoff = 100 * time.Millisecond

// DispatchConsumer is the outbound-connectors service's durable consumer of the connector-dispatch
// stream (ADR-060 §4 / slice C3). It mirrors the notification-management dispatch model: a single
// read loop hands each message to a bounded worker pool, and the worker that dispatches a message is
// the one that acks (success/poison/refused for a deleted tenant), leaves it unacked (transient,
// redeliver after AckWait), or dead-letters it (cap exhausted / terminal). The pool width is the
// outbound concurrency ceiling — SD-2's back-pressure:
// once every worker is busy on a slow target, the loop stops pulling and unacked work stays durable
// on the (per-tenant bounded) stream rather than growing an in-memory queue.
//
// Idempotency rides in each message (the content-addressed key), so an at-least-once redelivery or a
// DETECT replay collapses downstream to one execution (an endpoint honoring X-DC-Idempotency-Key);
// the consumer therefore never needs cross-message state and scales out as a queue group.
type DispatchConsumer struct {
	reader   messaging.MessageReader
	dead     messaging.MessageWriter
	executor *Executor
	metrics  *dispatchMetrics

	// deadIndex writes the platform-wide (ADR-024) index entry for a give-up, so an outbound
	// dispatch the service abandoned appears in the one list an operator reads rather than only on
	// this service's own terminal subject. See index for why it is best-effort.
	deadIndex *deadletter.Sink

	// rate is the per-tenant outbound egress limiter (ADR-060 SD-3). nil disables egress rate
	// limiting (every dispatch admitted; the bounded worker pool + per-send timeout still bound
	// egress). waitBudget is how long a worker blocks for a token before shedding.
	rate       *core.TenantRateLimiter
	waitBudget time.Duration

	// tenantDeleted answers the ADR-077 lifecycle question for one tenant. NIL means the gate is
	// unconfigured (governance.NewTenantLifecycleGate returns nil when user-management is not
	// reachable) and every dispatch is admitted, which is the behaviour this service had before the
	// gate existed. Read through the tenantIsDeleted accessor, never directly.
	tenantDeleted func(string) bool

	backlog int

	procCtx    context.Context
	procCancel context.CancelFunc
	messages   chan messaging.Message
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup
	workers    int
	stopOnce   sync.Once
}

// NewDispatchConsumer builds the consumer over its dispatch reader, its dead-letter writer, and the
// executor. rate is the per-tenant egress limiter (nil disables egress rate limiting); waitBudget is
// how long a worker blocks for a token before shedding. tenantDeleted is the ADR-077 lifecycle gate
// (nil disables the refusal). workers is the outbound concurrency ceiling; backlog is the
// reader→worker hand-off buffer. A nil Microservice (unit tests) leaves metrics nil (every recorder
// is nil-safe).
//
// tenantDeleted is a CONSTRUCTOR ARGUMENT rather than a setter for the same reason the rate limiter
// is: this is the only consumer in the service, so the property worth buying is that a second one
// added later cannot be constructed without answering the question.
func NewDispatchConsumer(ms *core.Microservice, reader messaging.MessageReader, dead messaging.MessageWriter,
	deadIndex deadletter.Writer, executor *Executor, rate *core.TenantRateLimiter, waitBudget time.Duration,
	tenantDeleted func(string) bool, workers, backlog int) *DispatchConsumer {
	metrics := newDispatchMetrics(ms)
	var index *deadletter.Sink
	if deadIndex != nil {
		index = deadletter.NewSink(deadIndex, func(error) { metrics.recordOutcome(actionUnknown, outcomeDeadIndexFailed) })
	}
	return &DispatchConsumer{
		reader:        reader,
		dead:          dead,
		deadIndex:     index,
		executor:      executor,
		metrics:       metrics,
		rate:          rate,
		waitBudget:    waitBudget,
		tenantDeleted: tenantDeleted,
		backlog:       backlog,
		workers:       workers,
		// A non-nil default so a shutdown-aware wait (deadLetter's retry backoff) never dereferences a
		// nil context before Start runs; Start replaces it with the cancelable process context.
		procCtx: context.Background(),
	}
}

// Start launches the worker pool and the read loop. It is called after the NATS manager is started
// (the reader is live) from main's afterMicroserviceStarted.
func (c *DispatchConsumer) Start(ctx context.Context) error {
	c.procCtx, c.procCancel = context.WithCancel(context.Background())
	c.messages = make(chan messaging.Message, c.backlog)
	for i := 0; i < c.workers; i++ {
		c.workerWG.Add(1)
		go func() {
			defer c.workerWG.Done()
			// Workers run on a background context so that on shutdown they drain the buffered
			// messages to completion (ack or leave-unacked) rather than aborting an in-flight send.
			for msg := range c.messages {
				c.handle(context.Background(), msg)
			}
		}()
	}
	c.readerWG.Add(1)
	go c.run()
	return nil
}

// Stop unwinds the pipeline in dependency order so no goroutine sends on a closed channel: cancel the
// reader, wait for it to exit, close the channel it feeds, then wait for the workers to drain. It is
// idempotent (sync.Once) so a double Stop cannot panic on a second close of the messages channel.
func (c *DispatchConsumer) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		if c.procCancel != nil {
			c.procCancel()
		}
		c.readerWG.Wait()
		if c.messages != nil {
			close(c.messages)
		}
		c.workerWG.Wait()
	})
	return nil
}

// run drains the dispatch stream, handing each message to the worker pool. An EOF (reader closed) or
// a cancelled context exits; a transient read error backs off and retries.
func (c *DispatchConsumer) run() {
	defer c.readerWG.Done()
	for {
		msg, err := c.reader.ReadMessage(c.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			c.reader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-c.procCtx.Done():
				return
			}
			continue
		}
		// Hand off to a worker, abandoning on shutdown so the loop can exit rather than block on a
		// full channel; the message is unacked, so it redelivers after restart.
		select {
		case c.messages <- msg:
		case <-c.procCtx.Done():
			return
		}
	}
}

// handle processes one dispatch message end-to-end and applies its ack/leave-unacked/dead-letter
// disposition. A message with no parseable tenant, an undecodable body, a failed structural validation,
// or a payload/subject tenant mismatch is POISON (a redelivery cannot fix it) — dropped (acked) and
// counted invalid. A well-formed message for a DELETED tenant is refused and dropped (acked) without
// being executed — the one disposition that is neither poison nor an outcome of a send. Otherwise it
// is executed; the outcome decides ack (sent), leave-unacked (transient, redeliver after AckWait
// until the cap), or dead-letter (cap exhausted / terminal).
func (c *DispatchConsumer) handle(ctx context.Context, msg messaging.Message) {
	tctx, tenant, ok := messaging.TenantContextFromSubject(ctx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping connector dispatch with no parseable tenant in subject %q", msg.Subject)
		c.metrics.recordOutcome(actionUnknown, outcomeInvalid)
		c.ack(msg)
		return
	}
	// Grammar-validate the subject tenant (ADR-042) before it seeds a rate-limiter bucket / becomes a
	// dead-letter subject segment. TenantContextFromSubject only checks non-emptiness; without this an
	// oversized or malformed subject segment (reachable only with broker write access, the same threat
	// the payload/subject backstop below defends) could seed an unbounded-cardinality set of limiter
	// buckets keyed by multi-KB strings, or a segment the dead-letter writer would reject. A malformed
	// tenant is poison — a redelivery cannot fix the subject — so drop it (mirrors the event-sources
	// ingest guard).
	if err := core.ValidateToken(tenant); err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping connector dispatch whose subject tenant is not a valid token (subject %q)", msg.Subject)
		c.metrics.recordOutcome(actionUnknown, outcomeInvalid)
		c.ack(msg)
		return
	}
	req, err := connectorwire.UnmarshalConnectorDispatchRequest(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping undecodable connector dispatch from subject %q", msg.Subject)
		c.metrics.recordOutcome(actionUnknown, outcomeInvalid)
		c.ack(msg)
		return
	}
	if err := req.Validate(); err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).Str("tenant", tenant).
			Msg("Dropping structurally-invalid connector dispatch.")
		c.metrics.recordOutcome(actionLabel(req.Kind), outcomeInvalid)
		c.ack(msg)
		return
	}
	// Defense in depth (mirrors the REACT consumer's backstop): the payload tenant must match the
	// tenant the subject scoped it to. A mismatch is a forged/corrupt message — drop it rather than
	// execute an outbound call attributed to (and resolving the secret of) the wrong tenant. Reaching
	// this needs broker write access; the producer stamps them equal.
	if req.Tenant != tenant {
		log.Error().Str("subjectTenant", tenant).Str("payloadTenant", req.Tenant).Str("rule", req.RuleID).
			Msg("Dropping connector dispatch whose payload tenant disagrees with its subject.")
		c.metrics.recordOutcome(actionLabel(req.Kind), outcomeInvalid)
		c.ack(msg)
		return
	}

	action := actionLabel(req.Kind)

	// ADR-077 lifecycle gate: refuse to dispatch for a tenant that has been deleted.
	//
	// THIS SERVICE EMITS RATHER THAN RETAINS, which is what makes a late refusal different here.
	// On the ingest fronts, a message admitted a moment too late is a row the sweep reclaims on its
	// next pass. Here it is that tenant's payload on somebody else's server — a webhook, a Kafka
	// topic, an SNS topic — where no purge of ours can reach it. So it is gated at admission rather
	// than left to the fence, and it is gated here rather than at the executor because refusing
	// costs nothing and un-sending is impossible.
	//
	// It is NOT the only emitter: notification-management pages a tenant's humans over SMTP and
	// webhooks, and its escalation scheduler does so from its OWN rows on a timer, needing no
	// inbound traffic at all. That one carries the same gate, at PolicyNotifier. Naming it here
	// rather than claiming this path is unique is deliberate — the claim was made and was wrong,
	// and an "only path" comment is exactly what stops the next person looking for the second one.
	//
	// It is not redundant with the broker purge. That purge deletes the tenant's PENDING dispatches,
	// but a worker holding a message it already read is past the purge, DETECT can still be draining
	// its own eviction, and the gate's 60s cache means the two are not synchronized in either
	// direction. Between the operator's delete and the last of that settling, a message reaching
	// this line is one send away from leaving.
	//
	// # Why this drops (acks) rather than dead-letters, which the rate shed above does
	//
	// The dead-letter subject is TENANT-SCOPED ({instance}.{tenant}.connector-dispatch.dead), so
	// dead-lettering here writes new messages into the very namespace the sweep is reclaiming. The
	// broker store's next pass would find them, erase them, and report rows erased — and a store
	// that erases rows loses its clean-since, which restarts the settle window the purge cannot
	// complete without. A tenant deleted with a dispatch backlog would therefore hold its own purge
	// open, one pass per pass, for as long as the backlog took to drain. Dropping writes nothing.
	//
	// Nothing is lost by dropping that dead-lettering would have preserved: the dead-letter subject
	// exists so an operator can inspect or replay a dispatch, and both of those are answers to
	// "should this have been sent?" for a tenant where the answer is permanently no.
	//
	// # Why there is no redelivery exemption
	//
	// There cannot be one — the refusal acks, so a refused message is never redelivered. Said
	// explicitly because the ingest gate this mirrors DOES take a redelivery flag, and the reason it
	// needs one (its rate METERING must not charge a message twice) has no counterpart here: the
	// rate wait below is reached only by an admitted message, and an admitted message is one this
	// gate said yes to on this delivery, not on a previous one.
	if c.tenantIsDeleted(tenant) {
		// Debug, not Warn, on the same argument as the rate shed below: this fires once per message
		// for a tenant that is being deleted wholesale, so a per-message warn would flood the log at
		// exactly the moment an operator is watching it. The tenant_deleted COUNT is the signal.
		log.Debug().Str("rule", req.RuleID).Str("tenant", tenant).Str("action", action).
			Msg("Refused an outbound connector dispatch for a tenant that has been deleted; dropping it.")
		c.metrics.recordOutcome(action, outcomeTenantDeleted)
		c.ack(msg)
		return
	}

	// Per-tenant egress rate gate (ADR-060 SD-3), applied BEFORE the expensive secret-resolve + send.
	// The worker blocks up to waitBudget for a token: a brief burst just over the tenant's rate is
	// smoothed into pacing and admitted; a dispatch that cannot get a token within the budget is a
	// tenant sustained over quota (a brief burst would have been admitted) and is SHED to the
	// dead-letter subject. It never leaves a rate-shed message unacked, so rate-limiting can never churn the redelivery
	// (poison) cap; and because reaching the budget means sustained-over-quota, a redelivery would not
	// help either. The wait runs on ctx (a background context, per the worker) bounded by waitBudget,
	// so it aborts on its own deadline; a shed consumes no token, so it does not deepen the deficit.
	//
	// This blocks a worker, so a flooding tenant can occupy workers for up to waitBudget each, adding a
	// bounded (≤ waitBudget) delivery latency to other tenants whose dispatches wait behind them — a
	// deliberate, bounded trade for not churning the poison cap. It self-limits: a tenant far over quota
	// hits the budget and sheds fast (freeing the worker) rather than blocking the full budget, and the
	// per-tenant bounded durable stream is the real buffer. The PRIMARY throttle is at the source
	// (REACT charges the cost-gate at publish, C3b.3), so egress sheds should be the rare exception.
	if c.rate != nil {
		// Derive the wait from procCtx (cancelled on Stop) so a rolling-update drain aborts an
		// in-progress rate wait rather than blocking Stop for the budget: a wait interrupted by
		// shutdown ABANDONS the message unacked (it redelivers after restart for a fresh admission),
		// rather than dead-lettering a message that was only waiting on rate. A budget timeout (not a
		// shutdown) is a genuine sustained-over-quota shed.
		waitCtx, cancel := context.WithTimeout(c.procCtx, c.waitBudget)
		err := c.rate.Wait(waitCtx, tenant)
		cancel()
		if err != nil {
			if c.procCtx.Err() != nil {
				log.Info().Str("rule", req.RuleID).Str("tenant", tenant).
					Msg("Abandoning connector dispatch rate-wait on shutdown; it will redeliver on restart.")
				return
			}
			// Debug, not Warn: by design a rising rate_limited COUNT (the metric) is the operator
			// signal; a per-message warn would flood the log for exactly the sustained-over-quota
			// tenant this fires on.
			log.Debug().Str("rule", req.RuleID).Str("tenant", tenant).Str("action", action).
				Msg("Connector dispatch shed: tenant over its outbound egress rate beyond the smoothing budget; dead-lettering.")
			c.deadLetter(tctx, msg, req.RuleID, action, outcomeRateLimited)
			return
		}
	}

	res := c.executor.Execute(tctx, req)
	switch {
	case res.err == nil:
		c.metrics.recordOutcome(action, outcomeSent)
		c.ack(msg)
	case res.retryable:
		// Transient: redeliver until the cap, then dead-letter so a permanently-failing send cannot
		// redeliver forever (SD-2).
		if msg.NumDelivered >= messaging.MaxDeliver {
			log.Error().Err(res.err).Str("rule", req.RuleID).Str("tenant", tenant).Int("attempts", msg.NumDelivered).
				Msg("Connector dispatch dead-lettered after the redelivery cap.")
			c.deadLetter(tctx, msg, req.RuleID, action, outcomeDead)
			return
		}
		// Transient: leave it UNACKED (do not nak) so AckWait paces redelivery — an
		// immediate nak would burn MaxDeliver in ~1.4ms inside an outage (ADR-030).
		log.Warn().Err(res.err).Str("rule", req.RuleID).Str("tenant", tenant).Int("attempt", msg.NumDelivered).
			Msg("Connector dispatch failed; leaving unacked for redelivery.")
		c.metrics.recordOutcome(action, outcomeRetry)
	default:
		// Terminal (unsupported kind / malformed config that bypassed the publish gate): a redelivery
		// cannot help, so dead-letter it visibly rather than churn the cap or silently drop it.
		log.Error().Err(res.err).Str("rule", req.RuleID).Str("tenant", tenant).
			Msg("Connector dispatch is terminally undeliverable; dead-lettering.")
		c.deadLetter(tctx, msg, req.RuleID, action, res.outcome)
	}
}

// deadLetterWriteAttempts bounds the in-process retries of the dead-letter write on the final
// delivery, where leaving the message unacked could not redeliver it (past the cap; see deadLetter).
const deadLetterWriteAttempts = 3

// headerDeadReason is the message-header key stamped on a dead-lettered dispatch recording WHY it was
// dead-lettered (the outcome* value: rate_limited / dead / unsupported / invalid), so a rate-shed
// (healthy, replayable) is distinguishable from genuine poison on the shared terminal subject.
const headerDeadReason = "Dc-Dead-Reason"

// deadLetter writes the original message verbatim to the terminal dead-letter subject
// ({instance}.{tenant}.connector-dispatch.dead), then acks the original so it stops redelivering.
// tctx already carries the tenant, which the writer requires to scope the subject (fail-closed on
// none).
//
// The write-failure path must not silently lose the request. Its handling turns on whether the
// broker will still redeliver this message: BELOW the redelivery cap leaving it unacked redelivers
// it (after AckWait) and we retry dead-lettering on the next attempt; AT/ABOVE the cap no redelivery
// follows (JetStream is done redelivering after MaxDeliver), so simply leaving it unacked there would
// strand the message forever — never executed, never dead-lettered. So on the final delivery we retry
// the write a bounded number of times in-process, and if it still fails we record an explicit,
// alertable LOSS (never the false "will retry") so an operator sees a dispatch that could be neither
// delivered nor dead-lettered.
func (c *DispatchConsumer) deadLetter(tctx context.Context, msg messaging.Message, rule, action, outcome string) {
	// Stamp the disposition on the dead-lettered message (not just the metric/log) so an operator or a
	// future replay tool can tell a healthy-but-rate-shed dispatch (replayable) apart from genuine
	// poison (unsupported/invalid/permanently-failed) sharing this terminal subject. The header rides
	// through the NATS writer (it propagates non-correlation headers).
	dead := messaging.Message{Value: msg.Value, Headers: map[string]string{headerDeadReason: outcome}}.
		WithCorrelationID(msg.CorrelationID())
	finalDelivery := msg.NumDelivered >= messaging.MaxDeliver

	var err error
	attempts := 1
	if finalDelivery {
		attempts = deadLetterWriteAttempts
	}
	for i := 0; i < attempts; i++ {
		if err = c.dead.WriteMessages(tctx, dead); err == nil {
			c.metrics.recordOutcome(action, outcome)
			c.ack(msg)
			// 🔑 AFTER THE ACK, DELIBERATELY. The index is best-effort and its sink retries
			// on its own deadline; running it before the ack would put a bounded-but-slow
			// broker write between "durably dead-lettered" and "stops redelivering", so a
			// pod dying in that window would redeliver a dispatch that was already
			// terminal. Nothing is lost by running it after: the authoritative copy is on
			// the terminal subject either way, and a lost index entry costs the operator's
			// VIEW of a give-up rather than the give-up.
			c.index(tctx, msg, rule, action, outcome)
			return
		}
		if i < attempts-1 {
			select {
			case <-time.After(deadLetterWriteBackoff):
			case <-c.procCtx.Done():
			}
		}
	}

	if finalDelivery {
		// No redelivery will follow; the write failed after retries — record an explicit LOSS rather
		// than pretend leaving it unacked will retry. Ack so the (already terminal) message is not left dangling.
		log.Error().Err(err).Str("correlation", msg.CorrelationID()).Str("action", action).
			Msg("LOST connector dispatch: dead-letter write failed on the final delivery; it could be neither delivered nor dead-lettered.")
		c.metrics.recordOutcome(action, outcomeDeadWriteFailed)
		c.ack(msg)
		return
	}
	// Below the cap: leave it unacked so JetStream redelivers (after AckWait) and we retry
	// dead-lettering on the next attempt.
	log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
		Msg("Failed to write connector dispatch to the dead-letter subject; leaving unacked to retry (not yet at the cap).")
}

// index records the give-up in the PLATFORM's dead-letter list (ADR-024) after the verbatim copy is
// safely on this service's own terminal subject.
//
// 🔴 THE GAP IT CLOSES IS AN OPERATOR SEEING NOTHING, FOREVER. connector-dispatch.dead has no reader,
// no store and no query surface: a dispatch written there is invisible to the one place an operator
// asks "what has the platform given up on", and then it ages out with the stream. Every other arm in
// the platform lands in that list; this one did not, so the list an operator trusts was quietly
// missing the noisiest egress path in the product.
//
// 🔑 IT IS AN INDEX ENTRY, NOT A SECOND COPY. The verbatim request stays where it is — a replay of an
// outbound send has to be byte-identical, which a summary cannot be — so the envelope carries no
// Payload. Writing the body twice would double the retention cost of this path to say the same thing.
//
// 🔴 SUBJECT AND SEQUENCE ARE THE ORIGINAL'S COORDINATES ON connector-dispatch, NOT THE COPY'S ON
// connector-dispatch.dead. They are msg's own, and msg is what was consumed from the source stream;
// the copy's sequence cannot be recorded here at all, because WriteMessages returns an error and no
// PubAck, so the write never learns where it landed. CORRELATION is the join onto the dead subject —
// it rides through the writer onto the verbatim copy — so an operator locates the original by
// subject/sequence and the copy by scanning the dead subject for the correlation id. Reading these
// fields as the copy's address sends them to a sequence holding somebody else's message.
//
// 🔑 BEST-EFFORT, AND THAT IS A DIFFERENT THING FROM FAIL-OPEN HERE. The authoritative record is
// already durable one line above; failing to write the index loses the operator's VIEW of a give-up,
// not the give-up itself, and the sink counts every such loss through its own hook. Blocking the ack
// on it would trade a durable terminal record for a redelivery of a dispatch that has already been
// given up on — which is how a visibility improvement becomes a duplicate outbound call.
func (c *DispatchConsumer) index(tctx context.Context, msg messaging.Message, rule, action, outcome string) {
	if c.deadIndex == nil {
		return
	}
	_ = c.deadIndex.Write(tctx, deadletter.Envelope{
		Kind:   deadletter.KindConnectorDispatch,
		Reason: indexReasonFor(outcome),
		Source: connectorsArea,
		Summary: "an outbound connector dispatch was given up on; the request itself is on this " +
			"instance's connector-dispatch dead-letter subject",
		// 🔴 THE DETAIL IS THE BOUNDED OUTCOME LABEL, NEVER THE ENDPOINT'S OWN ERROR TEXT. A send
		// failure's message carries the status and address of a destination the TENANT chose, and
		// this record is read across tenants; the egress boundary closed exactly that oracle.
		Detail:      action + "/" + outcome,
		Attempts:    msg.NumDelivered,
		Subject:     msg.Subject,
		Sequence:    msg.StreamSeq,
		Correlation: msg.CorrelationID(),
		Reference:   rule,
		OccurredAt:  time.Now().UTC(),
	})
}

// connectorsArea is the source recorded on this service's dead letters. It is a literal rather than
// the Microservice's FunctionalArea because the consumer is constructed with a nil Microservice in
// tests, and a source that silently became "" would fail the envelope's own validation on the one
// path that must not fail quietly.
const connectorsArea = "outbound-connectors"

// indexReasonFor maps this service's terminal outcome onto the platform's bounded reason vocabulary.
//
// 🔑 THE RATE-SHED CASE IS WHY THIS IS NOT A ONE-LINER. A shed dispatch is not broken and was never
// attempted — it is work a governed ceiling refused — so reporting it as "exhausted" would send an
// operator to inspect a destination that is perfectly healthy, and reporting it as "unprocessable"
// would call replayable work poison. It gets its own reason.
func indexReasonFor(outcome string) deadletter.Reason {
	switch outcome {
	case outcomeRateLimited:
		return deadletter.ReasonShed
	case outcomeUnsupported, outcomeInvalid:
		return deadletter.ReasonUnprocessable
	default:
		// outcomeDead, and anything a later build adds without revisiting this: exhausted is the
		// vocabulary's own "says nothing about the cause" value, which is the safe default for an
		// outcome nobody has classified rather than a claim about it.
		return deadletter.ReasonExhausted
	}
}

// tenantIsDeleted reads the ADR-077 lifecycle gate, treating an unconfigured gate as "not deleted".
//
// It exists so the nil check and the call are ONE expression that cannot be split: a caller writing
// `c.tenantDeleted(tenant)` against an unconfigured gate panics the worker, and a caller writing
// `c.tenantDeleted != nil && ...` at a second call site is a copy that the next one gets wrong. The
// gate is nil on any instance without user-management configured, so that panic is a real
// deployment, not a hypothetical.
//
// The gate itself fails OPEN — an unresolvable tenant reads active — and governance's
// TenantLifecycleResolver argues why at length. The short version is that this gate exists to stop
// the bleeding early, not to be the guarantee; failing closed would make user-management a hard
// dependency of every tenant's outbound traffic.
func (c *DispatchConsumer) tenantIsDeleted(tenant string) bool {
	return c.tenantDeleted != nil && c.tenantDeleted(tenant)
}

// ack best-effort acks; a failed ack redelivers, and the idempotency key makes the re-run safe.
func (c *DispatchConsumer) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a connector dispatch; it will redeliver (idempotent).")
	}
}

// actionLabel maps a wire kind onto the bounded metric action label, collapsing any unrecognized
// value to actionUnknown so the label set stays a fixed enum {httpCall, publish, unknown}.
func actionLabel(kind string) string {
	switch kind {
	case connectorwire.ConnectorKindHTTPCall, connectorwire.ConnectorKindPublish:
		return kind
	default:
		return actionUnknown
	}
}
