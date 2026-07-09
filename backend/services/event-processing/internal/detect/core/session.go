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
