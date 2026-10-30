// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// HOLDING MESSAGES WHILE THE EVENT STORE CANNOT TAKE WRITES.
//
// 🔴 THIS EXISTS BECAUSE A FULL DISK USED TO LOSE ACKNOWLEDGED EVENTS. A write the store
// refused was left unacked, so the broker redelivered it every AckWait (60 s); on the fifth
// delivery (messaging.MaxDeliver) the old disposition gave up, published the event to
// failed-events (which nothing reads) and ACKED it. Five deliveries is four minutes, so any
// store outage longer than that turned the whole in-flight window into acknowledged-then-
// discarded events, one window per four minutes for as long as the outage lasted. The
// GKE calc-cost gate lost 1.72M PUBACKed events to a 32 GiB volume that filled this way.
//
// The delivery budget is for a message that is the problem. When the STORE is the problem
// (rdb.IsStoreUnavailable: disk full, read-only, out of connections or memory, unreachable),
// every message fails the same way and redelivery cannot be blamed on any of them, so
// they are not counted against it and not given up on. They are HELD by the writer that
// found them:
//
//   - kept unacked, and told to the broker as InProgress every AckWait/3, which restarts the
//     ack-wait WITHOUT spending a delivery, so the budget never runs down;
//   - written again on a probe schedule (heldProbeMin doubling to heldProbeMax), and acked
//     only once a commit confirms them, exactly as any other message;
//   - never read past: a writer that holds does not collect, its channel fills, the read loop
//     blocks in handOff, and the consumer stops fetching. The backlog then waits UNREAD in
//     resolved-events, which is the quantity the stream's backpressure gate watches
//     (core/streams BackpressureReaders), so a store outage closes ingest at the gate rather
//     than draining the stream into the void.
//
// The held count is bounded: one batch per writer (persistence.maxBatch x writers).
// Shutdown ends the hold (Halt), leaving the messages unacked for redelivery to the next pod.
var (
	heldProbeMin = 2 * time.Second
	heldProbeMax = 30 * time.Second
)

// heldProgressEvery is how often held messages are told they are still in progress. A
// third of AckWait leaves two missed beats of margin before the broker redelivers.
var heldProgressEvery = messaging.AckWait / 3

// hold keeps p unacked and takes it out of the batch flow; settleHeld writes it again.
func (ep *EventPersistenceWorker) hold(p pendingEvent, err error) {
	// The message's duration/in-flight record is settled now (as a retry), once: it is
	// written again later but is not a new in-flight message.
	p.done(core.ResultRetry)
	p.done = func(string) {}
	if len(ep.held) == 0 {
		log.Warn().Err(err).Int("worker", ep.WorkerId).
			Msg("Event store cannot take writes; holding events (unacked, not counting against their delivery budget) until it recovers")
	}
	ep.held = append(ep.held, p)
	ep.metrics.holding(1)
}

// settleHeld blocks until every held message has been committed and acked, or Halt closes.
// Only the worker's own goroutine calls it, so held needs no lock.
func (ep *EventPersistenceWorker) settleHeld(ctx context.Context) {
	wait := heldProbeMin
	for len(ep.held) > 0 {
		if !ep.waitHeld(wait) {
			ep.releaseHeld()
			return
		}
		batch := ep.held
		ep.held = nil
		ep.metrics.holding(-len(batch))
		before := len(batch)
		ep.persistBatch(ctx, batch)
		switch {
		case len(ep.held) == 0:
			log.Info().Int("worker", ep.WorkerId).Int("events", before).
				Msg("Event store accepts writes again; held events committed")
		case len(ep.held) < before:
			wait = heldProbeMin
		default:
			wait = min(wait*2, heldProbeMax)
		}
	}
}

// waitHeld sleeps for d, telling the broker every held message is in progress on each
// heldProgressEvery tick. It reports false if Halt closed first.
func (ep *EventPersistenceWorker) waitHeld(d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(heldProgressEvery)
	defer tick.Stop()
	for {
		select {
		case <-ep.Halt:
			return false
		case <-deadline.C:
			return true
		case <-tick.C:
			for _, p := range ep.held {
				if err := p.msg.InProgress(); err != nil {
					log.Warn().Err(err).Int("worker", ep.WorkerId).Msg("Could not extend a held event's acknowledgement window")
				}
			}
		}
	}
}

// releaseHeld drops the held messages from memory on shutdown, still unacked: the broker
// redelivers them to the next pod.
func (ep *EventPersistenceWorker) releaseHeld() {
	ep.metrics.holding(-len(ep.held))
	ep.held = nil
}
