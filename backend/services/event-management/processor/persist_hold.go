// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// HOLDING MESSAGES WHILE THE EVENT STORE CANNOT TAKE WRITES.
//
// 🔴 THIS EXISTS BECAUSE A FULL DISK USED TO LOSE ACKNOWLEDGED EVENTS. A write the store
// refused was left unacked, so the broker redelivered it every AckWait (60 s); on the fifth
// delivery (messaging.MaxDeliver) the old disposition gave up, published the event to
// failed-events (which nothing reads) and ACKED it. Five deliveries is four minutes. When the
// store answered every statement at once with an error (a full disk, no free connections,
// read-only, a refused connection), an outage longer than that turned the whole in-flight
// window into acknowledged-then-discarded events, one window per four minutes for as long as
// the outage lasted. When each attempt instead hung (no endpoints, a crash-looping primary),
// the batch-to-single fallback made each writer take minutes per batch, the handoff channel
// filled and the reader stopped fetching: a throttle, so the loss window was longer and
// depended on the outage's shape. The hold now holds in every case. The
// GKE calc-cost gate lost 1.72M PUBACKed events to a 32 GiB volume that filled this way.
//
// The delivery budget is for a message that is the problem. When the STORE is the problem,
// every message fails the same way and redelivery cannot be blamed on any of them, so
// they are not counted against it and not given up on. They are HELD by the writer that
// found them:
//
//   - kept unacked, and told to the broker as InProgress, which restarts the ack-wait WITHOUT
//     spending a delivery: at once when a message is held, then whenever a third of AckWait
//     has passed, and before and after every probe (holdTuning.ProgressEvery);
//   - written again only after the store answers a trivial probe, on a jittered backoff
//     (holdTuning.ProbeMin doubling to holdTuning.ProbeMax), and acked only once a commit confirms them;
//   - never read past: a writer that holds does not collect, its channel fills, the read loop
//     blocks in handOff, and the consumer stops fetching. The backlog then waits UNREAD in
//     resolved-events, which is the quantity the stream's backpressure gate watches
//     (core/streams BackpressureReaders), so a store outage closes ingest at the gate rather
//     than draining the stream into the void.
//
// 🔴 WHAT COUNTS AS "THE STORE IS DOWN" IS A QUESTION ASKED OF THE STORE, NOT READ OFF AN ERROR.
// Two kinds of error qualify:
//
//   - the server SAYING so (rdb.IsStoreRefusingWrites: disk full, too many connections,
//     read-only, I/O error) -- no second opinion needed;
//   - a connection-class error (rdb.IsConnectionFailure), which a payload that crashes its
//     backend produces too. That one is held only if a probe (SELECT 1, bounded by
//     heldProbeTimeout) FAILS. If the probe succeeds the store is up and the message is the
//     problem, so it takes the ordinary path: set aside from its batch, and counted against
//     its delivery budget as before.
//
// The held count is bounded: one batch per writer (persistence.maxBatch x writers).
// Shutdown ends the hold (Halt), leaving the messages unacked for redelivery to the next pod.
// holdTuning is the hold's timing (storehold.Tuning); tests shorten it.
var holdTuning = storehold.DefaultTuning()

// holder is this worker's storehold.Holder, built on first use because the worker is also
// assembled by literal in tests. The classification, the probe, the beat cadence and the held
// gauge are the shared definition in core/storehold; what stays here is the BATCH shape: a
// writer holds the messages of a failed batch itself and writes them again as a batch.
func (ep *EventPersistenceWorker) holder() *storehold.Holder {
	if ep.hold_ == nil {
		h := &storehold.Holder{
			Name: "event-persistence",
			Halt: ep.Halt,
			Tune: holdTuning,
			Probe: func(ctx context.Context) error {
				return ep.Api.PersistInTx(ctx, func(db *gorm.DB) error { return db.Exec("SELECT 1").Error })
			},
		}
		ep.hold_ = h
	}
	return ep.hold_
}

// storeAnswers probes the event store with a trivial query and reports whether it answered.
func (ep *EventPersistenceWorker) storeAnswers(ctx context.Context) bool {
	return ep.holder().StoreAnswers(ctx)
}

// outage reports whether err means the STORE, and not the message, is what failed.
func (ep *EventPersistenceWorker) outage(ctx context.Context, err error) bool {
	// A probe cached from before the failed write began is not evidence about why it failed
	// (storehold.StoreAnswersSince): on a flapping database it would call a connection reset a
	// poison message. writeStart is when the write that produced err began.
	return ep.holder().OutageSince(ctx, err, ep.writeStart)
}

// hold keeps p unacked and takes it out of the batch flow; settleHeld writes it again.
func (ep *EventPersistenceWorker) hold(p pendingEvent, err error) {
	// The message's duration/in-flight record is settled now (as a retry), once: it is
	// written again later but is not a new in-flight message.
	p.done(core.ResultRetry)
	p.done = func(string) {}
	if len(ep.held) == 0 && ep.holdStart.IsZero() {
		ep.holdStart = time.Now()
		log.Warn().Err(err).Int("worker", ep.WorkerId).
			Msg("Event store cannot take writes; holding events (unacked, not counting against their delivery budget) until it recovers")
	}
	// Told at once, not at the next tick: it may already have waited most of an AckWait in
	// the channel or the fetch buffer.
	ep.beat(&p, p.progressAt.IsZero())
	ep.held = append(ep.held, p)
}

// beat tells the broker p is in progress when it has not been told within the tuning's
// ProgressEvery (or always, when force is set).
func (ep *EventPersistenceWorker) beat(p *pendingEvent, force bool) {
	ep.holder().Beat(p.msg, &p.progressAt, force)
}

// beatAll sends the progress beats that are due.
func (ep *EventPersistenceWorker) beatAll() {
	for i := range ep.held {
		ep.beat(&ep.held[i], false)
	}
}

// syncHeldGauge moves the held gauge by the NET change since it was last set, so it never
// reads 0 in the middle of a probe.
func (ep *EventPersistenceWorker) syncHeldGauge() {
	if d := len(ep.held) - ep.counted; d != 0 {
		ep.metrics.holding(d)
		ep.counted = len(ep.held)
	}
}

// settleHeld blocks until every held message has been committed and acked, or Halt closes.
// Only the worker's own goroutine calls it, so held needs no lock.
func (ep *EventPersistenceWorker) settleHeld(ctx context.Context) {
	ep.syncHeldGauge()
	wait := holdTuning.ProbeMin
	for len(ep.held) > 0 {
		if !ep.waitHeld(storehold.Jitter(wait)) {
			ep.releaseHeld()
			return
		}
		ep.beatAll()
		if !ep.storeAnswers(ctx) {
			ep.beatAll()
			wait = min(wait*2, holdTuning.ProbeMax)
			continue
		}
		batch := ep.held
		ep.held = nil
		before := len(batch)
		ep.persistBatch(ctx, batch)
		ep.beatAll()
		ep.syncHeldGauge()
		switch {
		case len(ep.held) == 0:
			log.Info().Int("worker", ep.WorkerId).Int("events", before).Dur("heldFor", time.Since(ep.holdStart)).
				Msg("Event store accepts writes again; held events committed")
			ep.holdStart = time.Time{}
		case len(ep.held) < before:
			wait = holdTuning.ProbeMin
		default:
			wait = min(wait*2, holdTuning.ProbeMax)
		}
	}
}

// waitHeld sleeps for d, sending the progress beats that fall due meanwhile. It reports
// false if Halt closed first.
func (ep *EventPersistenceWorker) waitHeld(d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		step := min(holdTuning.ProgressEvery/2, max(time.Millisecond, time.Until(ep.nextBeat())))
		t := time.NewTimer(step)
		select {
		case <-ep.Halt:
			t.Stop()
			return false
		case <-deadline.C:
			t.Stop()
			return true
		case <-t.C:
			ep.beatAll()
		}
	}
}

// nextBeat is when the earliest held message next needs a beat.
func (ep *EventPersistenceWorker) nextBeat() time.Time {
	next := time.Now().Add(holdTuning.ProgressEvery)
	for i := range ep.held {
		if t := ep.held[i].progressAt.Add(holdTuning.ProgressEvery); t.Before(next) {
			next = t
		}
	}
	return next
}

// releaseHeld drops the held messages from memory on shutdown, still unacked: the broker
// redelivers them to the next pod.
func (ep *EventPersistenceWorker) releaseHeld() {
	ep.held = nil
	ep.holdStart = time.Time{}
	ep.syncHeldGauge()
}
