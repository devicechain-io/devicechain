// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

func tumblingFix2Rule() json.RawMessage {
	return json.RawMessage(`{"name":"avg","type":"aggregate","windowMode":"tumbling","window":"5m","metric":"temp","agg":"avg","op":"gt","threshold":80,"severity":"minor","actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"hot"}}]}`)
}

func TestTumblingColdStartLatchIsUnknown(t *testing.T) {
	p := fixture(t)
	p.Base.Definition, p.Draft.Definition = tumblingFix2Rule(), tumblingFix2Rule()
	p.Windows[0].Start = epoch.Add(150 * time.Second)
	msgs := []messaging.Message{
		sample(t, 1, "d1", -7*time.Minute, "90"),
		sample(t, 2, "d1", -4*time.Minute, "100"),
		sample(t, 3, "d1", -30*time.Second, "70"),
		sample(t, 4, "d1", time.Minute, "70"),
	}
	for m := 2; m <= 10; m++ {
		msgs = append(msgs, sample(t, uint64(len(msgs)+1), "d1", time.Duration(m)*time.Minute, "100"))
	}
	p.Source = &source{msgs: msgs}
	p.Expectations = []Expectation{{Incident, ruletest.Expect{NeverRaised: &ruletest.EdgeExpect{Device: "d1"}}}}
	r := run(t, p)
	w := r.Windows[0]
	if len(w.LeadInBaseEdges) != 1 || !w.LeadInBaseEdges[0].Raise || !w.LeadInBaseEdges[0].At.Equal(epoch.Add(-5*time.Minute)) {
		t.Fatalf("fixture must cold-start raise at 11:55: %+v", w.LeadInBaseEdges)
	}
	if len(w.BaseEdges) != 0 || len(w.DraftEdges) != 0 {
		t.Fatalf("fixture must stay latched through the window: %+v %+v", w.BaseEdges, w.DraftEdges)
	}
	if w.Devices[0].StateBase != Unknown || w.Devices[0].StateDraft != Unknown || r.Expectations[0].Verdict != Inconclusive {
		t.Fatalf("latched cold start must be UNKNOWN / INCONCLUSIVE: %+v %+v", w.Devices, r.Expectations)
	}
}

func TestTumblingResetRequiresAlignedFullPane(t *testing.T) {
	p := fixture(t)
	p.Base.Definition = tumblingFix2Rule()
	s, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	lo, start := epoch.Add(-450*time.Second), epoch.Add(150*time.Second)
	for _, tc := range []struct {
		name   string
		middle float64
		want   State
	}{{"unaligned false trailing window", 100, Unknown}, {"aligned false full pane", 70, Clear}} {
		t.Run(tc.name, func(t *testing.T) {
			var events []evaluate.Event
			for _, v := range []struct {
				offset time.Duration
				value  float64
			}{
				{-7 * time.Minute, 70}, {-4 * time.Minute, tc.middle}, {-30 * time.Second, 70}, {time.Minute, 70},
			} {
				events = append(events, evaluate.Event{Device: "d1", At: epoch.Add(v.offset), Metrics: []evaluate.Metric{{Name: "temp", Value: v.value}}})
			}
			// No edges: exercise reset evidence independently of the latch guard.
			sliding := s
			sliding.fidelity.WindowMode = rules.ModeSliding
			if got := startState(sliding, evaluate.Result{}, events, "d1", lo, start); got != Clear {
				t.Fatalf("fixture must have false trailing-window evidence: %s", got)
			}
			if got := startState(s, evaluate.Result{}, events, "d1", lo, start); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUnresolvedColdStartRaiseBlocksSlidingReset(t *testing.T) {
	p := fixture(t)
	p.Base.Definition = json.RawMessage(strings.Replace(string(tumblingFix2Rule()), "tumbling", "sliding", 1))
	s, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	lo, start := epoch.Add(-10*time.Minute), epoch
	events := []evaluate.Event{{Device: "d1", At: epoch.Add(-time.Minute), Metrics: []evaluate.Metric{{Name: "temp", Value: 70}}}}
	cold := evaluate.Edge{Series: "d1", At: lo.Add(time.Minute), Raise: true}
	if got := startState(s, evaluate.Result{}, events, "d1", lo, start); got != Clear {
		t.Fatal(got)
	}
	if got := startState(s, evaluate.Result{Edges: []evaluate.Edge{cold}}, events, "d1", lo, start); got != Unknown {
		t.Fatal(got)
	}
	resolved := evaluate.Edge{Series: "d1", At: lo.Add(2 * time.Minute), Raise: false}
	if got := startState(s, evaluate.Result{Edges: []evaluate.Edge{cold, resolved}}, events, "d1", lo, start); got != Clear {
		t.Fatal(got)
	}
	trusted := evaluate.Edge{Series: "d1", At: epoch.Add(-2 * time.Minute), Raise: true}
	if got := startState(s, evaluate.Result{Edges: []evaluate.Edge{cold, trusted}}, events, "d1", lo, start); got != Raised {
		t.Fatal(got)
	}
}

func TestNoRuleToCompareIsInputError(t *testing.T) {
	p := fixture(t)
	p.Base, p.Draft = nil, nil
	p.Source = &source{forbid: t}
	_, err := Run(context.Background(), p)
	input, ok := err.(*InputError)
	if !ok || input.Msg != "there is no rule to compare" || input.RuleRejected {
		t.Fatalf("got %v", err)
	}
}

func TestLateAttributionGap(t *testing.T) {
	for _, tc := range []struct {
		total    int
		byDevice map[string]int
		gap      bool
	}{
		{0, nil, false}, {2, map[string]int{"d1": 1, "d2": 1}, false},
		{1, nil, true}, {2, map[string]int{"d1": 1}, true}, {1, map[string]int{"d1": 2}, true},
	} {
		got := lateAttributionGap(evaluate.Result{Stats: evaluate.Stats{LateReadings: tc.total}, LateByDevice: tc.byDevice})
		if got != tc.gap {
			t.Fatalf("%+v: got %t", tc, got)
		}
	}
}

// This test cannot be parallel: it temporarily wraps the evaluator seam.
func TestLateAttributionGapDegradesWindow(t *testing.T) {
	original := evaluateRun
	t.Cleanup(func() { evaluateRun = original })
	evaluateRun = func(ctx context.Context, p evaluate.Params) (evaluate.Result, error) {
		r, err := original(ctx, p)
		r.Stats.LateReadings++
		return r, err
	}
	r := run(t, fixture(t))
	for _, w := range r.Windows {
		if !strings.Contains(strings.Join(w.Degraded, " "), "late readings could not be attributed to devices;") {
			t.Fatal(w.Degraded)
		}
	}
	if r.Outcome != "INCONCLUSIVE" {
		t.Fatal(r.Outcome)
	}
}

func TestHTTPHeaderOnlyChangeIsContentChanged(t *testing.T) {
	base := side{def: rules.Rule{Actions: []rules.Action{{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://example.com", Headers: map[string]string{"X-Test": "before"}}}}}}
	draft := side{def: rules.Rule{Actions: []rules.Action{{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://example.com", Headers: map[string]string{"X-Test": "after"}}}}}}
	in := evaluate.Intent{Series: "d1", At: epoch, Kind: evaluate.KindHTTPCall, URL: "https://example.com", Method: "POST", Body: "same"}
	w := WindowResult{BaseIntents: []evaluate.Intent{in}, DraftIntents: []evaluate.Intent{in}}
	diff(&w, base, draft, time.Minute)
	if w.Counts.ContentChanged != 1 || len(w.Changes) != 1 || w.Changes[0].Kind != ContentChanged {
		t.Fatalf("%+v", w)
	}
	if len(w.Changes[0].BaseContent.ConfigDigest) != 64 {
		t.Fatal(w.Changes)
	}
}
