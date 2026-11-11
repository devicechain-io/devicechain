// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

func TestReplayEarlierVersionBatchAndCoverage(t *testing.T) {
	p := fixture(t)
	s := p.Source.(*source)
	ev, err := resolvedevent.UnmarshalResolvedEvent(s.msgs[1].Value)
	if err != nil {
		t.Fatal(err)
	}
	payload := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	payload.Entries = append(payload.Entries, resolvedevent.ResolvedMeasurementsEntry{OccurredTime: epoch.Add(2 * time.Minute), Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: "60"}, {Name: "text", Value: "not numeric"}}})
	ev.OccurredTime = epoch.Add(25 * time.Minute)
	s.msgs[1].Value, err = resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	r := run(t, p)
	w := r.Windows[0]
	if len(w.BaseIntents) != 1 || w.Devices[0].Readings["temp"] != 2 || w.Devices[0].Readings["text"] != 0 || w.Devices[0].LeadInReadings["temp"] != 1 || w.Devices[0].Versions["p@1"] != 3 {
		t.Fatalf("%+v", w)
	}
	old := w.InputDigest
	payload.Entries[0].Entries[0].Value = "95"
	s.msgs[1].Value, err = resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	if run(t, p).Windows[0].InputDigest == old {
		t.Fatal("digest ignored values")
	}
}
func TestRecordedBoundsExcludeNewTailAndReportEviction(t *testing.T) {
	p := fixture(t)
	r := run(t, p)
	for _, w := range r.Windows {
		p.StreamBounds = append(p.StreamBounds, w.Bounds)
	}
	s := p.Source.(*source)
	s.msgs = append(s.msgs, sample(t, 99, "d1", 3*time.Minute, "60"))
	again := run(t, p)
	if again.InputDigest != r.InputDigest || len(again.Windows[0].BaseEdges) != len(r.Windows[0].BaseEdges) {
		t.Fatal("rerun admitted new tail")
	}
	s.msgs = s.msgs[1:]
	again = run(t, p)
	if len(again.Windows[0].Degraded) == 0 {
		t.Fatal("missing recorded sequence eviction")
	}
}
func TestLatenessPerDevice(t *testing.T) {
	for _, lateAt := range []time.Duration{4*time.Minute + 30*time.Second, 0} {
		p := fixture(t)
		def := json.RawMessage(`{"name":"avg","type":"aggregate","windowMode":"sliding","window":"1m","metric":"temp","agg":"avg","op":"gt","threshold":80}`)
		p.Base.Definition = def
		p.Draft.Definition = def
		p.Lateness = time.Minute
		p.Source = &source{msgs: []messaging.Message{sample(t, 1, "d1", -4*time.Minute, "60"), sample(t, 2, "d1", 5*time.Minute, "90"), sample(t, 3, "d1", lateAt, "60")}}
		r := run(t, p)
		c := r.Windows[0].Devices[0]
		if lateAt > 0 {
			if c.LateBase != 0 || c.LateDraft != 0 {
				t.Fatalf("inside lateness declined: %+v", c)
			}
			folded := false
			for _, e := range r.Windows[0].DraftEdges {
				if !e.Raise && e.At.Equal(epoch.Add(5*time.Minute)) {
					folded = true
				}
			}
			if !folded {
				t.Fatal("in-budget reordered sample did not resolve the aggregate")
			}

		} else if c.LateBase == 0 || c.LateDraft == 0 || r.Expectations[0].Verdict != Inconclusive {
			t.Fatalf("outside lateness must be inconclusive: %+v", r)
		}
	}
}
func TestGeofenceConstantAndBadDefinitionNoRead(t *testing.T) {
	for _, kind := range []string{"geo", "constant", "bad base", "bad draft"} {
		p := fixture(t)
		p.Source = &source{forbid: t}
		switch kind {
		case "geo":
			p.Draft.Definition = json.RawMessage(`{"name":"geo","type":"threshold","when":{"cel":"geo.inFence(\"yard\")"}}`)
		case "constant":
			p.ConstantMetrics = []string{"temp"}
		case "bad base":
			p.Base.Definition = json.RawMessage(`{"name":"broken","type":"threshold"}`)
		case "bad draft":
			p.Draft.Definition = json.RawMessage(`{"unexpected":true}`)
		}
		r, err := Run(context.Background(), p)
		if kind == "bad base" || kind == "bad draft" {
			if _, ok := err.(*InputError); !ok {
				t.Fatal(err)
			}
		} else if err != nil || r.Fidelity.Status != "UNSUPPORTED" || len(r.Windows) != 0 {
			t.Fatalf("%+v %v", r, err)
		}
	}
}
func TestOutcomeRequiresBothWindowsAndFailWithoutInconclusive(t *testing.T) {
	p := fixture(t)
	p.Expectations = p.Expectations[:1]
	if run(t, p).Outcome != "INCONCLUSIVE" {
		t.Fatal("missing NORMAL expectation passed")
	}
	p = fixture(t)
	p.Expectations[0].Expect = ruletest.Expect{NeverIntent: &ruletest.IntentExpect{Device: "d1", Command: "boost"}}
	r := run(t, p)
	if r.Outcome != "FAILED" || r.Expectations[0].Verdict != Fail || r.Expectations[0].Baseline != Fail {
		t.Fatal(r)
	}
	p.Source.(*source).msgs = p.Source.(*source).msgs[:2]
	if run(t, p).Outcome != "INCONCLUSIVE" {
		t.Fatal("FAIL with unknown normal history was conclusive")
	}
}

func TestAbsentSidesCannotPassOnNoReadings(t *testing.T) {
	p := fixture(t)
	// Both sides absent is refused outright (there is nothing to compare); with one side
	// absent and no readings, nothing may pass.
	p.Base = nil
	p.Draft = nil
	var ie *InputError
	if _, err := Run(context.Background(), p); !errors.As(err, &ie) {
		t.Fatalf("both sides absent: err = %v, want an InputError", err)
	}
	p.Base = nil
	p.Draft = fixture(t).Draft
	p.Source = &source{}
	p.Expectations = []Expectation{{Incident, ruletest.Expect{NeverRaised: &ruletest.EdgeExpect{Device: "d1"}}}, {Normal, ruletest.Expect{NeverIntent: &ruletest.IntentExpect{Device: "d1"}}}}
	r := run(t, p)
	if r.Outcome != "INCONCLUSIVE" || r.Expectations[0].Verdict != Inconclusive || r.Expectations[1].Verdict != Inconclusive {
		t.Fatal(r)
	}
}
