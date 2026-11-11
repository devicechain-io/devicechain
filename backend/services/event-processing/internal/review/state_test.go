// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

func event(at time.Duration, value float64) evaluate.Event {
	return evaluate.Event{Device: "d1", At: epoch.Add(at), Metrics: []evaluate.Metric{{Name: "temp", Value: value}}}
}
func TestStateResetEvidence(t *testing.T) {
	p := fixture(t)
	s, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	lo, start := epoch.Add(-10*time.Minute), epoch
	for _, tc := range []struct {
		name       string
		evs        []evaluate.Event
		edges      []evaluate.Edge
		structured bool
		want       State
	}{
		{"no evidence", nil, nil, true, Unknown}, {"false reset", []evaluate.Event{event(-time.Minute, 60)}, nil, true, Clear}, {"true no reset", []evaluate.Event{event(-time.Minute, 90)}, nil, true, Unknown}, {"window reading not reset", []evaluate.Event{event(0, 60)}, nil, true, Unknown}, {"before lead not reset", []evaluate.Event{event(-11*time.Minute, 60)}, nil, true, Unknown}, {"raw CEL no evidence", []evaluate.Event{event(-time.Minute, 60)}, nil, false, Unknown},
		{"raise edge", nil, []evaluate.Edge{{Series: "d1", At: epoch.Add(-time.Minute), Raise: true}}, false, Raised},
		{"resolve proves clear", nil, []evaluate.Edge{{Series: "d1", At: epoch.Add(-time.Minute)}}, false, Clear},
		{"other device", nil, []evaluate.Edge{{Series: "d2", At: epoch.Add(-time.Minute)}}, true, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ss := s
			ss.fidelity.ResetStructured = tc.structured
			if got := startState(ss, evaluate.Result{Edges: tc.edges}, tc.evs, "d1", lo, start); got != tc.want {
				t.Fatalf("%s != %s", got, tc.want)
			}
		})
	}
	s.env = nil
	if got := startState(s, evaluate.Result{}, nil, "d1", lo, start); got != Clear {
		t.Fatal(got)
	}
}
func TestAggregateResetFullHorizonAndGate(t *testing.T) {
	for _, mode := range []string{"sliding", "tumbling"} {
		for _, agg := range []string{"avg", "sum", "min", "max", "count"} {
			t.Run(mode+agg, func(t *testing.T) {
				p := fixture(t)
				metric := `,"metric":"temp"`
				if agg == "count" {
					metric = ""
				}
				p.Base.Definition = json.RawMessage(`{"name":"a","type":"aggregate","windowMode":"` + mode + `","window":"5m","agg":"` + agg + `","op":"gt","threshold":80` + metric + `,"when":{"metric":"gate","op":"gt","threshold":0}}`)
				s, err := prepare(p, p.Base, "base")
				if err != nil {
					t.Fatal(err)
				}
				lo, start := epoch.Add(-10*time.Minute), epoch
				reading := event(-4*time.Minute, 1)
				reading.Metrics = append(reading.Metrics, evaluate.Metric{Name: "gate", Value: 1})
				if got := startState(s, evaluate.Result{}, []evaluate.Event{reading}, "d1", lo, start); got != Clear {
					t.Fatal(got)
				}
				reading.At = epoch.Add(-8 * time.Minute)
				if got := startState(s, evaluate.Result{}, []evaluate.Event{reading}, "d1", lo, start); got != Unknown {
					t.Fatal("partial horizon proved clear", got)
				}
				reading.At = epoch.Add(-4 * time.Minute)
				reading.Metrics[1].Value = 0
				if got := startState(s, evaluate.Result{}, []evaluate.Event{reading}, "d1", lo, start); got != Unknown {
					t.Fatal("false gate proved reset", got)
				}
			})
		}
	}
}
func TestHistoricalAndActualCommandDivergence(t *testing.T) {
	p := fixture(t)
	b, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	d := b
	c := DeviceCoverage{Device: "d1", Versions: map[string]int{"p@1": 1}}
	lo := epoch.Add(-time.Hour)
	for _, kind := range []string{"nil", "error", "missing", "absent", "disabled", "different", "same"} {
		t.Run(kind, func(t *testing.T) {
			pp := p
			pp.VersionRules = func(context.Context, string) (*RuleEnvelope, bool, error) {
				switch kind {
				case "error":
					return nil, false, errors.New("unavailable")
				case "missing":
					return nil, false, nil
				case "absent":
					return nil, true, nil
				case "disabled":
					env := *p.Base
					env.Enabled = false
					return &env, true, nil
				case "different":
					return envelope(`{"minutes":60}`), true, nil
				}
				return p.Base, true, nil
			}
			if kind == "nil" {
				pp.VersionRules = nil
			}
			got := divergence(context.Background(), pp, b, d, evaluate.Result{}, evaluate.Result{}, c, lo)
			if kind == "same" {
				if got != nil {
					t.Fatal(got)
				}
			} else if got == nil || !got.Equal(lo) {
				t.Fatal(got)
			}
		})
	}
	br := evaluate.Result{Intents: []evaluate.Intent{intent(-time.Minute, `{"a":1,"b":2}`)}}
	dr := evaluate.Result{Intents: []evaluate.Intent{intent(-time.Minute, `{ "b":2,"a":1 }`)}}
	if got := divergence(context.Background(), p, b, d, br, dr, c, lo); got != nil {
		t.Fatal(got)
	}
	dr.Intents = append(dr.Intents, intent(0, `{}`))
	got := divergence(context.Background(), p, b, d, br, dr, c, lo)
	if got == nil || !got.Equal(epoch) {
		t.Fatal(got)
	}
	// Every supported comparison operator must judge the literal comparison, including zero.
	for _, op := range []rules.CompareOp{rules.OpGt, rules.OpGe, rules.OpLt, rules.OpLe, rules.OpEq, rules.OpNe} {
		if compare(0, op, 0) != (op == rules.OpGe || op == rules.OpLe || op == rules.OpEq) {
			t.Fatal(op)
		}
	}
}
