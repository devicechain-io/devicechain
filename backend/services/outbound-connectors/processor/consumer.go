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
// the one that acks (success/poison), naks (transient, redeliver), or dead-letters it (cap
// exhausted / terminal). The pool width is the outbound concurrency ceiling — SD-2's back-pressure:
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
// executor. workers is the outbound concurrency ceiling; backlog is the reader→worker hand-off
// buffer. A nil Microservice (unit tests) leaves metrics nil (every recorder is nil-safe).
func NewDispatchConsumer(ms *core.Microservice, reader messaging.MessageReader, dead messaging.MessageWriter,
	executor *Executor, workers, backlog int) *DispatchConsumer {
	return &DispatchConsumer{
		reader:   reader,
		dead:     dead,
		executor: executor,
		metrics:  newDispatchMetrics(ms),
		backlog:  backlog,
		workers:  workers,
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
			// messages to completion (ack/nak) rather than aborting an in-flight send.
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

// handle processes one dispatch message end-to-end and applies its ack/nak/dead-letter disposition.
// A message with no parseable tenant, an undecodable body, a failed structural validation, or a
// payload/subject tenant mismatch is POISON (a redelivery cannot fix it) — dropped (acked) and
// counted invalid. A well-formed message is executed; the outcome decides ack (sent), nak (transient,
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

	res := c.executor.Execute(tctx, req)
	action := actionLabel(req.Kind)
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
			c.deadLetter(tctx, msg, action, outcomeDead)
			return
		}
		log.Warn().Err(res.err).Str("rule", req.RuleID).Str("tenant", tenant).Int("attempt", msg.NumDelivered).
			Msg("Connector dispatch failed; naking for redelivery.")
		c.metrics.recordOutcome(action, outcomeRetry)
		c.nak(msg)
	default:
		// Terminal (unsupported kind / malformed config that bypassed the publish gate): a redelivery
		// cannot help, so dead-letter it visibly rather than churn the cap or silently drop it.
		log.Error().Err(res.err).Str("rule", req.RuleID).Str("tenant", tenant).
			Msg("Connector dispatch is terminally undeliverable; dead-lettering.")
		c.deadLetter(tctx, msg, action, res.outcome)
	}
}

// deadLetterWriteAttempts bounds the in-process retries of the dead-letter write on the final
// delivery, where a plain nak could not redeliver (see deadLetter).
const deadLetterWriteAttempts = 3

// deadLetter writes the original message verbatim to the terminal dead-letter subject
// ({instance}.{tenant}.connector-dispatch.dead), then acks the original so it stops redelivering.
// tctx already carries the tenant, which the writer requires to scope the subject (fail-closed on
// none).
//
// The write-failure path must not silently lose the request. Its handling turns on whether the
// broker will still redeliver this message: BELOW the redelivery cap a nak redelivers it and we
// retry dead-lettering on the next attempt; AT/ABOVE the cap a nak does NOTHING (JetStream is done
// redelivering after MaxDeliver), so a bare nak there would strand the message forever — never
// executed, never dead-lettered. So on the final delivery we retry the write a bounded number of
// times in-process, and if it still fails we record an explicit, alertable LOSS (never the false
// "will retry") so an operator sees a dispatch that could be neither delivered nor dead-lettered.
func (c *DispatchConsumer) deadLetter(tctx context.Context, msg messaging.Message, action, outcome string) {
	dead := messaging.Message{Value: msg.Value}.WithCorrelationID(msg.CorrelationID())
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
		// than pretend a nak will retry. Ack so the (already terminal) message is not left dangling.
		log.Error().Err(err).Str("correlation", msg.CorrelationID()).Str("action", action).
			Msg("LOST connector dispatch: dead-letter write failed on the final delivery; it could be neither delivered nor dead-lettered.")
		c.metrics.recordOutcome(action, outcomeDeadWriteFailed)
		c.ack(msg)
		return
	}
	// Below the cap: nak so JetStream redelivers and we retry dead-lettering on the next attempt.
	log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
		Msg("Failed to write connector dispatch to the dead-letter subject; naking to retry (not yet at the cap).")
	c.nak(msg)
}

// ack best-effort acks; a failed ack redelivers, and the idempotency key makes the re-run safe.
func (c *DispatchConsumer) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a connector dispatch; it will redeliver (idempotent).")
	}
}

// nak best-effort naks for redelivery.
func (c *DispatchConsumer) nak(msg messaging.Message) {
	if err := msg.Nak(); err != nil {
		log.Warn().Err(err).Msg("Failed to nak a connector dispatch; it will redeliver on ack timeout.")
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
