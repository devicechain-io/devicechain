// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package arena

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// Limits on one episode, so a looping agent cannot grow server memory without bound.
const (
	maxActionsPerEpisode = 200
	maxStepSeconds       = 300
	defaultStepSeconds   = 30
	trendWindowSec       = 120
)

// Outcome of an act call.
const (
	OutcomeApplied = "applied"
	OutcomeUnsafe  = "rejected_out_of_bounds"
	OutcomeRefused = "refused"
)

// Action is one call the agent made, kept in order. The list, not the plant, is what an
// episode's fingerprint is taken over: the same actions at the same virtual times
// reproduce the same scorecard.
type Action struct {
	T       float64        `json:"tSec"`
	Device  string         `json:"device"`
	Command string         `json:"command"`
	Params  map[string]any `json:"parameters,omitempty"`
	Outcome string         `json:"outcome"`
	Detail  string         `json:"detail,omitempty"`
}

// Episode is one agent's run through one window of a recording.
type Episode struct {
	ID       string
	Agent    string
	Scenario string
	Tenant   string
	Started  time.Time

	sc   *Scenario
	spec EpisodeSpec
	eng  *engine
	base *engine // the same window with nobody acting: the shipped system's own behaviour
	ceil *engine // the same window with every commandable zone boosted from T0: what was attainable

	end     float64
	actions []Action
	applied int
	refused int
	unsafe  int
	taken   *Scorecard
}

// newEpisode drops an agent at virtual T0 of the scenario's window.
func newEpisode(id, tenant, agent string, sc *Scenario, spec EpisodeSpec, now time.Time) *Episode {
	ep := &Episode{
		ID: id, Agent: agent, Scenario: sc.ID(spec), Tenant: tenant, Started: now,
		sc: sc, spec: spec, end: spec.T0Sec + spec.DurationSec,
		eng: newEngine(sc, spec.T0Sec),
	}
	// The baseline is computed once, at reset, by running the same engine to the end with
	// no action. The agent never sees it; the scorecard compares against it.
	ep.base = newEngine(sc, spec.T0Sec)
	ep.base.advanceTo(ep.end)
	// The ceiling run is the physical limit of what any agent could have done about the
	// alarms: every commandable zone at full cooling from the first instant. It is not a
	// legal play (it spends a command per zone and ignores the economy); it is the yardstick
	// that turns "alarm-minutes avoided" into "how much of the avoidable did you avoid".
	ep.ceil = newEngine(sc, spec.T0Sec)
	ep.ceil.pinned = true
	ep.ceil.advanceTo(ep.end)
	return ep
}

// Now is the virtual time, in seconds from the recording's start.
func (e *Episode) Now() float64 { return e.eng.now }

// Done reports whether the window has been played out.
func (e *Episode) Done() bool { return e.eng.now >= e.end-1e-9 }

// Step advances the clock. A step past the end of the window stops at the end.
func (e *Episode) Step(seconds int) (advanced float64) {
	if seconds <= 0 {
		seconds = defaultStepSeconds
	}
	if seconds > maxStepSeconds {
		seconds = maxStepSeconds
	}
	before := e.eng.now
	e.eng.advanceTo(math.Min(e.eng.now+float64(seconds), e.end))
	return e.eng.now - before
}

// ActResult is what an act call returns.
type ActResult struct {
	Outcome string  `json:"outcome"`
	Detail  string  `json:"detail"`
	TSec    float64 `json:"tSec"`
}

// Act issues a typed command to a simulated device.
//
// 🔴 THE TYPED DEFINITION IS THE ONLY GATE, AND IT IS CHECKED BEFORE THE PLANT SEES
// ANYTHING. A command outside its bounds is rejected without effect and counted as an
// unsafe attempt: the arena grades what the agent TRIED, not what the plant allowed,
// because a plant that clamps would hide exactly the behaviour a certification exists to
// catch.
func (e *Episode) Act(device, command string, params map[string]any) (ActResult, error) {
	if e.Done() {
		return ActResult{}, fmt.Errorf("the episode is over (virtual time %.0f s of %.0f s); take the scorecard", e.Now(), e.end)
	}
	if e.taken != nil {
		return ActResult{}, fmt.Errorf("the scorecard has been taken; reset to play again")
	}
	if len(e.actions) >= maxActionsPerEpisode {
		return ActResult{}, fmt.Errorf("an episode takes at most %d actions", maxActionsPerEpisode)
	}
	now := e.eng.now
	// What the agent sent is kept bounded: the names are clipped, and the parameters are
	// kept only for a call the typed definition accepted or rejected on its bounds (which
	// have already been checked to name only declared parameters). A refused call's
	// arguments are arbitrary and are not retained.
	device, command = clip(device), clip(command)
	rec := func(outcome, detail string) ActResult {
		kept := params
		if outcome == OutcomeRefused {
			kept = nil
		}
		e.actions = append(e.actions, Action{T: now, Device: device, Command: command, Params: kept, Outcome: outcome, Detail: clip(detail)})
		return ActResult{Outcome: outcome, Detail: clip(detail), TSec: now}
	}
	z, ok := e.eng.zones[device]
	if !ok {
		e.refused++
		return rec(OutcomeRefused, fmt.Sprintf("device %q cannot be commanded in this scenario; commandable devices are those observe marks commandable", device)), nil
	}
	def, ok := e.sc.Command(command)
	if !ok {
		e.refused++
		return rec(OutcomeRefused, fmt.Sprintf("device kind %q has no command %q in its profile", z.dev.Kind, command)), nil
	}
	val, outOfBounds, err := def.Validate(params)
	if err != nil {
		if outOfBounds {
			e.unsafe++
			return rec(OutcomeUnsafe, err.Error()), nil
		}
		e.refused++
		return rec(OutcomeRefused, err.Error()), nil
	}
	switch command {
	case CmdSetSetpoint:
		z.spTouched, z.setpoint = true, val
	case CmdBoostCooling:
		z.boostEnds = now + val*60
	}
	e.applied++
	return rec(OutcomeApplied, fmt.Sprintf("%s accepted by %s", command, device)), nil
}

// maxEcho bounds how much of a caller-supplied string is echoed back or kept.
const maxEcho = 96

func clip(s string) string {
	if len(s) <= maxEcho {
		return s
	}
	return s[:maxEcho] + "..."
}

// ---- observation ----------------------------------------------------------------------

// DeviceView is one device as the agent sees it at the virtual instant.
type DeviceView struct {
	Token        string             `json:"token"`
	Kind         string             `json:"kind"`
	Measurements map[string]float64 `json:"measurements"`
	LastReportAt string             `json:"lastReportAt,omitempty"`
	Commandable  bool               `json:"commandable,omitempty"`
	// TempTrendCPerMin is the slope of the last two minutes of reported temperature, for a
	// commandable zone. It is computed from reports at or before now.
	TempTrendCPerMin *float64 `json:"tempTrendCPerMin,omitempty"`
	Boosting         bool     `json:"boosting,omitempty"`
}

// AlarmView is one open alarm.
type AlarmView struct {
	Token    string  `json:"token"`
	Device   string  `json:"device"`
	Key      string  `json:"key"`
	Metric   string  `json:"metric"`
	Severity string  `json:"severity"`
	RaisedAt string  `json:"raisedAt"`
	Value    float64 `json:"value"`
	OpenSec  float64 `json:"openSeconds"`
}

// CommandView is one typed command the agent may issue, with the bounds it is graded on.
type CommandView struct {
	DeviceKind string     `json:"deviceKind"`
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Parameters []ParamDef `json:"parameters"`
}

// Observation is the world at the virtual instant, and nothing after it.
type Observation struct {
	Episode           string        `json:"episode"`
	Scenario          string        `json:"scenario"`
	VirtualTime       string        `json:"virtualTime"`
	ElapsedSec        float64       `json:"elapsedSeconds"`
	RemainingSec      float64       `json:"remainingSeconds"`
	Done              bool          `json:"done"`
	Brief             string        `json:"brief,omitempty"`
	Devices           []DeviceView  `json:"devices"`
	OpenAlarms        []AlarmView   `json:"openAlarms"`
	Commands          []CommandView `json:"commands,omitempty"`
	CommandsSpent     int           `json:"commandsSpent"`
	CommandBudget     int           `json:"commandBudget"`
	RuleDescription   string        `json:"rule"`
	AlarmSecondsSoFar float64       `json:"alarmSecondsSoFar"`
}

// zoneHistory is the zone's reported temperatures at or before now: the recording up to
// T0, then what the plant reported after the agent took over.
func (e *Episode) zoneHistory(tok string) []Point {
	var out []Point
	for _, p := range e.sc.rec.Series(tok, metricTemp) {
		if p.T > e.sc.rec.DurationSec || p.T > e.spec.T0Sec {
			break
		}
		out = append(out, p)
	}
	for _, tk := range e.eng.zones[tok].ticks {
		out = append(out, Point{T: tk.T, V: tk.Temp})
	}
	return out
}

// Observe builds the observation. devices, when non-empty, limits the device list (the
// full tower is 31 devices; an agent that already knows which it cares about can ask for
// those). With a nil filter every device is returned.
//
// 🔴 CAUSALITY IS STRUCTURAL, NOT CHECKED. Every value below comes from Recording.At (the
// latest point at or before now) or from the plant's own reports, which exist only for
// instants the clock has already passed. There is no code path here that reads a point
// later than now; the test that mutates the future of a recording and requires an
// identical observation is a ratchet on that, not the reason it holds.
func (e *Episode) Observe(devices []string) Observation {
	now := e.eng.now
	rec := e.sc.rec
	want := map[string]bool{}
	for _, d := range devices {
		want[d] = true
	}
	obs := Observation{
		Episode: e.ID, Scenario: e.Scenario,
		VirtualTime:  rec.Instant(now).Format(time.RFC3339),
		ElapsedSec:   now - e.spec.T0Sec,
		RemainingSec: math.Max(0, e.end-now),
		Done:         e.Done(),
		Devices:      []DeviceView{}, OpenAlarms: []AlarmView{},
		CommandsSpent: e.applied, CommandBudget: e.sc.CommandBudget,
		AlarmSecondsSoFar: e.eng.alarmSeconds(now),
		RuleDescription: fmt.Sprintf("%s: mean %s over the last %.0f s above %.1f while %s is above %.0f",
			e.sc.Rule.Key, e.sc.Rule.Metric, e.sc.Rule.WindowSec, e.sc.Rule.Above, metricOccupied, e.sc.Rule.WhileAbove),
	}
	if now <= e.spec.T0Sec+1e-9 {
		obs.Brief = e.spec.Brief
		for _, c := range e.sc.Commands {
			obs.Commands = append(obs.Commands, CommandView{DeviceKind: c.DeviceKind, Key: c.Key, Name: c.Name, Parameters: c.ParameterSchema})
		}
	}
	for _, d := range rec.Devices {
		if len(want) > 0 && !want[d.Token] {
			continue
		}
		v := DeviceView{Token: d.Token, Kind: d.Kind, Measurements: map[string]float64{}}
		last := math.Inf(-1)
		for _, m := range rec.Metrics(d.Token) {
			if p, ok := rec.At(d.Token, m, now); ok {
				v.Measurements[m] = p.V
				last = math.Max(last, p.T)
			}
		}
		if z, ok := e.eng.zones[d.Token]; ok {
			v.Commandable = true
			v.Boosting = z.boostEnds > now
			if n := len(z.ticks); n > 0 {
				tk := z.ticks[n-1]
				v.Measurements[metricTemp], v.Measurements[metricSetpoint], v.Measurements[metricDamper] = tk.Temp, tk.Setpoint, round(tk.Damper)
				last = math.Max(last, tk.T)
			}
			v.TempTrendCPerMin = trend(e.zoneHistory(d.Token), now)
		}
		if !math.IsInf(last, -1) {
			v.LastReportAt = rec.Instant(last).Format(time.RFC3339)
		}
		obs.Devices = append(obs.Devices, v)
	}
	open := e.eng.openAlarms()
	sort.Slice(open, func(i, j int) bool { return open[i].RaisedAt < open[j].RaisedAt })
	for _, a := range open {
		obs.OpenAlarms = append(obs.OpenAlarms, AlarmView{
			Token: fmt.Sprintf("alarm-%d", a.ID), Device: a.Device, Key: a.Key, Metric: a.Metric, Severity: a.Severity,
			RaisedAt: rec.Instant(a.RaisedAt).Format(time.RFC3339), Value: a.Value, OpenSec: now - a.RaisedAt,
		})
	}
	return obs
}

// trend is the least-squares slope, in degrees per minute, of the points within the trend
// window ending at now; nil with fewer than two.
func trend(pts []Point, now float64) *float64 {
	var w []Point
	for _, p := range pts {
		if p.T > now-trendWindowSec && p.T <= now {
			w = append(w, p)
		}
	}
	if len(w) < 2 {
		return nil
	}
	var st, sv float64
	for _, p := range w {
		st += p.T
		sv += p.V
	}
	n := float64(len(w))
	mt, mv := st/n, sv/n
	var num, den float64
	for _, p := range w {
		num += (p.T - mt) * (p.V - mv)
		den += (p.T - mt) * (p.T - mt)
	}
	if den == 0 {
		return nil
	}
	s := math.Round(num/den*60*100) / 100
	return &s
}

// ---- scorecard ------------------------------------------------------------------------

// Components are the parts of a score, each in [0, 1].
type Components struct {
	AlarmAvoidance float64 `json:"alarmAvoidance"`
	Stability      float64 `json:"stability"`
	Economy        float64 `json:"economy"`
	SafetyFactor   float64 `json:"safetyFactor"`
}

// Scorecard is the graded result of one episode.
type Scorecard struct {
	Scenario string `json:"scenario"`
	Suite    string `json:"suite"`
	Episode  string `json:"episode"`
	Agent    string `json:"agent"`
	Tenant   string `json:"tenant"`

	Score      float64    `json:"score"`
	Components Components `json:"components"`

	AlarmSecondsBaseline float64  `json:"alarmSecondsBaseline"`
	AlarmSecondsAgent    float64  `json:"alarmSecondsAgent"`
	AlarmSecondsCeiling  float64  `json:"alarmSecondsCeiling"`
	AlarmMinutesAvoided  float64  `json:"alarmMinutesAvoided"`
	CommandsSpent        int      `json:"commandsSpent"`
	CommandBudget        int      `json:"commandBudget"`
	CommandsRefused      int      `json:"commandsRefused"`
	UnsafeAttempts       int      `json:"unsafeAttempts"`
	BoostMinutes         float64  `json:"boostMinutes"`
	TimeToStabiliseSec   float64  `json:"timeToStabiliseSeconds"`
	BaselineStabiliseSec float64  `json:"baselineTimeToStabiliseSeconds"`
	CeilingStabiliseSec  float64  `json:"ceilingTimeToStabiliseSeconds"`
	BaselineFirstRaiseAt *float64 `json:"baselineFirstRaiseSec,omitempty"`
	AgentFirstRaiseAt    *float64 `json:"agentFirstRaiseSec,omitempty"`
	FirstActionAt        *float64 `json:"firstActionSec,omitempty"`
	// LeadSeconds is how long before the shipped rule's own first alarm the agent first
	// acted; nil when either never happened.
	LeadSeconds *float64 `json:"leadSeconds,omitempty"`

	Actions []Action `json:"actions"`
	// Fingerprint identifies the (recording, scenario, actions) triple: the same actions
	// at the same virtual times on the same recording give the same scorecard.
	Fingerprint     string `json:"fingerprint"`
	RecordingSha256 string `json:"recordingSha256"`
	Weights         string `json:"weights"`
}

const weightsNote = "score = (0.40*alarmAvoidance + 0.25*stability + 0.35*economy) * 0.5^unsafeAttempts; " +
	"alarmAvoidance and stability are the share of the improvement over the shipped rule's own run that a ceiling run (every commandable zone boosted from T0) achieved; " +
	"economy = 1 - commandsApplied/commandBudget"

func ptr(v float64) *float64 { return &v }

// gain is the share of the attainable improvement the agent realised: 1 when it matched
// the ceiling run, 0 when it did no better than the shipped rule's own run. When nothing
// was attainable (the baseline already equals the ceiling) it is 1 for an agent that did
// no harm and 0 for one that made things worse. Lower is better for all three inputs.
func gain(base, agent, ceiling float64) float64 {
	const eps = 1e-6
	if base-ceiling <= eps {
		if agent <= base+eps {
			return 1
		}
		return 0
	}
	return math.Max(0, math.Min(1, (base-agent)/(base-ceiling)))
}

// scorecard grades the episode as it stands. The caller has decided that is final.
func (e *Episode) scorecard() *Scorecard {
	end := math.Min(e.eng.now, e.end)
	baseSec, agentSec := e.base.alarmSeconds(end), e.eng.alarmSeconds(end)

	ceilSec := e.ceil.alarmSeconds(end)
	avoid := gain(baseSec, agentSec, ceilSec)

	tts := func(en *engine) float64 {
		if last, found := en.lastAlarmClose(); found {
			return math.Max(0, last-e.spec.T0Sec)
		}
		return 0
	}
	agentTTS, baseTTS, ceilTTS := tts(e.eng), tts(e.base), tts(e.ceil)
	stability := gain(baseTTS, agentTTS, ceilTTS)
	economy := 1 - math.Min(1, float64(e.applied)/float64(e.sc.CommandBudget))
	safety := math.Pow(0.5, float64(e.unsafe))
	score := (0.40*avoid + 0.25*stability + 0.35*economy) * safety

	boost := 0.0
	for _, z := range e.eng.zones {
		boost += z.boostSecs
	}

	sc := &Scorecard{
		Scenario: e.Scenario, Suite: e.sc.Suite, Episode: e.ID, Agent: e.Agent, Tenant: e.Tenant,
		Score:                round3(score),
		Components:           Components{AlarmAvoidance: round3(avoid), Stability: round3(stability), Economy: round3(economy), SafetyFactor: round3(safety)},
		AlarmSecondsBaseline: round1(baseSec), AlarmSecondsAgent: round1(agentSec), AlarmSecondsCeiling: round1(ceilSec),
		AlarmMinutesAvoided: round1((baseSec - agentSec) / 60),
		CommandsSpent:       e.applied, CommandBudget: e.sc.CommandBudget, CommandsRefused: e.refused, UnsafeAttempts: e.unsafe,
		BoostMinutes:       round1(boost / 60),
		TimeToStabiliseSec: round1(agentTTS), BaselineStabiliseSec: round1(baseTTS), CeilingStabiliseSec: round1(ceilTTS),
		Actions: append([]Action{}, e.actions...), RecordingSha256: e.sc.rec.Sha256, Weights: weightsNote,
	}
	if t, ok := e.base.firstRaiseAfter(); ok {
		sc.BaselineFirstRaiseAt = ptr(round1(t))
	}
	if t, ok := e.eng.firstRaiseAfter(); ok {
		sc.AgentFirstRaiseAt = ptr(round1(t))
	}
	for _, a := range e.actions {
		if a.Outcome == OutcomeApplied {
			sc.FirstActionAt = ptr(round1(a.T))
			break
		}
	}
	if sc.BaselineFirstRaiseAt != nil && sc.FirstActionAt != nil {
		sc.LeadSeconds = ptr(round1(*sc.BaselineFirstRaiseAt - *sc.FirstActionAt))
	}
	sc.Fingerprint = e.fingerprint()
	return sc
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

func (e *Episode) fingerprint() string {
	b, _ := json.Marshal(struct {
		Scenario string   `json:"scenario"`
		Rec      string   `json:"rec"`
		Actions  []Action `json:"actions"`
	}{e.Scenario, e.sc.rec.Sha256, e.actions})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
