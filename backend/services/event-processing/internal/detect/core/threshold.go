// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "time"

// A Threshold rule's level is, by definition, the level of the series' NEWEST reading: raised while
// the newest reading matches, clear while it does not. It used to be the level of the LAST reading
// to ARRIVE, which is a different thing whenever readings are reordered — and the resolved-events
// stream does reorder them (the resolver pool, a catch-up backlog, two replicas, a redelivery after
// a failed publish). Two cases ended a burst at the wrong level, in both directions:
//
//   - match@1 raises; match@3 is absorbed by the latch; non-match@2 then clears the alarm — it is
//     not older than the raise at 1, so the stale-falling-edge guard in resolve lets it through.
//     The newest reading (3) matched, and the series ended clear.
//   - non-match@32 clears; a late match@28 raises again, stamped before the clear it follows.
//     The newest reading (32) did not match, and the series ended raised.
//
// Threshold also had no lateness bound at all: a reading redelivered a minute after newer ones
// flipped the level exactly the same way, and was counted nowhere.
//
// 🔑 THE CURSOR. The series' newest reading time is kept (Engine.newest — the same per-series
// instant the trailing-window kinds read their window at), and a reading OLDER than it is stale:
// it neither raises nor resolves, and it is counted in lateSamples. A reading at or after it is
// applied exactly as before and moves it. So the level after any delivery order is the newest
// reading's, every edge is stamped at a non-decreasing instant, and readings that arrive in time
// order — ties included — produce byte-identical edges to the arrival-order model, at zero added
// latency.
//
// What it gives up, deliberately: a blip shorter than the reorder span. match@1, non-match@3, then
// a late match@2 reports no raise at 2 — event-time order would have raised at 2 and cleared at 3.
// The alternative, holding every reading until the frontier passes it (as the sample-pairing kinds
// do, reorder.go), reports that blip at the price of a lateness budget of delay on EVERY Threshold
// alarm; for the kind users expect to be immediate that is the wrong trade.
//
// Ties: two readings at the newest instant are both "newest", so the second to arrive decides,
// as it always did. No order between them is more right than the other.
//
// HORIZON. One instant per series would be one entry per (rule, device) for as long as the device
// is known, which for the most-used kind is most of the engine's keyed state. So the cursor is only
// IN FORCE while it is within thresholdCursorHorizon of the frontier, or while the series is
// raised; outside that it behaves as absent — a reading of any age is applied and starts a new
// cursor — and pruneNewest drops it. That equivalence is what makes dropping it at a checkpoint
// outcome-free (the frontier only moves forward, and a series becomes raised only through a
// reading that also moves its cursor), so a replay from any checkpoint is identical to the run that
// wrote it. A raised series always keeps its cursor: however long the device has been quiet, its
// alarm cannot be cleared by a reading older than the newest one seen (by up to the horizon — see
// below for one older still).
//
// The horizon is measured from the frontier, which already trails the newest event time by the
// lateness budget, so every in-budget reorder is inside it whatever the budget. What it bounds is
// the reach BEYOND the budget — the redelivery case: a quiet, non-raised series whose old reading
// arrives more than the horizon after the frontier passed its newest one gets the arrival-order
// behaviour it had before the cursor. Ten minutes is ten AckWait (60 s) redeliveries.
//
// 🔴 THE SAME HORIZON BOUNDS THE CURSOR'S REACH BEHIND ITSELF. A device whose clock steps BACK —
// an NTP correction, an RTC reset after a power loss, a timezone bug — presents every later reading
// as older than its cursor, and nothing upstream stops it: event time is clamped only against
// running AHEAD of receipt, and a reading up to a year old is accepted. Refusing those readings
// until the clock caught up with the old newest one would stall the rule for the size of the step,
// and the frontier horizon does not end that stall: a raised series keeps its cursor however old,
// and on a quiet engine the stepped-back device cannot move the frontier itself. So a reading more
// than the horizon older than the cursor is not treated as a reorder: it is applied exactly as it
// would be with no cursor — as it was before the cursor existed, the resolve guard included — and
// restarts the cursor at its own time (at the raise, for a raised series older readings cannot
// clear), as an expired cursor would. A step back no longer than the
// horizon is indistinguishable from a reorder and is refused until the clock passes the old newest
// reading, a stall no longer than the step. The bound reads only the cursor and the reading, so it
// is deterministic under replay and changes nothing about which cursors are inert. What it gives
// up: a reorder WIDER than the horizon — a device that sends a live reading and only then uploads
// a backlog more than ten minutes old — gets the arrival-order behaviour for the older readings,
// exactly as before the cursor. A device that drains its backlog first (the edge agent does) is in
// time order and unaffected.
const thresholdCursorHorizon = 10 * time.Minute

// applyThreshold is the Threshold rule (ADR-057 level): raise on a matching reading, resolve on a
// non-matching one — the metric-scoped feed contract delivers both, so the falling edge is
// observable; a series that goes fully silent stays raised (see the package note on silence —
// Absence covers the went-dark case). A reading older than the series' newest, by no more than the
// horizon, is stale (see above).
func (e *Engine) applyThreshold(ev Event, r Rule) {
	if n, ok := e.newest[ev.Key]; ok && ev.Time.Before(n) && !ev.Time.Before(n.Add(-thresholdCursorHorizon)) &&
		e.thresholdCursorInForce(ev.Key, n) {
		e.lateSamples++
		return
	}
	// The cursor is never behind the series' rising edge: every edge a restore seeds from is at or
	// before the newest instant (newestFloor), and a snapshot omits a cursor equal to it. Only a
	// reading past the horizon behind the cursor can be older than a raise, and on a raised series
	// it changes nothing — a match is absorbed by the latch, a non-match is older than the raise —
	// so the cursor restarts at the raise instead, as it would be restored.
	cur := ev.Time
	if at, raised := e.raised[ev.Key]; raised && at.After(cur) {
		cur = at
	}
	e.newest[ev.Key] = cur
	if ev.Match {
		e.emitSample(r, ev)
	} else {
		e.resolve(r, ev.Key, ev.Time)
	}
}

// thresholdCursorInForce reports whether a Threshold series' cursor n still refuses older readings:
// the series is raised, or n is inside the horizon of the frontier. It is exactly the negation of
// pruneNewest's test for a Threshold series, which is what makes the prune outcome-free.
func (e *Engine) thresholdCursorInForce(key SeriesKey, n time.Time) bool {
	if _, raised := e.raised[key]; raised {
		return true
	}
	return n.After(e.wm.now.Add(-thresholdCursorHorizon))
}
