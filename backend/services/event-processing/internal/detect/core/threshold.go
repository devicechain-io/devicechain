// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "time"

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
// UNRAISED: THE FRONTIER. A series that is not raised holds nothing. A matching reading behind the
// frontier — more than the lateness budget behind the newest event the engine had seen before the
// message carrying it (msgFloor: a batch's own envelope does not make its samples late) — is refused
// and counted late, so a redelivered match cannot re-raise. One inside the budget raises as it always did, so the narrow in-budget
// stale re-raise (non-match@32 clears, then match@28 arrives within the budget and raises, stamped
// before the clear) behaves exactly as before; it heals on the series' next reading. A non-matching
// reading on an unraised series does nothing, as before.
//
// What the frontier bound costs, deliberately accepted: the frontier is engine-wide, so a device
// whose readings are dated more than the lateness budget behind the rest of the fleet — a clock that
// lags, or an in-order store-and-forward upload of readings taken during an outage — cannot RAISE a
// threshold alarm from those readings; only from one inside the budget. It can still clear one (a
// raised series answers to its cursor, not the frontier).
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
		if ev.Time.Before(e.msgFloor) {
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
