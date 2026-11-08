// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

// liveLateness is live DETECT's default watermark lateness (config.DefaultWatermarkLatenessSeconds).
const liveLateness = 5 * time.Second

// differentialScenario is a fleet with faults: reordered and duplicated delivery and an offline
// window delivered late, over random walks that cross the rules' thresholds many times.
const differentialScenario = `
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler
start: 2026-03-01T00:00:00Z
duration: 3h
seed: %d
devices:
  - token: d1
    streams:
      - {metric: temperature, every: 7s, randomWalk: {start: 60, step: 6, min: 0, max: 120}}
  - token: d2
    streams:
      - {metric: temperature, every: 11s, randomWalk: {start: 40, step: 9, min: 0, max: 120}}
  - token: d3
    streams:
      - {metric: temperature, every: 5s, randomWalk: {start: 80, step: 4, min: 0, max: 120}}
faults:
  - reorder: {probability: 0.3, maxDelay: 4s}
  - duplicate: {probability: 0.1}
  - offline: {device: d2, from: 40m, for: 15m}
`

func differentialRules() []Rule {
	acts := func(key string) []rules.Action {
		return []rules.Action{
			{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: key}},
			{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "fan", Payload: `{"on":true}`, ExpiresAfter: "10m"}},
		}
	}
	hot := rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: f64(90)}
	return []Rule{
		{Token: "tumbling", Def: rules.Rule{Name: "tumbling", Type: rules.TypeAggregate, Mode: rules.ModeTumbling, Severity: rules.SeverityMinor,
			Metric: "temperature", Agg: rules.AggAvg, Op: rules.OpGt, Threshold: f64(70), Window: rules.Duration(time.Minute), Actions: acts("tumbling")}},
		{Token: "sliding", Def: rules.Rule{Name: "sliding", Type: rules.TypeAggregate, Mode: rules.ModeSliding, Severity: rules.SeverityMinor,
			Metric: "temperature", Agg: rules.AggMax, Op: rules.OpGt, Threshold: f64(100), Window: rules.Duration(2 * time.Minute), Actions: acts("sliding")}},
		{Token: "session", Def: rules.Rule{Name: "session", Type: rules.TypeAggregate, Mode: rules.ModeSession, Severity: rules.SeverityMinor,
			Metric: "temperature", Agg: rules.AggAvg, Op: rules.OpGt, Threshold: f64(50), Gap: rules.Duration(8 * time.Second), Actions: acts("session")}},
		{Token: "held", Def: rules.Rule{Name: "held", Type: rules.TypeDuration, Severity: rules.SeverityMajor,
			When: hot, Hold: rules.Duration(30 * time.Second), Actions: acts("held")}},
		{Token: "rate", Def: rules.Rule{Name: "rate", Type: rules.TypeDeltaRate, Severity: rules.SeverityMinor,
			Metric: "temperature", Op: rules.OpGt, Threshold: f64(0.8), Rate: true, Actions: acts("rate")}},
		{Token: "count", Def: rules.Rule{Name: "count", Type: rules.TypeRepeating, Severity: rules.SeverityMinor,
			When: hot, Count: 3, Window: rules.Duration(time.Minute), Actions: acts("count")}},
	}
}

// The evaluator agrees with the engine configured the way live runs with several shards
// (ConcurrentSharded, live's default lateness), edge for edge and intent for intent, over every
// time-windowed kind under reordered, duplicated and late (offline) delivery. Correlation is not
// in the list because an evaluation refuses it: its events would need anchor memberships.
func TestEvaluatorAgreesWithTheLiveShardedEngine(t *testing.T) {
	for _, seed := range []int64{1, 7, 42} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			sc, err := ruletest.ParseScenario([]byte(fmt.Sprintf(differentialScenario, seed)))
			if err != nil {
				t.Fatal(err)
			}
			gen, err := sc.Generate(nil)
			if err != nil {
				t.Fatal(err)
			}
			evs := make([]Event, len(gen.Events))
			for i, e := range gen.Events {
				ms := make([]Metric, len(e.Metrics))
				for j, m := range e.Metrics {
					ms[j] = Metric{Name: m.Name, Value: m.Value}
				}
				evs[i] = Event{Device: e.Device, At: e.At, Metrics: ms}
			}
			p := params(differentialRules(), evs)
			p.Lateness = liveLateness
			p.AdvanceTo = gen.AdvanceTo
			p.AppliedGuard = true

			single := mustRun(t, p)

			var sharded *core.ConcurrentSharded
			orig := newDetector
			newDetector = func(cores []core.Rule, lateness time.Duration) core.Detector {
				sharded = core.NewConcurrentSharded(4, cores, lateness)
				return sharded
			}
			live := mustRun(t, p)
			newDetector = orig
			sharded.Close()

			fired := map[string]int{}
			for _, e := range single.Edges {
				fired[e.RuleToken]++
			}
			for _, r := range differentialRules() {
				if fired[r.Token] == 0 {
					t.Fatalf("rule %s never fired; the scenario does not exercise it (fired: %v)", r.Token, fired)
				}
			}
			if single.Stats.EventsDropped == 0 {
				t.Fatal("the duplicate fault dropped nothing; the scenario does not exercise the guard")
			}
			if !reflect.DeepEqual(single.Edges, live.Edges) {
				t.Fatalf("edges differ: evaluator %d, live-configured %d; first difference %s",
					len(single.Edges), len(live.Edges), firstDiff(single.Edges, live.Edges))
			}
			if !reflect.DeepEqual(single.Intents, live.Intents) {
				t.Fatalf("intents differ: evaluator %d, live-configured %d", len(single.Intents), len(live.Intents))
			}
			if single.Stats != live.Stats || !reflect.DeepEqual(single.Degradations, live.Degradations) {
				t.Fatalf("stats differ: %+v / %+v, %+v / %+v", single.Stats, live.Stats, single.Degradations, live.Degradations)
			}
		})
	}
}

func firstDiff(a, b []Edge) string {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return fmt.Sprintf("at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("lengths %d and %d", len(a), len(b))
}

// A correlation rule is refused in a draft: it would never fire on events with no anchors, and a
// quiet rule must not pass a neverRaised.
func TestCorrelationIsRefused(t *testing.T) {
	r := Rule{Token: "fleet", Def: rules.Rule{Name: "fleet", Type: rules.TypeCorrelation, Severity: rules.SeverityMinor,
		AnchorType: "area", Count: 2, Window: rules.Duration(time.Minute)}}
	_, err := Run(t.Context(), params([]Rule{r}, []Event{reading("d1", t0, "temperature", 1)}))
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("err = %v, want the correlation rule refused", err)
	}
}
