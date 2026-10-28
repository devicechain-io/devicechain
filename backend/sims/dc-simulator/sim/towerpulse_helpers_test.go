// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"sort"
	"testing"
	"time"
)

// ---- A rule evaluator that reads the PUBLISHED DOCUMENT --------------------------------------
//
// dc-simulator cannot import event-processing, so the engine that really judges a rule is out of
// reach here (event-processing's own gate holds the documents to its real compiler, and its
// engine was run against R1 as part of this slice). What this module CAN do is decide, from the
// document the scenario publishes and nothing else, whether the model's telemetry would raise an
// alarm: the threshold, the operator, the window, the gate and the silence all come out of the
// JSON, never out of the constants that wrote it. Changing the document therefore changes the
// answer, which is what lets a test be a gate on the rule's wiring and not a restatement of it.

type tpRuleDoc struct {
	Type       string  `json:"type"`
	Severity   string  `json:"severity"`
	Metric     string  `json:"metric"`
	Op         string  `json:"op"`
	Threshold  float64 `json:"threshold"`
	Window     string  `json:"window"`
	WindowMode string  `json:"windowMode"`
	Agg        string  `json:"agg"`
	Timeout    string  `json:"timeout"`
	When       struct {
		Metric    string   `json:"metric"`
		Op        string   `json:"op"`
		Threshold *float64 `json:"threshold"`
	} `json:"when"`
	Actions []struct {
		Type       string `json:"type"`
		RaiseAlarm struct {
			AlarmKey string `json:"alarmKey"`
		} `json:"raiseAlarm"`
		SendCommand struct {
			Command      string `json:"command"`
			Payload      string `json:"payload"`
			ExpiresAfter string `json:"expiresAfter"`
		} `json:"sendCommand"`
	} `json:"actions"`
}

func decodeTPRule(t *testing.T, spec DetectionRuleSpec) tpRuleDoc {
	t.Helper()
	var d tpRuleDoc
	if err := json.Unmarshal([]byte(spec.Definition), &d); err != nil {
		t.Fatalf("rule %s: %v", spec.Token, err)
	}
	return d
}

// tpFiring is one edge of a rule on one device: raised (true) or resolved (false), at a second
// of simulated time.
type tpFiring struct {
	at     float64
	raised bool
}

type tpSample struct {
	t float64
	m map[string]float64
}

func tpCompare(op string, v, bound float64) bool {
	switch op {
	case "gt":
		return v > bound
	case "lt":
		return v < bound
	}
	panic("tpCompare: unsupported op " + op)
}

// tpEval returns the edges `spec` would produce on one device's samples, read up to `until`.
func tpEval(t *testing.T, spec DetectionRuleSpec, samples []tpSample, until float64) []tpFiring {
	t.Helper()
	if !spec.Enabled {
		return nil
	}
	d := decodeTPRule(t, spec)
	var out []tpFiring
	state := false
	set := func(now float64, cond bool) {
		if cond != state {
			state = cond
			out = append(out, tpFiring{at: now, raised: cond})
		}
	}
	gate := func(m map[string]float64) bool {
		if d.When.Metric == "" {
			return true
		}
		v, ok := m[d.When.Metric]
		return ok && d.When.Threshold != nil && tpCompare(d.When.Op, v, *d.When.Threshold)
	}
	switch d.Type {
	case "threshold":
		for _, s := range samples {
			v, ok := s.m[d.When.Metric]
			if !ok {
				continue
			}
			set(s.t, tpCompare(d.When.Op, v, *d.When.Threshold))
		}
	case "aggregate":
		if d.WindowMode != "sliding" || d.Agg != "avg" {
			t.Fatalf("rule %s: evaluator supports a sliding average, got %s/%s", spec.Token, d.WindowMode, d.Agg)
		}
		window, err := time.ParseDuration(d.Window)
		if err != nil {
			t.Fatalf("rule %s: window %q: %v", spec.Token, d.Window, err)
		}
		type point struct{ t, v float64 }
		var win []point
		for _, s := range samples {
			v, ok := s.m[d.Metric]
			if !ok || !gate(s.m) {
				continue
			}
			win = append(win, point{s.t, v})
			for len(win) > 0 && win[0].t <= s.t-window.Seconds() {
				win = win[1:]
			}
			sum := 0.0
			for _, p := range win {
				sum += p.v
			}
			set(s.t, tpCompare(d.Op, sum/float64(len(win)), d.Threshold))
		}
	case "absence":
		timeout, err := time.ParseDuration(d.Timeout)
		if err != nil {
			t.Fatalf("rule %s: timeout %q: %v", spec.Token, d.Timeout, err)
		}
		for i, s := range samples {
			set(s.t, false)
			deadline := s.t + timeout.Seconds()
			next := until + 1
			if i+1 < len(samples) {
				next = samples[i+1].t
			}
			if deadline < next && deadline <= until {
				set(deadline, true)
			}
		}
	default:
		t.Fatalf("rule %s: evaluator does not know type %q", spec.Token, d.Type)
	}
	return out
}

// tpRaisedAt is the second a rule first raised, or -1.
func tpRaisedAt(f []tpFiring) float64 {
	for _, e := range f {
		if e.raised {
			return e.at
		}
	}
	return -1
}

// ---- Running the campus ----------------------------------------------------------------------

// tpRun advances a fresh model through `seconds`, sampling every device each `every` seconds as a
// tick would, and returns each device's samples.
func tpRun(t *testing.T, configure func(*towerpulseModel), seconds, every float64) (map[string][]tpSample, *towerpulseModel) {
	t.Helper()
	model := newTowerpulseModel(buildTowerpulseLayout())
	if configure != nil {
		configure(model)
	}
	series := map[string][]tpSample{}
	tokens := make([]string, 0, len(model.byToken))
	for tok := range model.byToken {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)
	for now := every; now <= seconds; now += every {
		model.advanceTo(now)
		for _, tok := range tokens {
			if r := model.reading(tok); r != nil {
				series[tok] = append(series[tok], tpSample{t: now, m: r})
			}
		}
	}
	return series, model
}

// tpRules returns every towerpulse rule keyed by token, with the profile it is on.
func tpRules(t *testing.T) map[string]struct {
	Spec    DetectionRuleSpec
	Profile string
} {
	t.Helper()
	out := map[string]struct {
		Spec    DetectionRuleSpec
		Profile string
	}{}
	for _, p := range NewTowerpulse(1, Load{}).Manifest().Profiles {
		for _, r := range p.DetectionRules {
			out[r.Token] = struct {
				Spec    DetectionRuleSpec
				Profile string
			}{r, p.Token}
		}
	}
	return out
}

// tpDevicesOfType returns the layout's tokens for one device type.
func tpDevicesOfType(t *testing.T, deviceType string) []string {
	t.Helper()
	m := NewTowerpulse(1, Load{}).Manifest()
	var out []string
	for _, d := range m.Expand(m.Seed) {
		if d.DeviceTypeToken == deviceType {
			out = append(out, d.Token)
		}
	}
	return out
}
