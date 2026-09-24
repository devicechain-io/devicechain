// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// deadLetterWriteBackoff is the short pause between bounded dead-letter write retries on the final
// delivery (see deadLetter).
const deadLetterWriteBackoff = 100 * time.Millisecond

// sendMargin is the headroom one dispatch leaves between the end of its send and the moment the
// broker redelivers the message (its AckDeadline): time for the ack, or the dead-letter write, to
// land before a second worker is handed the same dispatch.
const sendMargin = 5 * time.Second

// maxSend is the longest any single outbound send may run: the executor clamps every send timeout,
// authored or configured, to this shared ceiling (effectiveSendTimeout).
const maxSend = time.Duration(connectorwire.MaxTimeoutMs) * time.Millisecond

// DispatchConsumer is the outbound-connectors service's durable consumer of the connector-dispatch
// stream (ADR-060 §4 / slice C3). It mirrors the notification-management dispatch model: a single
// read loop hands each message to a bounded worker pool, and the worker that dispatches a message is
// the one that acks (success/poison/refused for a deleted tenant), leaves it unacked (transient,
// redeliver after AckWait), or dead-letters it (cap exhausted / terminal). The pool width is the
// outbound concurrency ceiling — SD-2's back-pressure — and it is also the reader's capacity
// (messaging.ReaderWithCapacity, in main): the reader fetches only as many dispatches as there are
// free workers, so once every worker is busy on a slow target nothing more is pulled, and unacked
// work stays durable on the (per-tenant bounded) stream rather than aging in an in-memory queue
// while the broker's redelivery clock runs.
//
// Idempotency rides in each message (the content-addressed key), so an at-least-once redelivery or a
// DETECT replay collapses downstream to one execution (an endpoint honoring X-DC-Idempotency-Key);
// the consumer therefore never needs cross-message state and scales out as a queue group.
type DispatchConsumer struct {
	reader   messaging.MessageReader
	dead     messaging.MessageWriter
	executor *Executor
	metrics  *DispatchMetrics

	// deadIndex writes the platform-wide (ADR-024) index entry for a give-up, so an outbound
	// dispatch the service abandoned appears in the one list an operator reads rather than only on
	// this service's own terminal subject. See index for why it is best-effort.
	deadIndex *deadletter.Sink

	// deadLetters is this service's dead-letter producer. The index sink comes from it, and so
	// does the source stamped on every index entry; and a verbatim copy that could not be written
	// on the final delivery is counted on its dead_letter_lost_total — the one series the
	// DeadLetterWriteLost alert selects, by name, for every service that loses a letter.
	deadLetters *deadletter.Producer

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

	// readPacer spaces out retries after a failing read and ends the loop once the failures stop
	// looking transient.
	//
	// 🔴 IT IS HANDED IN RATHER THAN BUILT HERE, and the reason is the same one that keeps a
	// Microservice out of this struct: a pacer needs one only to report an exhausted budget, and
	// taking the Microservice back to get it would put the hook the metrics defect hung on right
	// back where it was. Passing the pacer gives this consumer the capability it needs and none of
	// the one it must not have.
	//
	// 🔴 AND IT IS THE OPPOSITE OF metrics IN THE ONE WAY THAT MATTERS, though both are built
	// outside and handed in. Counters belong to the PROCESS and must be shared across every
	// consumer this callback builds; a pacer holds the state of ONE unbroken run of failures on ONE
	// goroutine, so each consumer needs its OWN. Sharing one would let two loops' failures add up
	// into a give-up neither earned, and would race besides.
	readPacer *core.ReadPacer

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
// (nil disables the refusal). workers is the outbound concurrency ceiling, and should equal the
// capacity the reader was built with; the reader→worker hand-off holds one message per worker and
// no more. Nil metrics (unit tests) run the consumer unmeasured (every
// recorder is nil-safe). deadLetters is the service's dead-letter producer, built once in the
// initialize phase; it is required.
//
// metrics is built once in the initialize phase (see NewDispatchMetrics) and shared by every
// consumer this service constructs, because that callback is connection-scoped and the
// counters are not.
//
// 🔴 IT TAKES NO Microservice, DELIBERATELY. It used to, for one purpose: building the
// counters right here — which is what made a second entry into that callback panic.
// Leaving the parameter behind
// with nothing reading it would leave the hook the defect hung on in place.
//
// tenantDeleted is a CONSTRUCTOR ARGUMENT rather than a setter for the same reason the rate limiter
// is: this is the only consumer in the service, so the property worth buying is that a second one
// added later cannot be constructed without answering the question.
func NewDispatchConsumer(reader messaging.MessageReader, dead messaging.MessageWriter,
	deadIndex deadletter.Writer, deadLetters *deadletter.Producer, executor *Executor,
	rate *core.TenantRateLimiter, waitBudget time.Duration,
	tenantDeleted func(string) bool, workers int, metrics *DispatchMetrics,
	readPacer *core.ReadPacer) *DispatchConsumer {
	// 🔴 A NIL PACER IS REFUSED RATHER THAN DEFAULTED, and an earlier version of this defaulted it.
	// Defaulting looks harmless — the substitute still paces and still ends the loop — but it
	// drops the one thing the pacer is here for: with no microservice it cannot call FailNow, so
	// an exhausted budget becomes a single Error line and a pod that stays READY with a dead
	// consumer. That is the exact failure this consumer adopted a pacer to remove, arrived at by
	// forgetting an argument. Refusing means a call site that omits it dies at startup, where the
	// stack names the line, instead of dispatching nothing six months later.
	//
	// A pacer built with a nil MICROSERVICE is still fine and is the documented unit-test shape;
	// what is refused is no pacer at all.
	if readPacer == nil {
		panic("outbound-connectors: NewDispatchConsumer needs a read pacer; without one an " +
			"exhausted retry budget cannot end the process, and the pod reports ready while " +
			"dispatching nothing")
	}
	// 🔴 A NIL PRODUCER IS REFUSED FOR THE SAME REASON. Without one, a verbatim copy lost on the
	// final delivery would be counted only as a disposition label that no alert reads — which is
	// exactly how this service's losses went unalerted before the counter was shared.
	if deadLetters == nil {
		panic("outbound-connectors: NewDispatchConsumer needs the service's dead-letter producer; " +
			"without one a dispatch lost on its final delivery alerts nobody")
	}
	// An INDEX sink: its loss is counted here, as a disposition, and never on
	// dead_letter_lost_total — the verbatim copy is already durable, so a lost index entry costs the
	// operator's view of a give-up, not the give-up. See deadletter.Producer.NewIndexSink.
	var index *deadletter.Sink
	if deadIndex != nil {
		index = deadLetters.NewIndexSink(deadIndex, func(error) {
			metrics.recordOutcome(actionUnknown, outcomeDeadIndexFailed)
		})
	}
	return &DispatchConsumer{
		reader:        reader,
		dead:          dead,
		deadIndex:     index,
		deadLetters:   deadLetters,
		executor:      executor,
		metrics:       metrics,
		rate:          rate,
		waitBudget:    waitBudget,
		tenantDeleted: tenantDeleted,
		readPacer:     readPacer,
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
	// One place per worker and no more: the capacity reader already holds at most `workers`
	// dispatches, so a deeper channel would never fill — and a deeper one in front of a reader
	// WITHOUT capacity is how a burst came to sit past AckWait and be sent twice.
	c.messages = make(chan messaging.Message, c.workers)
	for i := 0; i < c.workers; i++ {
		c.workerWG.Add(1)
		go func() {
			defer c.workerWG.Done()
			// Workers run on a background context so that on shutdown they drain the buffered
			// messages to completion (ack or leave-unacked) rather than aborting an in-flight send.
			// messaging.Process returns the message's reader slot when handle returns, whichever
			// disposition it took.
			for msg := range c.messages {
				messaging.Process(msg, func(m messaging.Message) { c.handle(context.Background(), m) })
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

// run drains the dispatch stream, handing each message to the worker pool.
//
// 🔴 STOPPING IS THE POINT when the read errors never clear. This service EMITS, so a loop that
// spins silently on an error the reader keeps handing back leaves a ready pod dispatching
// nothing — every connector the tenant configured quietly does not fire, and the stream's
// per-tenant bound then discards the requests. A restart is visible; a ready pod delivering
// nothing is not. messaging.RunConsumer ends the loop once the pacer's budget is spent.
func (c *DispatchConsumer) run() {
	defer c.readerWG.Done()
	messaging.RunConsumer(c.procCtx, c.reader, c.readPacer, func(msg messaging.Message) bool {
		return c.handOff(c.procCtx, msg)
	})
}

// handOff gives one dispatch message to the worker pool, and reports whether the read loop
// should carry on. It abandons the handoff on shutdown so the loop can exit rather than block
// on a full channel; the message is unacked at that point, so it redelivers after restart.
func (c *DispatchConsumer) handOff(ctx context.Context, msg messaging.Message) bool {
	select {
	case c.messages <- msg:
		return true
	case <-ctx.Done():
		return false
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
	// The message's AckDeadline rides the context down to the executor, which caps the send at it
	// (sendContext). It is a value, not a deadline, so the dead-letter writes on tctx are not cut
	// short by it.
	tctx = messaging.WithAckDeadline(tctx, msg)
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
	//
	// IT METERS THE TIME REACT METERED. The dispatch carries triggeredAt, the time the triggering
	// telemetry reached the platform, and core.MeteringTime — the one reader both ends share — picks
	// it (capped at this message's broker time), or the broker time when it is missing, or now. A
	// backlog that was within the tenant's ceiling when it happened therefore passes at drain speed
	// here as it did at the source: charged at arrival it would land at one instant, and the workers
	// would sit out the tenant's rate one token at a time while every other tenant queued behind them.
	//
	// The worker then waits up to waitBudget for a token: a dispatch whose token on that timeline is
	// further off than the budget is a tenant over its ceiling on the timeline its telemetry arrived
	// on, and it is SHED to the dead-letter subject. It never leaves a rate-shed message unacked, so
	// rate-limiting can never churn the redelivery (poison) cap, and because the tenant is over quota
	// a redelivery would not help either. A shed consumes no token, so it does not deepen the deficit.
	//
	// Head-of-line blocking is confined to a tenant that is over quota: only its dispatches wait, for
	// up to waitBudget each, adding a bounded delivery latency to other tenants' dispatches queued
	// behind them. It self-limits — a tenant far over quota is shed at once rather than waiting the
	// full budget — and the per-tenant bounded durable stream is the real buffer.
	if c.rate != nil {
		// 🔴 THE WAIT ENDS IN TIME FOR THE SEND TO FINISH BEFORE THE BROKER REDELIVERS. The
		// message's clock started when it was fetched (its AckDeadline), and after the wait come a
		// secret resolve and a send, each with its own ceiling. So the wait may run to the earlier
		// of its own budget and the latest moment those can still start and finish sendMargin
		// before that deadline. A message with no AckDeadline (not from a capacity reader) waits
		// its budget as before.
		//
		// A deadline already spent needs no check of its own: the limiter's WaitAt refuses a
		// context that is already done, or one whose deadline its wait cannot cover, before it
		// takes a token, so that case arrives below as a capped wait error like any other.
		waitDeadline, capped := c.rateWaitDeadline(tctx, time.Now())
		// Derive the wait from procCtx (cancelled on Stop) so a rolling-update drain aborts an
		// in-progress rate wait rather than blocking Stop for the budget: a wait interrupted by
		// shutdown ABANDONS the message unacked (it redelivers after restart for a fresh admission),
		// rather than dead-lettering a message that was only waiting on rate. A budget timeout (not a
		// shutdown) is a genuine sustained-over-quota shed.
		waitCtx, cancel := context.WithDeadline(c.procCtx, waitDeadline)
		at, clk := core.MeteringTime(req.TriggeredAt, msg.AppendTime)
		c.metrics.recordClockFallback(clk)
		err := c.rate.WaitAt(waitCtx, tenant, at)
		cancel()
		if err != nil {
			if c.procCtx.Err() != nil {
				log.Info().Str("rule", req.RuleID).Str("tenant", tenant).
					Msg("Abandoning connector dispatch rate-wait on shutdown; it will redeliver on restart.")
				return
			}
			if capped {
				// The wait was cut short by the redelivery clock, not by the budget, so this is not
				// evidence of a tenant sustained over quota: the broker will redeliver this dispatch
				// before a send could finish, and starting one would be a duplicate in the making.
				// It is disposed of exactly as a transient send failure is.
				c.retryOrDeadLetter(tctx, msg, req.RuleID, tenant, action, errTooLateToSend)
				return
			}
			if errors.Is(err, core.ErrWaitBudget) {
				// Debug, not Warn: by design a rising rate_limited COUNT (the metric) is the operator
				// signal; a per-message warn would flood the log for exactly the over-quota tenant
				// this fires on.
				log.Debug().Str("rule", req.RuleID).Str("tenant", tenant).Str("action", action).
					Msg("Connector dispatch shed: tenant over its outbound egress rate beyond the smoothing budget; dead-lettering.")
				c.deadLetter(tctx, msg, req.RuleID, action, outcomeRateLimited)
				return
			}
			// Anything else is not a verdict on the tenant's rate — a wait interrupted by its own
			// deadline, or a limiter that refused outright — so it is disposed of as a transient
			// failure rather than dead-lettered as a shed.
			c.retryOrDeadLetter(tctx, msg, req.RuleID, tenant, action, err)
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
		c.retryOrDeadLetter(tctx, msg, req.RuleID, tenant, action, res.err)
	default:
		// Terminal (unsupported kind / malformed config that bypassed the publish gate): a redelivery
		// cannot help, so dead-letter it visibly rather than churn the cap or silently drop it.
		log.Error().Err(res.err).Str("rule", req.RuleID).Str("tenant", tenant).
			Msg("Connector dispatch is terminally undeliverable; dead-lettering.")
		c.deadLetter(tctx, msg, req.RuleID, action, res.outcome)
	}
}

// errTooLateToSend is the cause recorded for a dispatch whose rate wait was cut short by its
// message's redelivery deadline: a send started then could not finish before the broker
// redelivered the message.
var errTooLateToSend = errors.New("cannot send before the message's redelivery deadline")

// retryOrDeadLetter disposes of a dispatch that did not go out but might on a later delivery — a
// transient send failure, or a rate wait cut short by the redelivery deadline. It is the ONE place
// this consumer decides "this is the final delivery, so letter it": below the cap the message is
// left UNACKED (not nak'd) so AckWait paces its redelivery — an immediate nak would burn MaxDeliver
// in ~1.4ms inside an outage (ADR-030); at the cap no redelivery follows, so it is dead-lettered
// rather than stranded.
func (c *DispatchConsumer) retryOrDeadLetter(tctx context.Context, msg messaging.Message, rule, tenant, action string, cause error) {
	if msg.NumDelivered >= messaging.MaxDeliver {
		log.Error().Err(cause).Str("rule", rule).Str("tenant", tenant).Int("attempts", msg.NumDelivered).
			Msg("Connector dispatch dead-lettered on its final delivery.")
		c.deadLetter(tctx, msg, rule, action, outcomeDead)
		return
	}
	log.Warn().Err(cause).Str("rule", rule).Str("tenant", tenant).Int("attempt", msg.NumDelivered).
		Msg("Connector dispatch did not go out; leaving it unacked for redelivery.")
	c.metrics.recordOutcome(action, outcomeRetry)
}

// rateWaitDeadline is when a dispatch's egress rate wait must give up: now + waitBudget, or — when
// that is later — the latest moment a secret resolve and a send, each at its ceiling, can still
// start and finish sendMargin before the message's AckDeadline. capped reports that the AckDeadline
// set it. A context with no AckDeadline gets the budget alone.
func (c *DispatchConsumer) rateWaitDeadline(ctx context.Context, now time.Time) (deadline time.Time, capped bool) {
	deadline = now.Add(c.waitBudget)
	if ackDeadline, ok := messaging.AckDeadlineFrom(ctx); ok {
		if latest := ackDeadline.Add(-(sendMargin + secretResolveTimeout + maxSend)); latest.Before(deadline) {
			return latest, true
		}
	}
	return deadline, false
}

// deadLetterWriteAttempts bounds the in-process retries of the dead-letter write on the final
// delivery, where leaving the message unacked could not redeliver it (past the cap; see deadLetter).
const deadLetterWriteAttempts = 3

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
	// through the NATS writer (it propagates non-correlation headers). The value is the outcome*
	// label (rate_limited / dead / unsupported / invalid); the platform's max-delivery recorder
	// writes deadletter.DeadReasonNoOutcome under the same key.
	//
	// The dedup id is the one that recorder derives for the same delivery, so if this arm overran
	// the message's ack window and the recorder has already copied it, the copy is stored once.
	dead := messaging.Message{Value: msg.Value, Headers: map[string]string{deadletter.HeaderDeadReason: outcome},
		DedupID: deadletter.OriginID(msg)}.
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

		// Counted twice, on purpose, because the two counters answer different questions. The
		// producer's dead_letter_lost_total is the alertable LOSS, on the series every service shares
		// and the alert selects by name; the outcome label keeps connector_dispatch_total a complete
		// per-dispatch disposition breakdown whose outcomes still sum to the dispatches handled.
		c.deadLetters.Lost()
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
	// WriteFor fills the kind (connector-dispatch's declared one), subject, sequence, attempts and
	// correlation from msg, and the dedup id the max-delivery recorder shares.
	_ = c.deadIndex.WriteFor(tctx, msg, deadletter.Envelope{
		Reason: indexReasonFor(outcome),
		Summary: "an outbound connector dispatch was given up on; the request itself is on this " +
			"instance's connector-dispatch dead-letter subject",
		// 🔴 THE DETAIL IS THE BOUNDED OUTCOME LABEL, NEVER THE ENDPOINT'S OWN ERROR TEXT. A send
		// failure's message carries the status and address of a destination the TENANT chose, and
		// this record is read across tenants; the egress boundary closed exactly that oracle.
		Detail:     action + "/" + outcome,
		Reference:  rule,
		OccurredAt: time.Now().UTC(),
	})
}

// indexReasonFor maps this service's terminal outcome onto the platform's bounded reason vocabulary.
//
// 🔴 EVERY OUTCOME GETS AN EXPLICIT CASE, AND THE DEFAULT CLAIMS NOTHING. deadletter.ReasonExhausted
// is not a neutral "says nothing about the cause" value: it is the one reason for which
// Reason.WorkWasAttempted answers TRUE, which is what tells a consumer it may SETTLE state on the
// letter. Returning it for an outcome nobody has classified is the skip-list shape that method's
// contract forbids — an outcome added later would assert "we attempted this and lost it" on the
// strength of nobody having thought about it. So the default returns a not-attempted reason and an
// unclassified outcome is inert until someone classifies it.
//
// 🔑 THE RATE-SHED CASE IS WHY THIS IS NOT A ONE-LINER. A shed dispatch is not broken and was never
// attempted — it is work a governed ceiling refused — so reporting it as "exhausted" would send an
// operator to inspect a destination that is perfectly healthy, and reporting it as "unprocessable"
// would call replayable work poison. It gets its own reason.
func indexReasonFor(outcome string) deadletter.Reason {
	switch outcome {
	case outcomeDead:
		// The only outcome here that describes attempted work: the send was made and remade to
		// the redelivery cap and never succeeded.
		return deadletter.ReasonExhausted
	case outcomeRateLimited:
		return deadletter.ReasonShed
	case outcomeUnsupported, outcomeInvalid, outcomeBlocked:
		// All three are the producer looking at the dispatch and declining it. outcomeBlocked
		// especially: the egress boundary refused the destination before a byte was written, so
		// nothing was attempted and there is nothing for a consumer to settle.
		return deadletter.ReasonUnprocessable
	default:
		// Anything a later build adds without revisiting this. Unprocessable is the honest
		// not-attempted answer for an outcome that reached a terminal sink by a route nobody
		// described — never exhausted, which would claim an attempt that may not have happened.
		return deadletter.ReasonUnprocessable
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

// ack best-effort acks; a failed ack redelivers, and the redelivery CALLS THE DESTINATION AGAIN.
// This service forwards the idempotency key and does not deduplicate on it, so the re-run is safe
// only where the destination deduplicates on that key.
func (c *DispatchConsumer) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a connector dispatch; it will redeliver and reach its destination again.")
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
