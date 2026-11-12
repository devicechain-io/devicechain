// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"strings"
	"testing"
	"time"
)

func TestReplayFidelityKinds(t *testing.T) {
	for _, tc := range []struct {
		def       string
		supported bool
		horizon   time.Duration
		reset     ResetKind
	}{
		{`{"type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`, true, 30 * time.Minute, ResetReading},
		{`{"type":"duration","hold":"2m","when":{"metric":"t","op":"lt","threshold":1}}`, true, 2 * time.Minute, ResetReading},
		{`{"type":"aggregate","windowMode":"sliding","window":"5m","metric":"t","agg":"avg","op":"gt","threshold":1}`, true, 5 * time.Minute, ResetAggregate},
		{`{"type":"aggregate","windowMode":"tumbling","window":"5m","metric":"t","agg":"sum","op":"gt","threshold":1}`, true, 5 * time.Minute, ResetAggregate},
		{`{"type":"aggregate","windowMode":"session","gap":"5m","metric":"t","agg":"avg","op":"gt","threshold":1}`, false, 0, ResetAggregate},
		{`{"type":"aggregate","windowMode":"count","count":2,"metric":"t","agg":"avg","op":"gt","threshold":1}`, false, 0, ResetAggregate},
		{`{"type":"absence","timeout":"5m"}`, false, 0, ""},
		{`{"type":"deltaRate","metric":"t","op":"gt","threshold":1}`, false, 0, ""},
		{`{"type":"repeating","count":2,"window":"5m"}`, false, 0, ""},
		{`{"type":"correlation","anchorType":"area","count":2,"window":"5m"}`, false, 0, ""},
		{`{"type":"connectivity"}`, false, 0, ""},
		{`{"type":"threshold","when":{"cel":"geo.inFence(\"yard\")"}}`, false, 30 * time.Minute, ResetReading},
		{`{"type":"threshold","when":{"metric":"t","op":"gt","thresholdAttr":"limit"}}`, false, 30 * time.Minute, ResetReading},
	} {
		t.Run(tc.def, func(t *testing.T) {
			def, err := Decode([]byte(tc.def))
			if err != nil {
				t.Fatal(err)
			}
			def.ID = "test"
			def.Name = "test"
			c, err := Compile(def, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			f := ReplayFidelity(def, c)
			if f.Supported != tc.supported || f.Horizon != tc.horizon || f.ResetKind != tc.reset {
				t.Fatalf("%+v", f)
			}
			if !tc.supported && len(f.Reasons) == 0 {
				t.Fatal("missing reason")
			}
			if tc.supported && !f.ResetStructured {
				t.Fatal("missing structured comparison")
			}
		})
	}
}
func TestReplayFidelityRawGateAndActions(t *testing.T) {
	v := 5.0
	def := Rule{ID: "test", Name: "test", Type: TypeAggregate, Mode: ModeSliding, Window: Duration(time.Minute), Metric: "temp", Agg: AggAvg, Op: OpGt, Threshold: &v, When: Condition{Metric: "gate", Op: OpLe, Threshold: &v}}
	c, err := Compile(def, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f := ReplayFidelity(def, c)
	if !f.ResetStructured || f.Metric != "temp" || f.GateMetric != "gate" || f.GateOp != OpLe || f.GateThreshold != 5 || f.Threshold != 5 || f.Agg != AggAvg {
		t.Fatal(f)
	}
	def.When = Condition{CEL: `"temp" in m`}
	c, err = Compile(def, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if ReplayFidelity(def, c).ResetStructured {
		t.Fatal("raw CEL provided reset")
	}
	for _, a := range []ActionType{ActionRaiseAlarm, ActionSendCommand, ActionHTTPCall, ActionPublish, "unknown"} {
		def.Actions = []Action{{Type: a}}
		f = ReplayFidelity(def, c)
		if f.Supported == (a == "unknown") {
			t.Fatal(f)
		}
		if a == "unknown" && !strings.Contains(strings.Join(f.Reasons, " "), "unknown") {
			t.Fatal(f)
		}
	}
}
