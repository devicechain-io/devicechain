// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package arena

import (
	"math"
	"sort"
)

// substep is the plant's fixed step in seconds. A fixed step, not the gap between two
// calls, is what makes an episode a pure function of (scenario, actions): the same
// actions at the same virtual times produce the same trajectory whatever the pacing of the
// agent that issued them.
const substep = 1.0

// tick is one report a zone made: the instant, the temperature and the setpoint and
// damper in force.
type tick struct {
	T, Temp, Setpoint, Damper float64
}

// zone is the plant for one controlled device.
//
// THE MODEL. The recording is the disturbance and the controller is what the agent can
// change. The zone's temperature is the RECORDED trajectory plus an accumulated offset:
//
//	T(t) = recorded(t) + delta(t)
//	d(delta)/dt = -cap_sim*damper_sim/100 + cap_base*damper_base/100
//
// where "base" is the controller as it was when the history was recorded (the recorded
// setpoint, the proportional damper law, no boost) and "sim" is the controller as the
// agent has left it. With no action the two terms are equal, delta stays exactly zero and
// the episode reproduces the recording to the last digit, which is the property that makes
// the baseline the shipped system's own behaviour rather than a model of it.
//
// The damper law, the capacities and the boost's stop-at-setpoint rule are the towerpulse
// simulator's, so a setpoint or a boost moves the room the way it moves the simulator's.
type zone struct {
	dev Device

	delta     float64
	setpoint  float64 // the setpoint in force, when the agent has set one
	spTouched bool
	boostEnds float64 // virtual second the boost stops being allowed, 0 for none
	boostSecs float64 // seconds spent boosting, for the economy score

	ticks []tick // reports after T0

	// rule state
	hist   []Point // temperatures within the rule window
	active *alarm
}

// alarm is one alarm raised by the rule during an episode.
type alarm struct {
	ID        int
	Device    string
	Key       string
	Metric    string
	Severity  string
	RaisedAt  float64
	ClearedAt float64 // negative while open
	Value     float64
}

func (a *alarm) open() bool { return a.ClearedAt < 0 }

// engine advances one plant over a recording. An episode owns one for the agent and builds
// a second, never acted on, to compute the baseline the agent is graded against.
type engine struct {
	sc    *Scenario
	rec   *Recording
	t0    float64
	now   float64
	zones map[string]*zone
	order []string

	alarms   []*alarm
	nextTick map[string]int // index into the recorded temp series of the zone's next report
}

func newEngine(sc *Scenario, t0 float64) *engine {
	e := &engine{sc: sc, rec: sc.rec, t0: t0, now: t0, zones: map[string]*zone{}, nextTick: map[string]int{}}
	for _, d := range sc.zones {
		z := &zone{dev: d}
		e.zones[d.Token] = z
		e.order = append(e.order, d.Token)
	}
	sort.Strings(e.order)
	// The history behind T0 is replayed through the same rule, so the alarms open at T0 are
	// the ones the shipped rule had open at T0.
	for _, tok := range e.order {
		z := e.zones[tok]
		series := e.rec.Series(tok, metricTemp)
		i := 0
		for ; i < len(series) && series[i].T <= t0; i++ {
			e.feed(z, series[i].T, series[i].V)
		}
		e.nextTick[tok] = i
	}
	return e
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func (e *engine) recordedSetpoint(tok string, t float64) float64 {
	v, _ := e.rec.Interp(tok, metricSetpoint, t)
	return v
}

func (e *engine) damper(temp, setpoint float64) float64 {
	p := e.sc.plant
	return clamp(p.ControlBase+p.ControlGain*(temp-setpoint), p.DamperFloor, 100)
}

// advanceTo steps the plant to virtual second target (whole substeps; a target that is not
// a multiple of the step rounds up).
func (e *engine) advanceTo(target float64) {
	for e.now < target-1e-9 {
		e.step()
	}
}

func (e *engine) step() {
	p := e.sc.plant
	t := e.now
	next := t + substep
	for _, tok := range e.order {
		z := e.zones[tok]
		rt, _ := e.rec.Interp(tok, metricTemp, t)
		spRec := e.recordedSetpoint(tok, t)
		temp := rt + z.delta
		spSim := spRec
		if z.spTouched {
			spSim = z.setpoint
		}
		dampBase := e.damper(rt, spRec)
		dampSim, capSim := e.damper(temp, spSim), p.CoolCapCPerMin
		boosting := z.boostEnds > t
		if boosting {
			dampSim, capSim = 100, p.BoostCapCPerMin
			z.boostSecs += substep
		}
		z.delta += (-capSim*dampSim/100 + p.CoolCapCPerMin*dampBase/100) * substep / 60
		if boosting {
			rtNext, _ := e.rec.Interp(tok, metricTemp, next)
			if rtNext+z.delta <= spSim+p.AtSetpointC || next >= z.boostEnds {
				z.boostEnds = 0 // the boost has done its job, or run out of time
			}
		}
		// Every recorded report instant crossed by this step is a report of the zone.
		series := e.rec.Series(tok, metricTemp)
		for i := e.nextTick[tok]; i < len(series) && series[i].T <= next; i = e.nextTick[tok] {
			e.nextTick[tok] = i + 1
			tk := series[i].T
			rtk, _ := e.rec.Interp(tok, metricTemp, tk)
			v := rtk + z.delta
			spk := e.recordedSetpoint(tok, tk)
			if z.spTouched {
				spk = z.setpoint
			}
			z.ticks = append(z.ticks, tick{T: tk, Temp: round(v), Setpoint: spk, Damper: e.damper(v, spk)})
			e.feed(z, tk, round(v))
		}
	}
	e.now = next
}

// round keeps reported values at the recording's own resolution (two decimals), so a
// reading an agent sees is never finer than one a device would have sent.
func round(v float64) float64 { return math.Round(v*100) / 100 }

// feed is the detection rule: the mean of the temperatures reported within the window,
// above the line, while the room is occupied.
func (e *engine) feed(z *zone, t, temp float64) {
	r := e.sc.Rule
	z.hist = append(z.hist, Point{T: t, V: temp})
	cut := 0
	for cut < len(z.hist) && z.hist[cut].T <= t-r.WindowSec {
		cut++
	}
	z.hist = z.hist[cut:]
	sum := 0.0
	for _, p := range z.hist {
		sum += p.V
	}
	mean := sum / float64(len(z.hist))
	occ, ok := e.rec.At(z.dev.Token, metricOccupied, t)
	armed := !ok || occ.V > r.WhileAbove
	hot := armed && mean > r.Above
	switch {
	case hot && z.active == nil:
		a := &alarm{ID: len(e.alarms) + 1, Device: z.dev.Token, Key: r.Key, Metric: r.Metric, Severity: r.Severity, RaisedAt: t, ClearedAt: -1, Value: temp}
		e.alarms = append(e.alarms, a)
		z.active = a
	case !hot && z.active != nil:
		z.active.ClearedAt = t
		z.active = nil
	}
}

// openAlarms are the alarms not yet cleared.
func (e *engine) openAlarms() []*alarm {
	var out []*alarm
	for _, a := range e.alarms {
		if a.open() {
			out = append(out, a)
		}
	}
	return out
}

// alarmSeconds is the time alarms were open inside [t0, upTo].
func (e *engine) alarmSeconds(upTo float64) float64 {
	total := 0.0
	for _, a := range e.alarms {
		from := math.Max(a.RaisedAt, e.t0)
		to := upTo
		if !a.open() {
			to = math.Min(a.ClearedAt, upTo)
		}
		if to > from {
			total += to - from
		}
	}
	return total
}

// lastAlarmClose is when the last alarm that was open at or after T0 closed, or the
// current time if one is still open; ok is false when no alarm was open at any point of
// the episode. It is the episode's time to stabilise: how long until the rule is quiet and
// stays quiet. It is measured on the rule's own alarms, not on the raw temperature, so the
// agent is graded against the same definition of "a problem" the baseline is.
func (e *engine) lastAlarmClose() (float64, bool) {
	last, found := 0.0, false
	for _, a := range e.alarms {
		end := e.now
		if !a.open() {
			end = a.ClearedAt
		}
		if end >= e.t0 && (!found || end > last) {
			last, found = end, true
		}
	}
	return last, found
}

// firstRaiseAfter is the first alarm raise at or after T0.
func (e *engine) firstRaiseAfter() (float64, bool) {
	first, found := 0.0, false
	for _, a := range e.alarms {
		if a.RaisedAt >= e.t0 && (!found || a.RaisedAt < first) {
			first, found = a.RaisedAt, true
		}
	}
	return first, found
}
