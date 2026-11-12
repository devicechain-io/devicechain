// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

func TestReviewMissingRequiredMetricWithOtherReadings(t *testing.T) {
	for _, missing := range []string{"temp", "occupancy"} {
		t.Run(missing, func(t *testing.T) {
			p := fixture(t)
			def := json.RawMessage(`{"name":"avg","type":"aggregate","windowMode":"sliding","window":"1m","metric":"temp","agg":"avg","op":"gt","threshold":80,"when":{"metric":"occupancy","op":"gt","threshold":0}}`)
			p.Base.Definition, p.Draft.Definition = def, def
			for i := range p.Expectations {
				p.Expectations[i].Expect = ruletest.Expect{NeverRaised: &ruletest.EdgeExpect{Device: "d1"}}
			}
			s := p.Source.(*source)
			for i := range s.msgs {
				ev, err := resolvedevent.UnmarshalResolvedEvent(s.msgs[i].Value)
				if err != nil {
					t.Fatal(err)
				}
				payload := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
				entries := []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: "60"}, {Name: "occupancy", Value: "1"}}
				// Keep both metrics in the lead-in to establish CLEAR. In each
				// window there is telemetry, but the required metric never occurs.
				if i%2 == 1 {
					if missing == "temp" {
						entries = entries[1:]
					} else {
						entries = entries[:1]
					}
				}
				payload.Entries[0].Entries = entries
				s.msgs[i].Value, err = resolvedevent.MarshalResolvedEvent(ev)
				if err != nil {
					t.Fatal(err)
				}
			}
			r := run(t, p)
			for _, w := range r.Windows {
				c := w.Devices[0]
				if len(c.Readings) == 0 || c.Readings[missing] != 0 || c.StateBase != Clear || c.StateDraft != Clear || len(w.Degraded) != 0 {
					t.Fatalf("missing-metric fixture has unexpected coverage: %+v", w)
				}
			}
			for _, e := range r.Expectations {
				if e.Verdict != Inconclusive || e.Baseline != Inconclusive ||
					!strings.Contains(e.Reason, "no reading of "+missing) || !strings.Contains(e.BaselineReason, "no reading of "+missing) {
					t.Fatalf("missing %s must make the expectation inconclusive: %+v", missing, e)
				}
			}
		})
	}
}

func TestReviewActionDroppedDegradesWindow(t *testing.T) {
	p := fixture(t)
	def := json.RawMessage(`{"name":"hot","type":"threshold","severity":"minor","when":{"metric":"temp","op":"gt","threshold":80},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKeyTemplate":"\"hot-\" + series"}}]}`)
	p.Base.Definition, p.Draft.Definition = def, def
	// The template compiles, but its rendered key contains a space. REACT
	// drops the action while retaining the raised detection edge.
	p.Devices = []string{"bad device"}
	s := p.Source.(*source)
	s.msgs[0] = sample(t, 1, "bad device", -time.Minute, "60")
	s.msgs[1] = sample(t, 2, "bad device", time.Minute, "90")
	s.msgs[2] = sample(t, 3, "bad device", 59*time.Minute, "60")
	s.msgs[3] = sample(t, 4, "bad device", 61*time.Minute, "90")
	for i := range p.Expectations {
		p.Expectations[i].Expect = ruletest.Expect{Raised: &ruletest.EdgeExpect{Device: "bad device"}}
	}
	r := run(t, p)
	for _, w := range r.Windows {
		if len(w.BaseEdges) != 1 || len(w.DraftEdges) != 1 || len(w.BaseIntents) != 0 || len(w.DraftIntents) != 0 {
			t.Fatalf("want a detection with its action dropped: %+v", w)
		}
		if !strings.Contains(strings.Join(w.Degraded, "; "), evaluate.DegradedActionDropped) {
			t.Fatalf("missing ACTION_DROPPED degradation: %+v", w)
		}
	}
	for _, e := range r.Expectations {
		if e.Verdict != Inconclusive || e.Baseline != Inconclusive || !strings.Contains(e.Reason, evaluate.DegradedActionDropped) {
			t.Fatalf("a matching edge on a degraded window must be inconclusive: %+v", e)
		}
	}
}

func TestReviewAggregateLeadInIsCapped(t *testing.T) {
	p := fixture(t)
	def := json.RawMessage(`{"name":"avg","type":"aggregate","windowMode":"sliding","window":"8h","metric":"temp","agg":"avg","op":"gt","threshold":80}`)
	p.Base.Definition, p.Draft.Definition = def, def
	r := run(t, p)
	for _, w := range r.Windows {
		want := w.Start.Add(-MaxLeadIn)
		if MaxLeadIn != 6*time.Hour || !w.LeadInStart.Equal(want) {
			t.Fatalf("%s lead-in starts at %s, want %s (exactly 6h before start)", w.Role, w.LeadInStart, want)
		}
	}
}

func TestReviewReadingResetRequiresFalseCondition(t *testing.T) {
	p := fixture(t)
	s, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	if s.fidelity.ResetKind != rules.ResetReading || !s.fidelity.ResetStructured {
		t.Fatalf("want a structured reading reset: %+v", s.fidelity)
	}
	for _, tc := range []struct {
		name   string
		values []float64
		want   State
	}{
		{"only condition-true samples", []float64{90, 95}, Unknown},
		{"one condition-false sample", []float64{90, 60, 95}, Clear},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var evs []evaluate.Event
			for i, value := range tc.values {
				evs = append(evs, event(time.Duration(i-5)*time.Minute, value))
			}
			// Supply no edges directly: feeding true samples through Run would
			// raise an edge, which would mask the reset-evidence branch.
			if got := startState(s, evaluate.Result{}, evs, "d1", epoch.Add(-10*time.Minute), epoch); got != tc.want {
				t.Fatalf("startState = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestReviewPayloadChangeSetsDeviceDivergence(t *testing.T) {
	p := fixture(t)
	p.Draft = envelope(`{"minutes":60}`)
	r := run(t, p)
	for _, w := range r.Windows {
		if len(w.BaseIntents) != 1 || len(w.DraftIntents) != 1 {
			t.Fatalf("want one command on each side: %+v", w)
		}
		b, d := w.BaseIntents[0], w.DraftIntents[0]
		if b.Kind != evaluate.KindSendCommand || d.Kind != evaluate.KindSendCommand ||
			b.Command != "boost" || d.Command != b.Command || !b.At.Equal(d.At) ||
			canonicalJSON(b.Payload) != `{"minutes":15}` || canonicalJSON(d.Payload) != `{"minutes":60}` {
			t.Fatalf("want the same command key and instant with different payloads: base=%+v draft=%+v", b, d)
		}
		want := w.Start.Add(time.Minute)
		if !b.At.Equal(want) {
			t.Fatalf("command instant = %s, want %s", b.At, want)
		}
		c := w.Devices[0]
		if c.Device != "d1" || c.Divergence == nil || !c.Divergence.Equal(want) {
			t.Fatalf("payload change must set device divergence to %s: %+v", want, c)
		}
	}
}
