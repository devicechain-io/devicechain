// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

// applySession folds a matching event into its series' open session and (re)arms the gap
// timer. A session is the run of matching events whose successive gaps stay under Gap; each
// event pushes the close deadline to eventTime+Gap, but only ever FORWARD (scheduleForward) —
// so a bounded-late out-of-order event can't pull the close in and split one session in two,
// and the live deadline stays at (latest event time)+Gap, which is where fire() stamps the
// close. Reset-per-event shares the wheel with Absence/Duration, keyed on the rule id so keys
// never collide. A crash mid-session re-derives the identical close: both the accumulator and
// the timer are in the snapshot.
//
// KNOWN RESIDUAL (ADR-057 review D2/D5, inherent — not the sliding-kind gap): a session is DEFINED by
// matching events (a non-match opens none and arms no gap timer). An OPEN session always closes via
// its gap timer even under non-matching traffic (the watermark still advances), so an in-flight breach
// is always evaluated; but once a session has closed satisfied and raised, only a NEW session that
// closes unsatisfied resolves it — and a device reporting only non-matching values opens no new
// session, so the raised alarm persists. There is no time-based expiry to bridge the gap without
// changing the kind's nature. Operators pair such a rule with an Absence rule when "stopped producing
// matching sessions" must also clear the alarm.
func (e *Engine) applySession(ev Event, r Rule) {
	if !ev.Match {
		return
	}
	if !ev.Time.Add(r.Gap).After(e.wm.now) {
		return // beyond lateness: its session has already closed — don't fold it into the next one
	}
	pa := e.session[ev.Key]
	if pa == nil {
		pa = &paneAgg{}
		e.session[ev.Key] = pa
	}
	pa.add(ev.Value)
	e.wheel.scheduleForward(ev.Key, ev.Time.Add(r.Gap))
}

// --- snapshot / restore ---

type snapSession struct {
	Rule   string  `json:"rule"`
	Series string  `json:"series"`
	Count  int     `json:"count"`
	Sum    float64 `json:"sum"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

func (e *Engine) snapshotSessions() []snapSession {
	out := make([]snapSession, 0, len(e.session))
	for k, pa := range e.session {
		out = append(out, snapSession{Rule: k.Rule, Series: k.Series, Count: pa.count, Sum: pa.sum, Min: pa.min, Max: pa.max})
	}
	sortByRuleSeries(out, func(i int) (string, string) { return out[i].Rule, out[i].Series })
	return out
}

func (e *Engine) restoreSessions(in []snapSession) {
	for _, s := range in {
		e.session[SeriesKey{Rule: s.Rule, Series: s.Series}] = &paneAgg{count: s.Count, sum: s.Sum, min: s.Min, max: s.Max}
	}
}
