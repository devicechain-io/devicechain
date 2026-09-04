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

// readErrorBackoff paces a retry after a read or store failure, so a database that is
// briefly away does not spin this loop.
const readErrorBackoff = time.Second

// Consumer drains the platform's dead-letter stream into the store.
//
// 🔑 IT IS THE ONE CONSUMER THAT MUST NOT DEAD-LETTER ITS OWN FAILURES, which is why its
// disposition is the opposite of every other consumer's here: a message it cannot store is
// left UNACKED, forever if need be, rather than dropped at the redelivery cap. Everywhere
// else giving up is safe because the giving-up is recorded; this is where that recording
// happens, so a give-up here is the failure with nothing behind it. The stream's own
// seven-day age-out is the real bound, and losing a letter to that is at least visible as
// a growing consumer backlog rather than as silence.
type Consumer struct {
	Microservice *core.Microservice
	reader       messaging.MessageReader
	store        *Store

	stored     prometheus.Counter
	unstorable prometheus.Counter

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
	if err := c.store.Record(c.procCtx, tenant, msg.StreamSeq, e); err != nil {
		// UNACKED, deliberately — see the type doc. The stream's age-out is the bound.
		log.Error().Err(err).Str("tenant", tenant).Str("kind", string(e.Kind)).
			Msg("Could not store a dead letter; leaving it unacked to retry.")
		c.pause()
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
