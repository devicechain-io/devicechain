// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// NightShiftDevices returns the explicit review device set in emission order.
func NightShiftDevices() []string {
	return []string{"tp-vav-tower-f1-north", NightShiftIncident, NightShiftNormal, "tp-vav-tower-f3-south", NightShiftControl}
}

var nightShiftEpoch = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

type captureRule struct {
	Token      string          `json:"token"`
	Definition json.RawMessage `json:"definition"`
}
type captureHeader struct {
	Kind           string                  `json:"kind"`
	Epoch          time.Time               `json:"epoch"`
	ProfileToken   string                  `json:"profileToken"`
	ProfileVersion string                  `json:"profileVersion"`
	R1             captureRule             `json:"r1"`
	Windows        map[string][2]time.Time `json:"windows"`
	Devices        []string                `json:"devices"`
	Bystander      string                  `json:"bystander"`
	CommandLatency string                  `json:"commandLatency"`
}
type captureEvent struct {
	Kind    string                 `json:"kind"`
	Seq     int                    `json:"seq"`
	Device  string                 `json:"device"`
	At      time.Time              `json:"at"`
	Metrics map[string]json.Number `json:"metrics"`
}
type oracleCommand struct {
	Kind       string          `json:"kind"`
	Device     string          `json:"device"`
	RaisedAt   time.Time       `json:"raisedAt"`
	AcceptedAt time.Time       `json:"acceptedAt"`
	Command    string          `json:"command"`
	Payload    json.RawMessage `json:"payload"`
}
type nightShiftRun struct {
	bytes    []byte
	events   []captureEvent
	commands []oracleCommand
	edges    map[string][]oracleEdge
}
type oracleEdge struct {
	second float64
	raised bool
}

// aggregateOracle is headless: live Tick never calls it. Real runs receive real
// commands; captures/tests feed commands back through the same model.accept path.
type aggregateOracle struct {
	doc      aggregateOracleDoc
	window   float64
	averages map[string]*SlidingAverage
	active   map[string]bool
}
type aggregateOracleDoc struct {
	Type, WindowMode, Agg, Metric, Op, Window string
	Threshold                                 float64
	When                                      struct {
		Metric, Op string
		Threshold  float64
	}
	Actions []struct {
		Type        string
		SendCommand struct{ Command, Payload string }
	}
}

func newR1Oracle() (*aggregateOracle, error) {
	var d aggregateOracleDoc
	if err := json.Unmarshal([]byte(towerpulseZoneHotRule(true).Definition), &d); err != nil {
		return nil, err
	}
	w, err := time.ParseDuration(d.Window)
	if err != nil {
		return nil, err
	}
	if d.Type != "aggregate" || d.WindowMode != "sliding" || d.Agg != "avg" || d.Op != "gt" || d.When.Op != "gt" || w <= 0 || len(d.Actions) != 2 || d.Actions[1].Type != "sendCommand" || d.Actions[1].SendCommand.Command != TowerpulseBoostCommandKey || d.Actions[1].SendCommand.Payload != `{"minutes":15}` {
		return nil, fmt.Errorf("unsupported published R1 oracle definition")
	}
	return &aggregateOracle{doc: d, window: w.Seconds(), averages: map[string]*SlidingAverage{}, active: map[string]bool{}}, nil
}
func (o *aggregateOracle) sample(device string, second float64, metrics map[string]float64) (changed, raised bool) {
	gate, ok := metrics[o.doc.When.Metric]
	value, present := metrics[o.doc.Metric]
	if !ok || !present || gate <= o.doc.When.Threshold {
		return false, false
	}
	a := o.averages[device]
	if a == nil {
		a = &SlidingAverage{}
		o.averages[device] = a
	}
	active := a.Add(second, value, o.window) > o.doc.Threshold
	changed = active != o.active[device]
	o.active[device] = active
	return changed, active
}

// GenerateNightShiftCapture is shared by the generator and drift test.
func GenerateNightShiftCapture() ([]byte, error) {
	r, err := runNightShift(true)
	return r.bytes, err
}
func runNightShift(feedback bool) (nightShiftRun, error) {
	r := nightShiftRun{edges: map[string][]oracleEdge{}}
	oracle, err := newR1Oracle()
	if err != nil {
		return r, err
	}
	m := newTowerpulseModel(buildTowerpulseLayout())
	m.initNightShift()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	at := func(second float64) time.Time { return nightShiftEpoch.Add(time.Duration(second) * time.Second) }
	h := captureHeader{Kind: "header", Epoch: nightShiftEpoch, ProfileToken: TowerpulseVAVProfileToken, ProfileVersion: TowerpulseVAVProfileToken + "@1", R1: captureRule{TowerpulseZoneHotRuleToken, json.RawMessage(towerpulseZoneHotRule(true).Definition)}, Windows: map[string][2]time.Time{"INCIDENT": {at(45 * 60), at(95 * 60)}, "NORMAL": {at(105 * 60), at(160 * 60)}}, Devices: NightShiftDevices(), Bystander: NightShiftBystander, CommandLatency: "2s"}
	if err := enc.Encode(h); err != nil {
		return r, err
	}
	tokens := append(NightShiftDevices(), NightShiftBystander)
	var pending []oracleCommand
	seq := 0
	for second := 0.0; second <= 165*60; second++ {
		m.advanceTo(second)
		for len(pending) > 0 && !pending[0].AcceptedAt.After(at(second)) {
			c := pending[0]
			pending = pending[1:]
			if _, _, ok := m.accept(c.Device, c.Command, c.Payload); !ok {
				return r, fmt.Errorf("oracle command refused on %s", c.Device)
			}
			r.commands = append(r.commands, c)
			if err := enc.Encode(c); err != nil {
				return r, err
			}
		}
		if int64(second)%int64(DefaultEmitInterval/time.Second) != 0 {
			continue
		}
		for _, tok := range tokens {
			metrics := m.reading(tok)
			wire := map[string]json.Number{}
			for k, v := range metrics {
				wire[k] = json.Number(formatFixValue(v))
			}
			seq++
			e := captureEvent{"event", seq, tok, at(second), wire}
			r.events = append(r.events, e)
			if err := enc.Encode(e); err != nil {
				return r, err
			}
			if changed, raised := oracle.sample(tok, second, metrics); changed {
				r.edges[tok] = append(r.edges[tok], oracleEdge{second, raised})
				if raised && feedback {
					pending = append(pending, oracleCommand{"oracle", tok, at(second), at(second + 2), oracle.doc.Actions[1].SendCommand.Command, json.RawMessage(oracle.doc.Actions[1].SendCommand.Payload)})
				}
			}
		}
	}
	r.bytes = buf.Bytes()
	return r, nil
}
