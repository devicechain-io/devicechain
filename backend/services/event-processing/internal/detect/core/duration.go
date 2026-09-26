// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "time"

// durState is one Duration series: EITHER an open matched run (running) OR the break that ended
// the last one (a "kept break"), never both. See applyDuration for why each field exists.
type durState struct {
	running   bool
	since     time.Time // running: the run start the hold is measured from
	lastMatch time.Time // running: the newest matching sample in the run (where a restart re-anchors)
	lastBreak time.Time // !running: the newest non-matching sample, kept until the frontier passes lastBreak+Hold
}

// applyDuration places one sample on its Duration series by the sample's OWN time, not by the
// order it arrived in. Per series the engine keeps either an open run (since, lastMatch) or a kept
// break (lastBreak).
//
// Admission. A MATCH with t+Hold <= watermark is refused and counted in lateSamples: it could only
// speak to a hold whose deadline the frontier has already passed, i.e. a decision already made. The
// budget is the hold, not the lateness tolerance, for the same reason Session's is its gap
// (session.go) and Aggregate's is its window (window.go): a strict "behind the frontier is late"
// rule would refuse most of every batched upload (a 60-reading message advances the frontier once,
// from its envelope, before any of its readings is placed), would refuse the BREAKS inside such a
// batch while admitting its tail's matches — a false raise over a real gap — and would refuse
// every reading of a device whose clock runs slower than the fleet by more than the tolerance.
//
// A NON-MATCH that far back is never refused outright, and never counted as a refused sample:
//   - on a RAISED series, at or after the raise, it is the falling edge and is still the current
//     word: it resolves the alarm and drops the run. Refusing it would make the alarm immortal for
//     a device whose readings always arrive that late. No kept break is needed afterwards: a match
//     it could be crossed by is older still, so the admission rule already refuses it.
//   - otherwise it can change nothing, so it is ignored. An open run it falls inside has a
//     deadline at or before t+Hold <= watermark, so its hold timer already fired (advance runs
//     before apply) and the series is raised; a kept break it would extend already refuses, via
//     the admission rule, every match it could refuse.
//
// Inside the budget:
//
//	sample     state                               effect
//	match      open run                            lastMatch = max(lastMatch, t); never extends a run backwards
//	match      kept break, t <= lastBreak          ignored: the break is the newer word
//	match      kept break with t > lastBreak, none open a run since = lastMatch = t, hold timer at t+Hold
//	non-match  raised, t < raisedAt                ignored and counted (see below)
//	non-match  open run, t < since                 ignored: the run began after it
//	non-match  open run, since <= t < lastMatch    RESTART: resolve at t, since = lastMatch, hold timer at lastMatch+Hold
//	non-match  open run, t >= lastMatch            BREAK: resolve at t, kept break at t, expiry timer at t+Hold
//	non-match  kept break                          lastBreak = max(lastBreak, t); the expiry re-arms lazily (fireDuration)
//	non-match  none                                kept break at t, expiry timer at t+Hold
//
// A break wins every tie: a match exactly at lastBreak is ignored, a non-match exactly at
// lastMatch breaks, a non-match exactly at since with lastMatch > since restarts.
//
// Why that is sufficient:
//  1. A run opens only after every break the engine has seen. A kept break refuses matches at or
//     before it, and is released only once watermark >= lastBreak+Hold — after which a match at
//     t <= lastBreak has t+Hold <= watermark and the admission rule refuses it.
//  2. No run is born already elapsed. An admitted opener has t+Hold > watermark; a restart's
//     lastMatch is later than the admitted break t, so lastMatch+Hold > watermark too. A reading
//     from far behind the frontier cannot open a run whose hold has already passed and raise on
//     the next advance.
//  3. Every break admitted BEFORE the hold fires restarts or ends the run. A break inside the
//     held span that is admitted only AFTER the fire (it is newer than watermark-Hold, but the
//     frontier has already passed the deadline) is older than the raise: it cannot withdraw the
//     alarm — the same discipline resolve applies to every level kind's stale falling edge — and
//     it is counted in lateSamples so the contradiction is visible. This includes a break carried
//     in the SAME message as the reading whose envelope moved the frontier past the deadline. So
//     the guarantee is: a break that reaches the engine before the frontier passes the deadline
//     is always honoured; one that arrives later is counted, not applied.
//  4. It is conservative. Restarting at lastMatch (not at the first match after the break, which
//     is not retained) and never extending a run backwards can only delay a raise.
//
// The run is KEPT after the raise (fireDuration) so a later break inside it can restart it.
//
// Known limit (pinned by TestDurationEpisodeShorterThanHoldPlusLatenessIsNotRaised): the hold
// timer fires only when the frontier — the newest time seen less the lateness tolerance — passes
// the deadline, so an in-order break that arrives before then ends the run unraised even though
// the condition held for the hold in event time. An episode must last its hold plus the lateness
// tolerance to be certain to raise.
func (e *Engine) applyDuration(ev Event, r Rule) {
	st, ok := e.runs[ev.Key]
	beyond := !ev.Time.Add(r.Hold).After(e.wm.now)
	if !ev.Match {
		if raisedAt, raised := e.raised[ev.Key]; raised {
			if ev.Time.Before(raisedAt) {
				// Older than the raise: the hold was decided before this break arrived. Count it only
				// when it falls inside the run it contradicts; one that predates the run says nothing.
				if ok && st.running && !ev.Time.Before(st.since) {
					e.lateSamples++
				}
				return
			}
			if beyond {
				e.resolve(r, ev.Key, ev.Time)
				e.dropRun(ev.Key)
				return
			}
		} else if beyond {
			return
		}
	} else if beyond {
		e.lateSamples++ // beyond the budget: it could only speak to a hold the frontier has already decided
		return
	}

	if ev.Match {
		switch {
		case ok && st.running:
			if ev.Time.After(st.lastMatch) {
				st.lastMatch = ev.Time
				e.runs[ev.Key] = st
			}
		case ok && !ev.Time.After(st.lastBreak):
			// At or before a kept break: opening here would span a break already seen.
		default:
			e.runs[ev.Key] = durState{running: true, since: ev.Time, lastMatch: ev.Time}
			e.wheel.schedule(ev.Key, ev.Time.Add(r.Hold)) // replaces a kept break's expiry timer
		}
		return
	}
	switch {
	case ok && st.running && ev.Time.Before(st.since):
		// Predates the run: the run began after it, so it breaks nothing.
	case ok && st.running && ev.Time.Before(st.lastMatch):
		// A known break inside the run, and the newest match shows the condition resumed: restart there.
		st.since = st.lastMatch
		e.runs[ev.Key] = st
		e.resolve(r, ev.Key, ev.Time)
		e.wheel.schedule(ev.Key, st.since.Add(r.Hold))
	case ok && !st.running:
		if ev.Time.After(st.lastBreak) {
			st.lastBreak = ev.Time // the expiry timer re-arms from here when it fires (fireDuration)
			e.runs[ev.Key] = st
		}
	default:
		// No state, or at/after the run's newest match: the condition is off as of ev.Time.
		e.runs[ev.Key] = durState{lastBreak: ev.Time}
		e.wheel.schedule(ev.Key, ev.Time.Add(r.Hold)) // replaces the hold timer
		e.resolve(r, ev.Key, ev.Time)
	}
}

// fireDuration handles a due Duration timer: a hold elapsing on an open run raises (and the run
// is KEPT, so a later break inside it can restart at lastMatch); a kept break's expiry re-arms if
// the break moved on since the timer was armed, else releases the entry. The lazy re-arm costs at
// most one heap push per hold per series, not one per non-matching reading.
func (e *Engine) fireDuration(r Rule, key SeriesKey, deadline time.Time) {
	st, ok := e.runs[key]
	if !ok {
		return // dropped since the timer was armed
	}
	if st.running {
		e.emit(r, key, deadline)
		return
	}
	if expiry := st.lastBreak.Add(r.Hold); expiry.After(e.wm.now) {
		e.wheel.schedule(key, expiry)
		return
	}
	delete(e.runs, key)
}

// dropRun releases a Duration series' run or kept break and its timer.
func (e *Engine) dropRun(key SeriesKey) {
	if _, ok := e.runs[key]; ok {
		delete(e.runs, key)
		e.wheel.cancel(key)
	}
}

// snapRun persists one OPEN run under the checkpoint's "active" key. That key and its
// {rule, series, since} shape predate this record, and both directions of version skew depend on
// them: a checkpoint written before lastMatch existed restores as the open run it was (a missing
// lastMatch reads as since), and a binary from before this change reads every entry here as an
// open run — which is why a kept break is NEVER written here (snapBreak).
type snapRun struct {
	Rule      string    `json:"rule"`
	Series    string    `json:"series"`
	Since     time.Time `json:"since"`
	LastMatch time.Time `json:"lastMatch"`
}

// snapBreak persists one kept break under its own checkpoint key. It is separate from "active"
// so a rollback to a binary that knows nothing of kept breaks ignores them: its own fire raises
// every "active" entry whose timer comes due, so a kept break written there would raise an alarm
// for every device that recently sent a non-matching reading. Ignored, the break's orphaned
// expiry timer fires into a series with no run, which that binary treats as a no-op.
type snapBreak struct {
	Rule   string    `json:"rule"`
	Series string    `json:"series"`
	At     time.Time `json:"at"`
}

func (e *Engine) snapshotRuns() ([]snapRun, []snapBreak) {
	runs := make([]snapRun, 0, len(e.runs))
	breaks := []snapBreak{}
	for k, st := range e.runs {
		if st.running {
			runs = append(runs, snapRun{Rule: k.Rule, Series: k.Series, Since: st.since, LastMatch: st.lastMatch})
		} else {
			breaks = append(breaks, snapBreak{Rule: k.Rule, Series: k.Series, At: st.lastBreak})
		}
	}
	sortByRuleSeries(runs, func(i int) (string, string) { return runs[i].Rule, runs[i].Series })
	sortByRuleSeries(breaks, func(i int) (string, string) { return breaks[i].Rule, breaks[i].Series })
	return runs, breaks
}

// restoreRuns rebuilds the Duration state. It runs after the raised latch is restored, because a
// checkpoint written before the run was kept past the raise holds a raised Duration series with no
// run at all; left that way, the series' next match would open a fresh run and a late break
// between the raise and that match would be taken for one older than the run and ignored, leaving
// the alarm raised across a real break. Such a series is given the run its raise implies: held for
// the hold up to the raise, with the raise as its newest match.
func (e *Engine) restoreRuns(runs []snapRun, breaks []snapBreak) {
	for _, s := range runs {
		lastMatch := s.LastMatch
		if lastMatch.Before(s.Since) {
			lastMatch = s.Since // written before lastMatch existed
		}
		e.runs[SeriesKey{Rule: s.Rule, Series: s.Series}] = durState{running: true, since: s.Since, lastMatch: lastMatch}
	}
	for _, s := range breaks {
		e.runs[SeriesKey{Rule: s.Rule, Series: s.Series}] = durState{lastBreak: s.At}
	}
	for key, raisedAt := range e.raised {
		r, ok := e.rules[key.Rule]
		if !ok || r.Kind != Duration {
			continue
		}
		if _, has := e.runs[key]; !has {
			e.runs[key] = durState{running: true, since: raisedAt.Add(-r.Hold), lastMatch: raisedAt}
		}
	}
}
