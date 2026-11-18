// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

func TestDivergencePrefixTruthTable(t *testing.T) {
	p := fixture(t)
	s, err := prepare(p, p.Draft, "draft")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		negative bool
		hit      time.Duration
		div      *time.Duration
		want     Verdict
	}{
		{"positive identical hit", false, time.Minute, nil, Pass}, {"positive identical miss", false, -1, nil, Fail},
		{"positive before", false, time.Minute, durationPtr(2 * time.Minute), Pass}, {"positive boundary", false, time.Minute, durationPtr(time.Minute), Pass}, {"positive after", false, 3 * time.Minute, durationPtr(2 * time.Minute), Inconclusive}, {"positive absent after", false, -1, durationPtr(2 * time.Minute), Inconclusive},
		{"negative identical absent", true, -1, nil, Pass}, {"negative identical hit", true, time.Minute, nil, Fail}, {"negative before", true, time.Minute, durationPtr(2 * time.Minute), Fail}, {"negative boundary", true, 2 * time.Minute, durationPtr(2 * time.Minute), Fail}, {"negative after", true, 3 * time.Minute, durationPtr(2 * time.Minute), Inconclusive}, {"negative absent", true, -1, durationPtr(2 * time.Minute), Inconclusive}, {"divergence outside", true, -1, durationPtr(11 * time.Minute), Pass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := WindowResult{Role: Incident, Start: epoch, End: epoch.Add(10 * time.Minute), Devices: []DeviceCoverage{{Device: "d1", Readings: map[string]int{"temp": 1}, StateBase: Clear, StateDraft: Clear}}}
			if tc.div != nil {
				v := epoch.Add(*tc.div)
				w.Devices[0].Divergence = &v
			}
			r := evaluate.Result{Evaluated: []string{"hot"}}
			if tc.hit >= 0 {
				r.Intents = []evaluate.Intent{intent(tc.hit, `{"minutes":15}`)}
				r.Edges = []evaluate.Edge{{At: epoch.Add(tc.hit), Series: "d1", RuleToken: "hot", Raise: true, Value: 92, HasValue: true}}
			}
			e := ruletest.Expect{Intent: &ruletest.IntentExpect{Device: "d1", Command: "boost"}}
			if tc.negative {
				e = ruletest.Expect{NeverIntent: e.Intent}
			}
			v, reason, ev, _ := judge(e, w, s, s, r, true)
			if v != tc.want {
				t.Fatalf("%s: %s", v, reason)
			}
			if v == Fail && tc.negative {
				for _, want := range []string{"d1", "INCIDENT", "boost", `{"minutes":15}`, "92", "identical"} {
					if !strings.Contains(reason, want) {
						t.Fatalf("missing %s: %s", want, reason)
					}
				}
				if len(ev) != 1 {
					t.Fatal(ev)
				}
			}
		})
	}
}
func durationPtr(d time.Duration) *time.Duration { return &d }
func TestOverlayOrderAndEitherSide(t *testing.T) {
	p := fixture(t)
	s, err := prepare(p, p.Draft, "draft")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*WindowResult, *side, *ruletest.Expect)
		want   string
	}{
		{"unsupported", func(w *WindowResult, s *side, e *ruletest.Expect) {
			s.fidelity.Supported = false
			w.Degraded = []string{"degraded"}
		}, "unsupported"},
		{"degraded", func(w *WindowResult, s *side, e *ruletest.Expect) {
			w.Degraded = []string{"degraded"}
			w.Devices[0].LateBase = 1
		}, "degraded"},
		{"late base", func(w *WindowResult, s *side, e *ruletest.Expect) {
			w.Devices[0].LateBase = 1
			w.Devices[0].Readings = nil
		}, "declined as late"},
		{"late draft", func(w *WindowResult, s *side, e *ruletest.Expect) { w.Devices[0].LateDraft = 1 }, "declined as late"},
		{"missing metric", func(w *WindowResult, s *side, e *ruletest.Expect) {
			w.Devices[0].Readings = nil
			w.Devices[0].StateBase = Unknown
		}, "no reading"},
		{"unknown base", func(w *WindowResult, s *side, e *ruletest.Expect) { w.Devices[0].StateBase = Unknown }, "UNKNOWN"},
		{"unknown draft", func(w *WindowResult, s *side, e *ruletest.Expect) { w.Devices[0].StateDraft = Unknown }, "UNKNOWN"},
		{"already raised", func(w *WindowResult, s *side, e *ruletest.Expect) {
			w.Devices[0].StateDraft = Raised
			*e = ruletest.Expect{NeverIntent: e.Intent}
		}, "already raised"},
		{"judge error", func(w *WindowResult, s *side, e *ruletest.Expect) { e.Intent.Rule = "unrun" }, "not evaluated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss := s
			w := WindowResult{Role: Incident, Start: epoch, End: epoch.Add(time.Hour), Devices: []DeviceCoverage{{Device: "d1", Readings: map[string]int{"temp": 1}, StateBase: Clear, StateDraft: Clear}}}
			e := ruletest.Expect{Intent: &ruletest.IntentExpect{Device: "d1", Command: "boost"}}
			tc.mutate(&w, &ss, &e)
			r := evaluate.Result{Evaluated: []string{"hot"}, Intents: []evaluate.Intent{intent(time.Minute, `{}`)}}
			v, reason, _, _ := judge(e, w, s, ss, r, true)
			if v != Inconclusive || !strings.Contains(reason, tc.want) {
				t.Fatalf("%s %s", v, reason)
			}
		})
	}
	// A late decline on an untouched device must not taint a device-specific expectation.
	w := WindowResult{Role: Incident, Start: epoch, End: epoch.Add(time.Hour), Devices: []DeviceCoverage{{Device: "d1", Readings: map[string]int{"temp": 1}, StateBase: Clear, StateDraft: Clear}, {Device: "d2", LateBase: 1}}}
	e := ruletest.Expect{Intent: &ruletest.IntentExpect{Device: "d1", Command: "boost"}}
	v, reason, _, _ := judge(e, w, s, s, evaluate.Result{Evaluated: []string{"hot"}, Intents: []evaluate.Intent{intent(time.Minute, `{}`)}}, true)
	if v != Pass {
		t.Fatal(v, reason)
	}
}
