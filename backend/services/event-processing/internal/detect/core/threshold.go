// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
)

// A Threshold rule's level is meant to be the level of the series' NEWEST reading: raised while the
// newest reading matches, clear while it does not. It used to be the level of the LAST reading to
// ARRIVE, which is a different thing whenever readings are reordered — and the resolved-events
// stream does reorder them (the resolver pool, a catch-up backlog, two replicas, a redelivery after
// a failed publish). Three cases ended at the wrong level:
//
//   - match@1 raises; match@3 is absorbed by the latch; non-match@2 then clears the alarm — it is
//     not older than the raise at 1, so the stale-falling-edge guard in resolve lets it through.
//   - the same with the non-match redelivered a minute later, far behind the frontier: Threshold had
//     no lateness bound at all, and the reading was counted nowhere.
//   - non-match@80 clears; a match@10 redelivered a minute later raises again, stamped before the
//     clear it follows.
//
// The two states are guarded differently, so that state is held only for raised series.
//
// 🔑 RAISED: THE CURSOR. A raised series keeps its newest reading time (Engine.newest — the same
// per-series instant the trailing-window kinds read their window at), and a reading OLDER than it
// is stale: it neither resolves nor refreshes anything, and it is counted in lateSamples. A reading
// at or after it is applied and moves it. So a raised alarm is only cleared by a reading at least
// as new as every reading it has seen, whatever the delivery order and however long the device has
// been quiet. A cursor at the raise is not stored (the raise is its floor), and the cursor is
// dropped when the alarm clears, so it costs one entry per RAISED alarm that has seen a reading
// newer than its raise, live and in the checkpoint, not one per active series.
//
// UNRAISED: THE FRONTIER, FOR A REDELIVERY. A series that is not raised holds nothing. A matching
// reading is refused and counted late only when BOTH hold:
//
//   - it is behind the frontier: more than the lateness budget behind the newest event the engine
//     had seen before the message carrying it (msgFloor: a batch's own envelope does not make its
//     samples late); and
//   - its message may be a platform redelivery (msgRedelivered, Receipt.redelivered): it was stored
//     on the resolved stream more than thresholdRedeliveryBudget after the platform received the
//     reading, or one of those two times is unknown.
//
// So a redelivered match cannot re-raise, and a reading that is merely dated behind the fleet can.
// Neither test is right alone. The receipt alone would refuse every raise in a message the pipeline
// held up, however current its readings. The frontier is engine-wide, so on its own it also refused every reading of a device whose readings are dated more than the budget
// behind the rest of the fleet — a clock that lags, or an in-order store-and-forward upload of an
// outage backlog arriving after other devices had moved event time on — and such a device could
// not raise a threshold alarm at all. What tells those readings from a redelivery is not their
// event time but when they reached the platform: a redelivery re-publishes the reading it was
// given, so it carries the ProcessedTime of its first delivery, while the broker stores it afresh.
// A copy of a resolved event reaches the stream twice only through a redelivery of its source: a
// writer's own retry of a publish that was in fact stored carries the same dedup id and is absorbed
// by the stream's duplicate window. A redelivery waits for the source consumer's AckWait (no
// durable sets a backoff), so a duplicate is stored at least one AckWait after the reading was
// received, while a reading delivered for the first time reaches the stream within the pipeline's
// latency, a fraction of a second when the platform keeps up.
//
// Neither time is engine state: both are fixed in the stored message, so the decision is the same
// in the live run, a replay from any checkpoint, and the replay preview, and nothing is held per
// unraised series. One inside the frontier raises as it always did, so the narrow in-budget stale
// re-raise (non-match@32 clears, then match@28 arrives within the budget and raises, stamped before
// the clear) behaves exactly as before; it heals on the series' next reading. A non-matching
// reading on an unraised series does nothing, as before.
//
// What this does not see, deliberately accepted: a reading that reaches the platform afresh is not
// a redelivery in this sense, whatever its event time — a device that sends an old reading again
// (an MQTT retransmission after a reconnect, a re-uploaded buffer, an ingest adapter's retry, which
// is stamped anew), or one whose readings reach the platform out of order. If it is an older match
// arriving after a newer non-match cleared the alarm, it raises again, stamped before the clear, as
// every reading did before the frontier bound; the series' next reading heals it. Telling that apart from a lagging device's new reading needs the
// series' own newest time, which an unraised series does not keep. And a first delivery the
// pipeline held for longer than thresholdRedeliveryBudget (a backlog in device-management) is
// treated as a possible redelivery: a match in it that is also behind the frontier is refused, as
// every one was before. A raised series answers to its cursor, not to any of this.
//
// Ties: two readings at the cursor's instant are both "newest", so the second to arrive decides,
// as it always did. No order between them is more right than the other.
//
// 🔴 HORIZON BEHIND THE CURSOR. A device whose clock steps BACK — an NTP correction, an RTC reset
// after a power loss, a time-zone fix — presents every later reading as older than its cursor, and
// nothing upstream stops it: event time is clamped only against running AHEAD of receipt, and a
// reading up to a year old is accepted. Refusing those readings until the clock caught up with the
// old newest one would hold the alarm raised for the size of the step. So a reading more than
// thresholdCursorHorizon older than the cursor is not treated as a reorder: it is applied exactly as
// it would be with no cursor — the resolve guard included, which still refuses a non-match older
// than the raise — and restarts the cursor at its own time, or at the raise if that is newer, so the
// cursor is never behind the rising edge (the floor an absent cursor stands for). A step back no longer
// than the horizon is indistinguishable from a reorder and is refused until the clock passes the old
// newest reading, a stall no longer than the step. Ten minutes is ten AckWait (60 s) redeliveries.
// The bound reads only the cursor and the reading, so it is deterministic under replay.
const thresholdCursorHorizon = 10 * time.Minute

// thresholdRedeliveryBudget is how long after the platform received a reading its message may be
// stored on the resolved stream and still be known not to be a redelivery: half the consumer
// AckWait, the least a redelivery waits. The other half is margin for the clocks involved — the
// broker that stores the message, and the ingest path that stamped ProcessedTime, which is the
// capture stream's broker time where there is one and a pod's clock where there is not. It is
// taken from the AckWait rather than restated, so it moves with it. It is deliberately not the
// watermark lateness: that is an event-time tolerance and is zero in the replay preview, which
// would then refuse every match behind its frontier that the live engine raises from.
const thresholdRedeliveryBudget = streams.ConsumerAckWaitSeconds * time.Second / 2

// redelivered reports whether a message with this receipt may be a platform redelivery: it was
// stored more than thresholdRedeliveryBudget after its reading was received, or either time is
// unknown. Unknown answers yes, so a caller that has no receipt keeps the frontier bound whole.
func (r Receipt) redelivered() bool {
	if r.Received.IsZero() || r.Stored.IsZero() {
		return true
	}
	return r.Stored.Sub(r.Received) > thresholdRedeliveryBudget
}

// applyThreshold is the Threshold rule (ADR-057 level): raise on a matching reading, resolve on a
// non-matching one — the metric-scoped feed contract delivers both, so the falling edge is
// observable; a series that goes fully silent stays raised (see the package note on silence —
// Absence covers the went-dark case). Stale readings are refused as described above.
func (e *Engine) applyThreshold(ev Event, r Rule) {
	raisedAt, raised := e.raised[ev.Key]
	if !raised {
		// No cursor outside an alarm. One can be left behind only by ClearRaised (a raise the
		// runtime dropped), and it must not outlive the latch it belonged to.
		delete(e.newest, ev.Key)
		if !ev.Match {
			return
		}
		if e.msgRedelivered && ev.Time.Before(e.msgFloor) {
			e.lateSamples++
			return
		}
		e.emitSample(r, ev) // the cursor starts AT the raise, which is held as an absent one
		return
	}
	n, ok := e.newest[ev.Key]
	if !ok {
		n = raisedAt // a cursor at the raise is not stored: the raise is its floor
	}
	if ev.Time.Before(n) && !ev.Time.Before(n.Add(-thresholdCursorHorizon)) {
		e.lateSamples++
		return
	}
	if !ev.Match {
		e.resolve(r, ev.Key, ev.Time)
		if _, still := e.raised[ev.Key]; !still {
			delete(e.newest, ev.Key)
			return
		}
	}
	// Store the cursor only past the raise. At the raise it is held as absent; BEHIND the raise —
	// only a reading past the horizon, which on a raised series changed nothing — it restarts at
	// the raise, which is the same thing.
	if ev.Time.After(raisedAt) {
		e.newest[ev.Key] = ev.Time
	} else {
		delete(e.newest, ev.Key)
	}
}
