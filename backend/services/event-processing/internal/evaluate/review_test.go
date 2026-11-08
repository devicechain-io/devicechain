// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// A run over no events is a refusal: it derives nothing, and every neverRaised would pass on it.
func TestNoEventsIsARefusal(t *testing.T) {
	_, err := Run(context.Background(), params([]Rule{hotRule()}, nil))
	var ie *InputError
	if !errors.As(err, &ie) || !strings.Contains(err.Error(), "no events") {
		t.Fatalf("err = %v, want an InputError naming the missing events", err)
	}
}

func tumblingAvg() Rule {
	return Rule{Token: "avg", Def: rules.Rule{Name: "avg", Type: rules.TypeAggregate, Mode: rules.ModeTumbling, Severity: rules.SeverityMinor,
		Metric: "temperature", Agg: rules.AggAvg, Op: rules.OpGt, Threshold: f64(50), Window: rules.Duration(time.Minute)}}
}

// The reviewer's reproduction of the lateness gap. A 1-minute tumbling avg>50 rule; readings at 0s
// and 62s, then a 500 at 59s delivered after the 62s one. Live holds its watermark 5s behind, so the
// 59s reading is inside the open pane and the pane's average is 255: it detects. An evaluation with
// no lateness closed the pane at 62s and declined the 500, and did not.
func TestLatenessMatchesLive(t *testing.T) {
	evs := []Event{
		reading("d1", t0, "temperature", 10),
		reading("d1", t0.Add(62*time.Second), "temperature", 10),
		reading("d1", t0.Add(59*time.Second), "temperature", 500), // delivered late
	}
	p := params([]Rule{tumblingAvg()}, evs)
	p.Lateness = 5 * time.Second
	p.AdvanceTo = t0.Add(3 * time.Minute)
	res := mustRun(t, p)
	if len(res.Edges) == 0 || !res.Edges[0].Raise || !res.Edges[0].At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("edges = %+v, want the pane [0s, 60s) to raise at its close", res.Edges)
	}
	if res.Stats.LateReadings != 0 || len(res.Degradations) != 0 {
		t.Fatalf("stats=%+v degradations=%+v, want nothing declined inside the lateness budget", res.Stats, res.Degradations)
	}

	// With no lateness the reading is beyond the closed pane: declined, counted, and the result
	// says it is partial rather than reporting a quiet rule.
	p.Lateness = 0
	res = mustRun(t, p)
	if len(res.Edges) != 0 {
		t.Fatalf("edges = %+v with no lateness, want none", res.Edges)
	}
	if res.Stats.LateReadings != 1 || len(res.Degradations) != 1 || res.Degradations[0].Reason != DegradedLateReadings {
		t.Fatalf("stats=%+v degradations=%+v, want the late tumbling-pane reading counted", res.Stats, res.Degradations)
	}
}

// The result names every rule it evaluated, so a client can refuse an expectation about any other.
func TestResultNamesTheEvaluatedRules(t *testing.T) {
	res := mustRun(t, params([]Rule{hotRule(), absenceRule(time.Hour)}, []Event{reading("d1", t0, "temperature", 1)}))
	if strings.Join(res.Evaluated, ",") != "hot,silent" {
		t.Fatalf("evaluated = %v", res.Evaluated)
	}
}

// Edges past the edge limit are not recorded, and the result says so.
func TestEdgeLimitTruncatesLoudly(t *testing.T) {
	var evs []Event
	for i := 0; i < 20; i++ {
		evs = append(evs, reading("d1", t0.Add(time.Duration(2*i)*time.Minute), "temperature", 90),
			reading("d1", t0.Add(time.Duration(2*i+1)*time.Minute), "temperature", 10))
	}
	p := params([]Rule{hotRule(alarmAction())}, evs)
	p.MaxEdges = 7
	res := mustRun(t, p)
	if len(res.Edges) != 7 || len(res.Intents) != 7 {
		t.Fatalf("edges=%d intents=%d, want 7 edges and only their intents", len(res.Edges), len(res.Intents))
	}
	if len(res.Degradations) != 1 || res.Degradations[0] != (Degradation{Reason: DegradedEdgeLimit, Count: 33}) {
		t.Fatalf("degradations = %+v", res.Degradations)
	}
	p.MaxEdges = HardMaxEdges + 1
	var le *LimitError
	if _, err := Run(context.Background(), p); !errors.As(err, &le) {
		t.Fatalf("err = %v, want the edge ceiling enforced", err)
	}
}

// A run takes at most MaxRules rules.
func TestRuleCapIsEnforced(t *testing.T) {
	rs := make([]Rule, MaxRules+1)
	for i := range rs {
		rs[i] = hotRule()
		rs[i].Token = "r" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	var le *LimitError
	if _, err := Run(context.Background(), params(rs, []Event{reading("d1", t0, "temperature", 1)})); !errors.As(err, &le) {
		t.Fatalf("err = %v, want a LimitError for %d rules", err, len(rs))
	}
}

// An action REACT skips fail-closed (here an alarm-key template that renders an invalid key for a
// device token with a space in it) is counted in the result, not lost.
func TestReactDropsAreReported(t *testing.T) {
	r := hotRule(rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKeyTemplate: `"hot-" + series`}})
	res := mustRun(t, params([]Rule{r}, []Event{reading("bad device", t0, "temperature", 90)}))
	if len(res.Edges) != 1 || len(res.Intents) != 0 {
		t.Fatalf("edges=%+v intents=%+v, want the edge and no intent", res.Edges, res.Intents)
	}
	if len(res.Degradations) != 1 || res.Degradations[0] != (Degradation{Reason: DegradedActionDropped, Count: 1}) ||
		!strings.Contains(res.Degraded, "alarm_key_error: 1") {
		t.Fatalf("degradations=%+v degraded=%q, want the dropped action reported", res.Degradations, res.Degraded)
	}
	// A well-formed device renders a key and nothing is dropped.
	if ok := mustRun(t, params([]Rule{r}, []Event{reading("d1", t0, "temperature", 90)})); len(ok.Intents) != 1 || len(ok.Degradations) != 0 {
		t.Fatalf("intents=%+v degradations=%+v", ok.Intents, ok.Degradations)
	}
}

// A rule that reads device attributes is judged without them: the result is degraded, so a
// neverRaised on it cannot pass quietly. A rule whose metric no event carries is warned about.
func TestAttributeRulesDegradeAndMissingMetricsWarn(t *testing.T) {
	attr := Rule{Token: "dyn", Def: rules.Rule{Name: "dyn", Type: rules.TypeThreshold, Severity: rules.SeverityMinor,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, ThresholdAttr: "limit"}}}
	res := mustRun(t, params([]Rule{attr}, []Event{reading("d1", t0, "temperature", 90)}))
	if len(res.Degradations) != 1 || res.Degradations[0] != (Degradation{Reason: DegradedAttributeReferences, Count: 1}) ||
		!strings.Contains(res.Degraded, `"dyn"`) {
		t.Fatalf("degradations=%+v degraded=%q", res.Degradations, res.Degraded)
	}

	res = mustRun(t, params([]Rule{hotRule()}, []Event{reading("d1", t0, "humidity", 90)}))
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], `rule "hot" watches temperature`) {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if res = mustRun(t, params([]Rule{hotRule()}, []Event{reading("d1", t0, "temperature", 9)})); len(res.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none when the metric is supplied", res.Warnings)
	}
}
