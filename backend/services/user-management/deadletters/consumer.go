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

// storeRetryBudget is how long one delivery attempt spends retrying the store in process.
//
// 🔑 IT IS SHORTER THAN messaging.AckWait ON PURPOSE. Retrying past the ack deadline would
// have the broker redeliver the same message to this same consumer while the first copy is
// still working on it — spending a delivery attempt to duplicate work rather than to
// extend it. Staying inside the window means the five attempts are five INDEPENDENT
// windows rather than five overlapping ones.
const storeRetryBudget = 45 * time.Second

// storeRetryInterval paces those in-process attempts.
const storeRetryInterval = 3 * time.Second

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
//   - A store failure is retried IN PROCESS first (storeRetryBudget), so a blip costs no
//     delivery attempts at all. That is the common case and it is fully covered.
//   - Only if that budget is exhausted is the message left unacked, which buys the next
//     delivery attempt — up to five, roughly five minutes of AckWait-paced retries on top
//     of the in-process ones.
//   - A message on its FINAL attempt that still cannot be stored is a LOSS. It is counted
//     and logged as one rather than left to look like a retry, because after this no
//     redelivery follows and nothing else will ever record that failure.
type Consumer struct {
	Microservice *core.Microservice
	reader       messaging.MessageReader
	store        *Store

	// retryBudget and retryInterval pace the in-process retry. They are FIELDS rather
	// than the constants they default to because a 45-second budget in a struct with no
	// override is a branch no test can reach in a reasonable time — and this is the
	// branch that decides whether a store outage costs a delivery attempt.
	retryBudget   time.Duration
	retryInterval time.Duration

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
		Microservice:  ms,
		reader:        reader,
		store:         store,
		retryBudget:   storeRetryBudget,
		retryInterval: storeRetryInterval,
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
	if err := c.recordWithRetry(tenant, msg.StreamSeq, e); err != nil {
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

// recordWithRetry writes one letter, retrying inside this delivery attempt's own window.
//
// The retry is what makes a database blip free: it costs no delivery attempts, which are
// the scarce thing here (there are five, and they cannot be extended). It stops short of
// AckWait so the broker does not redeliver on top of a call still in flight.
func (c *Consumer) recordWithRetry(tenant string, streamSeq uint64, e deadletter.Envelope) error {
	deadline := time.Now().Add(c.retryBudget)
	var err error
	for {
		if err = c.store.Record(c.procCtx, tenant, streamSeq, e); err == nil {
			return nil
		}
		if time.Now().Add(c.retryInterval).After(deadline) {
			return err
		}
		select {
		case <-time.After(c.retryInterval):
		case <-c.procCtx.Done():
			return err
		}
	}
}

func (c *Consumer) ack(msg messaging.Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Failed to ack a dead letter; a redelivery re-stores it idempotently.")
	}
}
