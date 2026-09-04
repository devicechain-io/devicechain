// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// readErrorBackoff paces a retry after a read failure, so a broker that is briefly away
// does not spin this loop.
const readErrorBackoff = time.Second

// 🔴 THERE IS DELIBERATELY NO IN-PROCESS RETRY HERE, and an earlier version of this file
// had one. It looked like free insurance — ride out a database blip inside one delivery
// attempt, spending none of the five — and it does not work at this seam, because the
// reader FETCHES IN BATCHES whose AckWait timers all start at the fetch. A per-message
// retry budget multiplies by the batch size: a 45-second budget across a batch of 64 is
// three quarters of an hour, during which the broker has long since redelivered everything
// after the first message and the buffered copies are carrying a stale NumDelivered — so
// the final-attempt decision, which is the one that declares a loss, would be taken on a
// count that no longer matches the broker's.
//
// The delivery attempts are the retry. Five of them, AckWait apart, is about five minutes,
// which is the real window; anything longer than that is an outage, and an outage is what
// dead_letters_unstored_total and its alert exist to report.

// Consumer drains the platform's dead-letter stream into the store.
//
// 🔑 IT IS THE ONE CONSUMER THAT MUST NOT DEAD-LETTER ITS OWN FAILURES. Everywhere else
// giving up at the redelivery cap is safe BECAUSE the giving-up is recorded; this is where
// that recording happens, so a give-up here is the failure with nothing behind it.
//
// 🔴 "UNACKED FOREVER" IS NOT AVAILABLE, AND AN EARLIER VERSION OF THIS COMMENT CLAIMED IT
// WAS. Every reader in this platform gets messaging.MaxDeliver (5) — it is pinned in
// consumerConfig and is not a per-reader option — so declining to ack buys five delivery
// attempts, not infinite ones, and after the fifth the broker stops redelivering and the
// letter is stranded on the stream until it ages out. There is no disposition here that
// makes a long database outage lossless.
//
// What is available is spending those five attempts well, and saying so when they run out:
//
//   - A store failure below the cap leaves the message unacked, which buys the next
//     delivery attempt. Five of them, AckWait apart, is about five minutes — enough for
//     an ordinary blip and not enough for an outage, which is the honest shape of it.
//   - A message on its FINAL attempt that still cannot be stored is a LOSS. It is counted
//     and logged as one rather than left to look like a retry, because after this no
//     redelivery follows and nothing else will ever record that failure.
type Consumer struct {
	Microservice *core.Microservice
	reader       messaging.MessageReader
	store        *Store

	stored     prometheus.Counter
	unstorable prometheus.Counter
	// unstored counts letters that reached their final delivery attempt and still could
	// not be written. It is the only outcome here where a failure the platform recorded
	// once is then lost, so it is counted apart from every other error on this path.
	unstored prometheus.Counter

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewConsumer builds the consumer over the dead-letter reader.
func NewConsumer(ms *core.Microservice, reader messaging.MessageReader, store *Store,
	callbacks core.LifecycleCallbacks) *Consumer {
	c := &Consumer{
		Microservice: ms,
		reader:       reader,
		store:        store,
		stored: ms.NewCounter("dead_letters_stored_total",
			"Dead letters written to the queryable store, so a failure a consumer gave up on "+
				"outlives the stream's own seven-day window (ADR-024).", nil),
		unstorable: ms.NewCounter("dead_letters_unstorable_total",
			"Dead-letter messages this consumer could not make sense of — no parseable tenant, "+
				"or a body that is not an envelope. They are ACKED and counted rather than "+
				"retried, because no redelivery makes a malformed message parse.", nil),
		unstored: ms.NewCounter("dead_letters_unstored_total",
			"Dead letters that exhausted every delivery attempt without being stored — the "+
				"store was unreachable for longer than the retries last. The failure they "+
				"described is now recorded nowhere, which is the one thing this consumer "+
				"exists to prevent.", nil),
	}
	c.lifecycle = core.NewLifecycleManager(
		fmt.Sprintf("%s-%s", ms.FunctionalArea, "dead-letter-consumer"), c, callbacks)
	return c
}

func (c *Consumer) Initialize(ctx context.Context) error { return c.lifecycle.Initialize(ctx) }
func (c *Consumer) ExecuteInitialize(ctx context.Context) error {
	c.procCtx, c.procCancel = context.WithCancel(context.Background())
	return nil
}
func (c *Consumer) Start(ctx context.Context) error { return c.lifecycle.Start(ctx) }
func (c *Consumer) ExecuteStart(context.Context) error {
	c.wg.Add(1)
	go c.loop()
	return nil
}
func (c *Consumer) Stop(ctx context.Context) error { return c.lifecycle.Stop(ctx) }
func (c *Consumer) ExecuteStop(context.Context) error {
	if c.procCancel != nil {
		c.procCancel()
	}
	c.wg.Wait()
	return nil
}
func (c *Consumer) Terminate(ctx context.Context) error    { return c.lifecycle.Terminate(ctx) }
func (c *Consumer) ExecuteTerminate(context.Context) error { return nil }

// loop reads until shutdown.
func (c *Consumer) loop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.procCtx.Done():
			return
		default:
		}
		msg, err := c.reader.ReadMessage(c.procCtx)
		if err != nil {
			if errors.Is(err, io.EOF) || c.procCtx.Err() != nil {
				return
			}
			c.reader.HandleResponse(err)
			c.pause()
			continue
		}
		c.handle(msg)
	}
}

func (c *Consumer) pause() {
	select {
	case <-time.After(readErrorBackoff):
	case <-c.procCtx.Done():
	}
}

// handle stores one letter.
//
// Two dispositions, and the asymmetry is the design:
//
//   - A message that cannot be MADE SENSE OF is acked and counted. No redelivery makes a
//     body parse or gives a subject a tenant it never had, so retrying it only holds the
//     stream's consumer behind something that will never move.
//   - A message that could not be STORED is left unacked. That is a database saying no,
//     and the whole value of this consumer is that it does not give up on the record of
//     something else giving up.
func (c *Consumer) handle(msg messaging.Message) {
	tenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).
			Msg("Dropping a dead letter with no parseable tenant in its subject.")
		c.unstorable.Inc()
		c.ack(msg)
		return
	}
	e, err := deadletter.Unmarshal(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("subject", msg.Subject).
			Msg("Dropping a dead letter whose body is not an envelope.")
		c.unstorable.Inc()
		c.ack(msg)
		return
	}
	if err := c.store.Record(c.procCtx, tenant, msg.StreamSeq, msg.AppendTime, e); err != nil {
		if msg.NumDelivered >= messaging.MaxDeliver {
			// 🔴 NO REDELIVERY FOLLOWS. This is the loss, and it is said plainly rather
			// than logged as something that will be retried. Acked so the message is not
			// left dangling on a consumer that will never look at it again.
			log.Error().Err(err).Str("tenant", tenant).Str("kind", string(e.Kind)).
				Int("attempts", msg.NumDelivered).
				Msg("LOST a dead letter: every delivery attempt failed to store it, and the " +
					"failure it recorded is now recorded nowhere.")
			c.unstored.Inc()
			c.ack(msg)
			return
		}
		// Below the cap: leave it UNACKED so AckWait paces the next attempt.
		log.Warn().Err(err).Str("tenant", tenant).Str("kind", string(e.Kind)).
			Int("attempts", msg.NumDelivered).
			Msg("Could not store a dead letter; leaving it unacked for another delivery attempt.")
		return
	}
	c.stored.Inc()
	c.ack(msg)
}

func (c *Consumer) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a dead letter; a redelivery re-stores it idempotently.")
	}
}
