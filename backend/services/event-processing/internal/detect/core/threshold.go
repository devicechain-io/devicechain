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
// wrote it. A raised series always keeps its cursor: its alarm can only be cleared by a reading at
// least as new as the newest one seen, however long the device has been quiet.
//
// The horizon is measured from the frontier, which already trails the newest event time by the
// lateness budget, so every in-budget reorder is inside it whatever the budget. What it bounds is
// the reach BEYOND the budget — the redelivery case: a quiet, non-raised series whose old reading
// arrives more than the horizon after the frontier passed its newest one gets the arrival-order
// behaviour it had before the cursor. Ten minutes is ten AckWait (60 s) redeliveries.
const thresholdCursorHorizon = 10 * time.Minute

// applyThreshold is the Threshold rule (ADR-057 level): raise on a matching reading, resolve on a
// non-matching one — the metric-scoped feed contract delivers both, so the falling edge is
// observable; a series that goes fully silent stays raised (see the package note on silence —
// Absence covers the went-dark case). A reading older than the series' newest is stale (see above).
func (e *Engine) applyThreshold(ev Event, r Rule) {
	if n, ok := e.newest[ev.Key]; ok && ev.Time.Before(n) && e.thresholdCursorInForce(ev.Key, n) {
		e.lateSamples++
		return
	}
	e.newest[ev.Key] = ev.Time
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
