// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
)

// The towerpulse THERMAL MODEL, deliberately small.
//
// This is the headless backbone's physics: just enough that the platform sees plausible
// telemetry, a rule can fire for a reason, and a command can take time to do its job. The
// real thermal, CO2 and occupancy model is the Unity showcase's own (a C# port with a day
// timeline, tested separately); nothing here is meant to match it, and nothing here should
// grow toward it. If a number needs to be right to the decimal, it belongs there.
//
// WHAT IS MODELLED, per VAV zone: a temperature that gains heat at a seeded rate and loses
// it through a damper a proportional controller opens as the zone runs above its setpoint;
// a CO2 level fed by the people in the room and flushed by the damper; and a boost that
// opens the damper fully with more cooling capacity behind it. Per building: a chiller whose
// power follows the zones' dampers, air handlers, and a meter that adds them up.
//
// DETERMINISM. The state is a pure function of (device ids, elapsed simulated seconds): it
// advances in fixed one-second sub-steps rather than by the wall-clock gap between two
// ticks, so two runs read the same at the same moment of the run whatever their tick jitter,
// and a test can ask what minute 8 looks like without waiting for it. The seed is the
// device's external id (stableSeed), the same trick sitepulse's device plane plays.
//
// THE SCRIPT. Normal operation crosses no rule. The presenter inputs below cross each one
// exactly once, on a named device, at a fixed time, so an alarm on the board is one the
// script raised and never the model drifting. TestTowerpulseScriptCrossesEachRule... holds
// that in both directions.

// Fixed sub-step of simulated time.
const towerpulseSubstepSeconds = 1.0

// Zone physics. Rates are degrees Celsius per SECOND of simulated time, written per minute
// beside each so the scale is readable.
const (
	// towerpulseCoolCap is the cooling a fully open damper delivers (0.18 C/min).
	towerpulseCoolCap = 0.0030
	// towerpulseBoostCap is the cooling the boost delivers: the damper pinned open with the
	// air handler's supply pushed colder behind it (1.20 C/min). It must exceed the meeting's
	// heat gain below, or a boost could never bring the room back.
	towerpulseBoostCap = 0.0200
	// towerpulseControlBase / Gain: the controller opens the damper to
	// base + gain * (temp - setpoint) percent, between the floor and 100.
	towerpulseControlBase = 20.0
	towerpulseControlGain = 40.0
	towerpulseDamperFloor = 10.0
	// towerpulseAtSetpoint is how close to its setpoint a zone must be to count as having
	// reached it. Zero would never be reached by a controller that approaches it, and a loose
	// value would report SUCCESSFUL on a room that is still warm.
	towerpulseAtSetpoint = 0.15
	// towerpulseDefaultSetpoint is where every zone starts.
	towerpulseDefaultSetpoint = 22.5

	// CO2: the people in a room add it, the damper flushes it toward the outdoor level.
	towerpulseOutdoorCO2     = 420.0
	towerpulseCO2PerPerson   = 0.05  // ppm per second per person
	towerpulseFlushPerDamper = 4e-05 // 1/s per percent of damper opening
)

// The presenter script, in seconds of simulated time from the start of the run.
const (
	// The meeting: a full room and the afternoon sun push one zone warm. It is the story of
	// the board's first beat, and the zone R1 fires on.
	TowerpulseHotZone            = "tp-vav-tower-f4-east"
	towerpulseMeetingStartSecond = 30.0
	towerpulseMeetingEndSecond   = 1080.0
	towerpulseMeetingGain        = 0.0120 // C/s extra heat (0.72 C/min)
	towerpulseMeetingPeople      = 14

	// The empty sunny zone: the same extra heat on a room NOBODY is in. R1 is gated on
	// occupancy, so this zone runs just as hot and raises nothing, which is what proves the
	// gate is doing something.
	TowerpulseSunnyEmptyZone = "tp-vav-tower-f5-north"

	// The stuffy room: a crowded training room the damper cannot flush, so CO2 climbs past R2.
	TowerpulseStuffyZone        = "tp-vav-tower-f2-west"
	towerpulseStuffyStartSecond = 120.0
	towerpulseStuffyPeople      = 24

	// The clogging filter: one air handler's pressure drop climbs through R3's line.
	TowerpulseClogAHU          = "tp-ahu-tower-2"
	towerpulseClogStartSecond  = 120.0
	towerpulseClogEndSecond    = 1200.0
	towerpulseClogFromPa       = 120.0
	towerpulseClogToPa         = 290.0
	towerpulseFilterNominalPa  = 105.0
	towerpulseFilterSpreadPa   = 25.0
	towerpulseFilterNoiseWidth = 4.0

	// The annex chiller goes silent. A restart brings it back after the delay.
	TowerpulseDropChiller          = "tp-chiller-annex"
	towerpulseChillerDropSecond    = 900.0
	towerpulseRestartSeconds       = 45.0
	towerpulseRideThroughRestart   = 20.0
	towerpulseChillerBasePowerKw   = 38.0
	towerpulseChillerPerDamperKw   = 0.55
	towerpulseChillerSupplyNominal = 6.6
)

// towerpulseBuilding says which building a thing belongs to.
type towerpulseBuilding string

const (
	towerpulseTower towerpulseBuilding = "tower"
	towerpulseAnnex towerpulseBuilding = "annex"
)

// towerpulseZone is one VAV controller's room.
type towerpulseZone struct {
	token    string
	building towerpulseBuilding
	seed     uint64

	gain      float64 // seeded baseline heat gain, C/s
	extraGain float64 // scripted extra heat, C/s
	setpoint  float64
	temp      float64
	co2       float64
	damper    float64
	people    int

	// boostEnds is the simulated second the boost stops being allowed, or 0 for no boost.
	boostEnds float64
	// boostSeq numbers the boosts handed to the zone and reachedSeq is the latest one that
	// brought it to its setpoint. The command waits on the latter being ITS number, because
	// the zone drifts off the setpoint again within seconds of the boost ending while the
	// meeting is still on, and a command that read the temperature at some later tick would
	// miss a setpoint the zone really did reach.
	boostSeq, reachedSeq int
}

// towerpulseAHU is one air handler.
type towerpulseAHU struct {
	token    string
	building towerpulseBuilding
	seed     uint64
	mode     int // 0 normal, 1 economy, 2 boost
}

// towerpulseChiller is one chiller.
type towerpulseChiller struct {
	token    string
	building towerpulseBuilding
	seed     uint64

	dropped bool
	// restartAt is the simulated second a restart completes, or 0 when none is under way.
	// A restart of a healthy chiller rides through without interrupting its telemetry.
	restartAt float64
	// scriptDropped says the chiller has been dropped once, by the script or by a presenter,
	// so the script does not knock it over again the moment a restart brings it back.
	scriptDropped      bool
	powerKw, coolingKw float64
}

// towerpulseMeter is one building meter.
type towerpulseMeter struct {
	token    string
	building towerpulseBuilding
	kwh      float64
	kw       float64
}

// towerpulseModel is the whole building campus.
type towerpulseModel struct {
	mu sync.Mutex

	simT float64

	zones    []*towerpulseZone
	ahus     []*towerpulseAHU
	chillers []*towerpulseChiller
	meters   []*towerpulseMeter

	byToken map[string]any

	// dropAt is when the script drops the annex chiller. Negative disables the drop, which
	// is how the negative control for R4 is built.
	dropAt float64
	// script gates the whole presenter script. Off, the campus runs at its baseline and
	// crosses no rule: the negative control for R1 to R3.
	script bool
}

// newTowerpulseModel builds the campus from its layout.
func newTowerpulseModel(layout towerpulseLayout) *towerpulseModel {
	m := &towerpulseModel{byToken: map[string]any{}, dropAt: towerpulseChillerDropSecond, script: true}
	for _, z := range layout.zones {
		seed := stableSeed(z.externalId)
		zone := &towerpulseZone{
			token:    z.token,
			building: z.building,
			seed:     seed,
			gain:     0.0004 + 0.0003*stableUnit(seed, 1),
			setpoint: towerpulseDefaultSetpoint,
			people:   z.basePeople,
		}
		zone.temp = zone.setpoint + 0.1 + 0.25*stableUnit(seed, 2)
		zone.co2 = towerpulseOutdoorCO2 + 160 + 80*stableUnit(seed, 3)
		zone.damper = towerpulseDamperFloor
		m.zones = append(m.zones, zone)
		m.byToken[z.token] = zone
	}
	for _, a := range layout.ahus {
		ahu := &towerpulseAHU{token: a.token, building: a.building, seed: stableSeed(a.externalId)}
		m.ahus = append(m.ahus, ahu)
		m.byToken[a.token] = ahu
	}
	for _, c := range layout.chillers {
		ch := &towerpulseChiller{token: c.token, building: c.building, seed: stableSeed(c.externalId)}
		m.chillers = append(m.chillers, ch)
		m.byToken[c.token] = ch
	}
	for _, me := range layout.meters {
		seed := stableSeed(me.externalId)
		start := 96000.0 + 60000.0*stableUnit(seed, 1)
		if me.building == towerpulseTower {
			start = 412000.0 + 90000.0*stableUnit(seed, 1)
		}
		meter := &towerpulseMeter{token: me.token, building: me.building, kwh: start}
		m.meters = append(m.meters, meter)
		m.byToken[me.token] = meter
	}
	// One settling pass so the first reading is a campus at rest, not a cold start.
	for i := 0; i < 600; i++ {
		m.stepLocked(false)
	}
	m.simT = 0
	for _, me := range m.meters {
		me.kwh = me.kwh - me.kw*600/3600 // the settling pass is not metered
	}
	return m
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// advanceTo steps the campus forward to `elapsed` simulated seconds.
func (m *towerpulseModel) advanceTo(elapsed float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for m.simT+towerpulseSubstepSeconds <= elapsed {
		m.stepLocked(m.script)
	}
}

// stepLocked advances one sub-step. scripted says whether the presenter script applies.
func (m *towerpulseModel) stepLocked(scripted bool) {
	t := m.simT
	if scripted {
		m.applyScript(t)
	}
	dt := towerpulseSubstepSeconds
	damperSumByBuilding := map[towerpulseBuilding]float64{}
	peopleByBuilding := map[towerpulseBuilding]float64{}
	for _, z := range m.zones {
		x := z.temp - z.setpoint
		damper := clamp(towerpulseControlBase+towerpulseControlGain*x, towerpulseDamperFloor, 100)
		capacity := towerpulseCoolCap
		if z.boostEnds > 0 {
			damper = 100
			capacity = towerpulseBoostCap
		}
		z.damper = damper
		z.temp += (z.gain + z.extraGain - capacity*damper/100) * dt
		flush := towerpulseFlushPerDamper * damper
		z.co2 += (float64(z.people)*towerpulseCO2PerPerson - (z.co2-towerpulseOutdoorCO2)*flush) * dt
		damperSumByBuilding[z.building] += damper
		peopleByBuilding[z.building] += float64(z.people)
	}
	for _, c := range m.chillers {
		if c.restartAt > 0 && t+dt >= c.restartAt {
			c.restartAt = 0
			c.dropped = false
		}
		load := damperSumByBuilding[c.building]
		phase := 2 * math.Pi * stableUnit(c.seed, 2)
		cop := 4.7 + 0.35*math.Sin(2*math.Pi*t/1800+phase) - 0.002*load
		c.powerKw = towerpulseChillerBasePowerKw + towerpulseChillerPerDamperKw*load/10
		c.coolingKw = c.powerKw * cop
	}
	for _, me := range m.meters {
		kw := 90.0 + 0.35*peopleByBuilding[me.building]
		if me.building == towerpulseAnnex {
			kw = 28.0 + 0.35*peopleByBuilding[me.building]
		}
		for _, c := range m.chillers {
			if c.building == me.building && !c.dropped {
				kw += c.powerKw
			}
		}
		for _, a := range m.ahus {
			if a.building == me.building {
				kw += 0.12 * m.fanPctLocked(a, damperSumByBuilding[a.building])
			}
		}
		me.kw = kw
		me.kwh += kw * dt / 3600
	}
	m.simT = t + dt
	m.settleBoostsLocked()
}

// fanPctLocked is an air handler's fan speed: it follows how hard its building's dampers work.
func (m *towerpulseModel) fanPctLocked(a *towerpulseAHU, damperSum float64) float64 {
	zones := 0
	for _, z := range m.zones {
		if z.building == a.building {
			zones++
		}
	}
	mean := 0.0
	if zones > 0 {
		mean = damperSum / float64(zones)
	}
	fan := 38 + 0.5*mean + 4*(stableUnit(a.seed, 3)-0.5)
	if a.mode == 2 {
		fan += 18
	}
	if a.mode == 1 {
		fan -= 8
	}
	return clamp(fan, 20, 100)
}

// settleBoostsLocked ends a boost that has done its job or run out of time. Whether the
// zone REACHED its setpoint is decided separately by the command engine from the zone's own
// state; this only stops the cooling.
func (m *towerpulseModel) settleBoostsLocked() {
	for _, z := range m.zones {
		if z.boostEnds == 0 {
			continue
		}
		switch {
		case z.temp <= z.setpoint+towerpulseAtSetpoint:
			z.boostEnds, z.reachedSeq = 0, z.boostSeq
		case m.simT >= z.boostEnds:
			z.boostEnds = 0
		}
	}
}

// applyScript applies the presenter inputs due at simulated second t.
func (m *towerpulseModel) applyScript(t float64) {
	if z, ok := m.byToken[TowerpulseHotZone].(*towerpulseZone); ok {
		switch {
		case t >= towerpulseMeetingStartSecond && t < towerpulseMeetingEndSecond:
			z.extraGain, z.people = towerpulseMeetingGain, towerpulseMeetingPeople
		case t >= towerpulseMeetingEndSecond:
			z.extraGain, z.people = 0, 3
		}
	}
	if z, ok := m.byToken[TowerpulseSunnyEmptyZone].(*towerpulseZone); ok {
		if t >= towerpulseMeetingStartSecond && t < towerpulseMeetingEndSecond {
			z.extraGain = towerpulseMeetingGain
		} else {
			z.extraGain = 0
		}
	}
	if z, ok := m.byToken[TowerpulseStuffyZone].(*towerpulseZone); ok && t >= towerpulseStuffyStartSecond {
		z.people = towerpulseStuffyPeople
	}
	if m.dropAt >= 0 && t >= m.dropAt {
		if c, ok := m.byToken[TowerpulseDropChiller].(*towerpulseChiller); ok && !c.scriptDropped {
			c.dropped, c.scriptDropped = true, true
		}
	}
}

// filterPressure is an air handler's filter drop at simulated second t.
func (m *towerpulseModel) filterPressureLocked(a *towerpulseAHU) float64 {
	base := towerpulseFilterNominalPa + towerpulseFilterSpreadPa*stableUnit(a.seed, 4) +
		towerpulseFilterNoiseWidth*math.Sin(m.simT/97+2*math.Pi*stableUnit(a.seed, 5))
	if a.token != TowerpulseClogAHU || !m.script {
		return base
	}
	switch {
	case m.simT <= towerpulseClogStartSecond:
		return base
	case m.simT >= towerpulseClogEndSecond:
		return towerpulseClogToPa
	}
	frac := (m.simT - towerpulseClogStartSecond) / (towerpulseClogEndSecond - towerpulseClogStartSecond)
	return towerpulseClogFromPa + (towerpulseClogToPa-towerpulseClogFromPa)*frac
}

// reading is what a device reports now, or nil when it reports nothing (a chiller that has
// dropped out is silent, which is what an absence rule fires on). Values are rounded the way
// a controller would round them for the wire.
func (m *towerpulseModel) reading(token string) map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch d := m.byToken[token].(type) {
	case *towerpulseZone:
		return map[string]float64{
			TowerpulseTempKey:      roundEven(d.temp, 2),
			TowerpulseSetpointKey:  roundEven(d.setpoint, 1),
			TowerpulseCO2Key:       roundEven(d.co2, 0),
			TowerpulseOccupancyKey: float64(d.people),
			TowerpulseDamperKey:    roundEven(d.damper, 0),
		}
	case *towerpulseAHU:
		damperSum := 0.0
		for _, z := range m.zones {
			if z.building == d.building {
				damperSum += z.damper
			}
		}
		supply := 13.5 + 0.6*math.Sin(m.simT/240+2*math.Pi*stableUnit(d.seed, 1))
		switch d.mode {
		case 1:
			supply += 1.5
		case 2:
			supply -= 1.0
		}
		return map[string]float64{
			TowerpulseSupplyAirKey: roundEven(supply, 1),
			TowerpulseFanKey:       roundEven(m.fanPctLocked(d, damperSum), 0),
			TowerpulseFilterDPKey:  roundEven(m.filterPressureLocked(d), 0),
			TowerpulseAHUModeKey:   float64(d.mode),
		}
	case *towerpulseChiller:
		if d.dropped {
			return nil
		}
		supply := towerpulseChillerSupplyNominal + 0.2*math.Sin(m.simT/300+2*math.Pi*stableUnit(d.seed, 3))
		return map[string]float64{
			TowerpulseChillerSupplyKey: roundEven(supply, 2),
			TowerpulsePowerKey:         roundEven(d.powerKw, 1),
			TowerpulseCoolingKey:       roundEven(d.coolingKw, 1),
			TowerpulseRunningKey:       1,
		}
	case *towerpulseMeter:
		return map[string]float64{
			TowerpulseMeterKwKey:  roundEven(d.kw, 1),
			TowerpulseMeterKwhKey: roundEven(d.kwh, 1),
		}
	}
	return nil
}

// ---- Commands, with completion semantics -------------------------------------------------

// towerpulseCommand is one command the campus has been handed and not yet settled.
type towerpulseCommand struct {
	token  string // the delivery token
	device string
	key    string
	// deadline is the simulated second after which a command that has not done its job fails.
	deadline float64
	answer   func(success bool)
	// done reports whether the command's job is done, and fail whether it has become
	// impossible. Evaluated against the live model on every settle.
	done func() bool
}

// towerpulseCommands is the device side of the command contract for the whole campus: it
// holds each command until the thing it asked for is true of the model, then answers.
//
// 🔴 THE POINT OF IT, and the reason cmdreceiver's own answer-at-once receiver is not used:
// that receiver reports SUCCESSFUL the instant a frame arrives, which for a command that
// takes minutes to take effect is the false success the whole two-way contract exists to
// prevent. A `boost-cooling` here is SUCCESSFUL only once the zone has come down to its
// setpoint, and FAILED if the boost's own time runs out first.
type towerpulseCommands struct {
	model *towerpulseModel
	// dispatch runs an answer. Production answers from their own goroutine, because the
	// answer is a broker publish that can block; a test runs them inline so it can read the
	// outcome straight after the step that settled it.
	dispatch func(func())

	mu      sync.Mutex
	pending map[string]*towerpulseCommand
	settled map[string]bool // delivery token -> the outcome already given
}

func newTowerpulseCommands(model *towerpulseModel) *towerpulseCommands {
	return &towerpulseCommands{
		model:    model,
		dispatch: func(f func()) { go f() },
		pending:  map[string]*towerpulseCommand{},
		settled:  map[string]bool{},
	}
}

// receive takes one command frame. A redelivery of a command still pending is ignored (the
// answer will come when it settles); a redelivery of one already settled is answered again
// with the same outcome, because the first answer may have been the thing that was lost.
func (c *towerpulseCommands) receive(device, token, key string, payload json.RawMessage, answer func(bool)) {
	c.mu.Lock()
	if _, pending := c.pending[token]; pending {
		c.mu.Unlock()
		return
	}
	if outcome, done := c.settled[token]; done {
		c.mu.Unlock()
		c.dispatch(func() { answer(outcome) })
		return
	}
	c.mu.Unlock()

	cmd, immediate, ok := c.model.accept(device, key, payload)
	if !ok {
		c.record(token, false)
		c.dispatch(func() { answer(false) })
		return
	}
	if immediate {
		c.record(token, true)
		c.dispatch(func() { answer(true) })
		return
	}
	cmd.token, cmd.device, cmd.key, cmd.answer = token, device, key, answer
	c.mu.Lock()
	c.pending[token] = cmd
	c.mu.Unlock()
}

func (c *towerpulseCommands) record(token string, outcome bool) {
	c.mu.Lock()
	c.settled[token] = outcome
	c.mu.Unlock()
}

// settle answers every pending command whose job is done or whose time has run out. Called
// after the model advances.
func (c *towerpulseCommands) settle() {
	c.mu.Lock()
	var finished []*towerpulseCommand
	var outcomes []bool
	// The model's lock is held while the commands read it: a command's test of "done" reads
	// zone state the broker's goroutine may be writing (a setpoint just accepted).
	c.model.mu.Lock()
	simT := c.model.simT
	tokens := make([]string, 0, len(c.pending))
	for t := range c.pending {
		tokens = append(tokens, t)
	}
	sort.Strings(tokens)
	for _, t := range tokens {
		cmd := c.pending[t]
		switch {
		case cmd.done():
			finished, outcomes = append(finished, cmd), append(outcomes, true)
		case simT >= cmd.deadline:
			finished, outcomes = append(finished, cmd), append(outcomes, false)
		}
	}
	c.model.mu.Unlock()
	for i, cmd := range finished {
		delete(c.pending, cmd.token)
		c.settled[cmd.token] = outcomes[i]
	}
	c.mu.Unlock()
	for i, cmd := range finished {
		answer, outcome := cmd.answer, outcomes[i]
		c.dispatch(func() { answer(outcome) })
	}
}

// pendingCount is how many commands are waiting on the model.
func (c *towerpulseCommands) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// accept applies a command to the model. immediate means it took effect at once and is
// SUCCESSFUL now; otherwise the returned command carries what to wait for. ok false is a
// command the device refuses (unknown key, a payload it cannot read, the wrong device kind).
func (m *towerpulseModel) accept(device, key string, payload json.RawMessage) (cmd *towerpulseCommand, immediate, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch d := m.byToken[device].(type) {
	case *towerpulseZone:
		switch key {
		case TowerpulseBoostCommandKey:
			var p struct {
				Minutes float64 `json:"minutes"`
			}
			if json.Unmarshal(payload, &p) != nil || p.Minutes <= 0 {
				return nil, false, false
			}
			ends := m.simT + p.Minutes*60
			d.boostEnds = ends
			d.boostSeq++
			seq := d.boostSeq
			return &towerpulseCommand{
				deadline: ends,
				// The job is the zone coming down to its setpoint, latched by the step that saw
				// it happen. Not the boost flag, which clears when the boost stops for ANY
				// reason, running out of time included, and must not be read as success.
				done: func() bool { return d.reachedSeq == seq },
			}, false, true
		case TowerpulseSetpointCommandKey:
			var p struct {
				Celsius float64 `json:"celsius"`
			}
			if json.Unmarshal(payload, &p) != nil || p.Celsius < 10 || p.Celsius > 35 {
				return nil, false, false
			}
			d.setpoint = p.Celsius
			return nil, true, true
		}
	case *towerpulseAHU:
		if key == TowerpulseModeCommandKey {
			var p struct {
				Mode string `json:"mode"`
			}
			if json.Unmarshal(payload, &p) != nil {
				return nil, false, false
			}
			mode, known := map[string]int{"normal": 0, "economy": 1, "boost": 2}[p.Mode]
			if !known {
				return nil, false, false
			}
			d.mode = mode
			return nil, true, true
		}
	case *towerpulseChiller:
		if key == TowerpulseRestartCommandKey {
			delay := towerpulseRideThroughRestart
			if d.dropped {
				delay = towerpulseRestartSeconds
			}
			d.restartAt = m.simT + delay
			target := d
			return &towerpulseCommand{
				deadline: m.simT + 3*delay,
				// Back and reporting: the restart has completed and the chiller is not dropped.
				done: func() bool { return target.restartAt == 0 && !target.dropped },
			}, false, true
		}
	}
	return nil, false, false
}

// DropChiller silences a chiller now, as the script does at its appointed second. It is how a
// presenter (or a test) triggers the dropout out of turn.
func (m *towerpulseModel) DropChiller(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byToken[token].(*towerpulseChiller)
	if !ok {
		return fmt.Errorf("towerpulse: %q is not a chiller", token)
	}
	c.dropped, c.scriptDropped = true, true
	c.restartAt = 0
	return nil
}
