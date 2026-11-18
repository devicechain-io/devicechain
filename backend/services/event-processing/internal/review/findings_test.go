// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

func TestColdStartAggregateRaiseIsNotStateEvidence(t *testing.T) {
	p := fixture(t)
	def := json.RawMessage(`{"name":"avg","type":"aggregate","windowMode":"sliding","window":"5m","metric":"temp","agg":"avg","op":"gt","threshold":80}`)
	p.Base.Definition, p.Draft.Definition = def, def
	p.Source = &source{msgs: []messaging.Message{
		sample(t, 1, "d1", -9*time.Minute, "90"), sample(t, 2, "d1", -8*time.Minute, "95"), sample(t, 3, "d1", time.Minute, "95"),
	}}
	p.Expectations[0].Expect = ruletest.Expect{Raised: &ruletest.EdgeExpect{Device: "d1"}}
	r := run(t, p)
	w := r.Windows[0]
	if len(w.LeadInBaseEdges) == 0 || !w.LeadInBaseEdges[0].Raise {
		t.Fatal("fixture must raise on a partial aggregate", w.LeadInBaseEdges)
	}
	for _, e := range w.LeadInBaseEdges {
		if !e.At.Before(w.LeadInStart.Add(5 * time.Minute)) {
			t.Fatal("fixture has full-horizon edge", e)
		}
	}
	// Fails without fix 1: partial-window raise incorrectly makes StateBase RAISED.
	if w.Devices[0].StateBase != Unknown || w.Devices[0].StateDraft != Unknown || r.Expectations[0].Verdict != Inconclusive {
		t.Fatalf("cold-start state must remain UNKNOWN and expectation INCONCLUSIVE: %+v %+v", w.Devices, r.Expectations)
	}
}

func TestCompletenessIncludesDevicesWithoutExpectations(t *testing.T) {
	p := fixture(t)
	p.Devices = []string{"d1", "d2"}
	r := run(t, p)
	for _, e := range r.Expectations {
		if e.Verdict != Pass {
			t.Fatal(e)
		}
	}
	// Fails without fix 2: d1 passes, but missing d2 history must prevent PASSED.
	if r.Outcome != "INCONCLUSIVE" {
		t.Fatal(r.Outcome)
	}
	for _, role := range []Role{Incident, Normal} {
		found := false
		for _, reason := range r.Incomplete {
			found = found || (strings.Contains(reason, "d2") && strings.Contains(reason, string(role)) && strings.Contains(reason, "no reading"))
		}
		if !found {
			t.Fatalf("missing d2 %s completeness cause: %v", role, r.Incomplete)
		}
	}
}

func TestAdmittedDistinctEventsAreNotGuardedTwice(t *testing.T) {
	p := fixture(t)
	p.AppliedGuard = true
	def := json.RawMessage(`{"name":"sum","type":"aggregate","windowMode":"sliding","window":"5m","metric":"temp","agg":"sum","op":"gt","threshold":150}`)
	p.Base.Definition, p.Draft.Definition = def, def
	first, second := sample(t, 1, "d1", time.Minute, "90"), sample(t, 2, "d1", time.Minute, "90")
	ev, err := resolvedevent.UnmarshalResolvedEvent(second.Value)
	if err != nil {
		t.Fatal(err)
	}
	alternateID := "second-event"
	ev.AltId = &alternateID
	second.Value, err = resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	original, err := resolvedevent.UnmarshalResolvedEvent(first.Value)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := runtime.EventIdentity(p.Tenant, original)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := runtime.EventIdentity(p.Tenant, ev)
	if err != nil || id1 == id2 {
		t.Fatal("fixture requires distinct original identities", err)
	}
	p.Source = &source{msgs: []messaging.Message{first, second}}
	r := run(t, p)
	w := r.Windows[0]
	if w.Devices[0].Readings["temp"] != 2 {
		t.Fatal("both originals must be admitted", w.Devices)
	}
	// Fails without fix 3: evaluate's reconstructed identities drop the second 90, so sum never raises.
	if len(w.DraftEdges) != 1 || !w.DraftEdges[0].Raise {
		t.Fatal("both admitted samples must count", w.DraftEdges)
	}
}

func TestHistoricalCommandGuardDivergesAtLeadInStart(t *testing.T) {
	p := fixture(t)
	b, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	def, err := json.Marshal(map[string]any{
		"name": "hot", "type": "threshold", "when": map[string]any{"metric": "temp", "op": "gt", "threshold": 80},
		"actions": []any{map[string]any{"type": "sendCommand", "guard": "value > 90", "sendCommand": map[string]any{"command": "boost", "payload": `{"minutes":15}`}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	historical := *p.Base
	historical.Definition = def
	p.VersionRules = func(context.Context, string) (*RuleEnvelope, bool, error) { return &historical, true, nil }
	lo := epoch.Add(-time.Hour)
	got := divergence(context.Background(), p, b, b, evaluate.Result{}, evaluate.Result{}, DeviceCoverage{Device: "d1", Versions: map[string]int{"p@1": 1}}, lo)
	// Fails without fix 4: comparing only command and payload ignores the historical guard.
	if got == nil || !got.Equal(lo) {
		t.Fatal("guard change must diverge at lead-in start", got)
	}
}

func TestCommandRelativeExpiryDivergesAtIntent(t *testing.T) {
	p := fixture(t)
	b, err := prepare(p, p.Base, "base")
	if err != nil {
		t.Fatal(err)
	}
	bi, di := intent(time.Minute, `{}`), intent(time.Minute, `{}`)
	bt, dt := bi.At.Add(time.Minute), di.At.Add(2*time.Minute)
	bi.ExpiresAt, di.ExpiresAt = &bt, &dt
	got := divergence(context.Background(), p, b, b, evaluate.Result{Intents: []evaluate.Intent{bi}}, evaluate.Result{Intents: []evaluate.Intent{di}}, DeviceCoverage{Device: "d1"}, epoch)
	// Fails without fix 5: equal command/payload/instant with different TTLs incorrectly has no divergence.
	if got == nil || !got.Equal(bi.At) {
		t.Fatal("expiry must diverge at the command instant", got)
	}
}

func TestReadMetricTypeChangeAcrossLeadInAndWindow(t *testing.T) {
	p := fixture(t)
	s := p.Source.(*source)
	for i := range s.msgs {
		ev, err := resolvedevent.UnmarshalResolvedEvent(s.msgs[i].Value)
		if err != nil {
			t.Fatal(err)
		}
		typ := "DOUBLE"
		if i%2 == 1 {
			typ = "INTEGER"
		}
		ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[0].DataType = &typ
		s.msgs[i].Value, err = resolvedevent.MarshalResolvedEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := run(t, p)
	// Fails without fix 6: a read-metric type change must be covered, overlay matching expectations, and prevent PASSED.
	if r.Outcome != "INCONCLUSIVE" {
		t.Fatal(r.Outcome)
	}
	for _, w := range r.Windows {
		if !reflect.DeepEqual(w.Devices[0].MetricTypes["temp"], []string{"DOUBLE", "INTEGER"}) {
			t.Fatal(w.Devices)
		}
	}
	for _, e := range r.Expectations {
		if e.Verdict != Inconclusive || e.Baseline != Inconclusive {
			t.Fatal(e)
		}
		for _, word := range []string{"temp", "DOUBLE", "INTEGER"} {
			if !strings.Contains(e.Reason, word) || !strings.Contains(e.BaselineReason, word) {
				t.Fatal(e)
			}
		}
	}
	if !strings.Contains(strings.Join(r.Incomplete, " "), "metric-type change for temp") {
		t.Fatal(r.Incomplete)
	}
}

func TestDiffRemovesExactIntersectionBeforePairing(t *testing.T) {
	for _, n := range []int{1, 2} {
		bs := []evaluate.Intent{intent(0, `{"n":1}`)}
		ds := []evaluate.Intent{}
		for i := 0; i < n; i++ {
			bs = append(bs, intent(0, `{"n":2}`))
			ds = append(ds, intent(0, `{"n":2}`))
		}
		w := diffWindow(bs, ds, time.Minute)
		// Fails without fix 7: n=1 pairs n:1 with n:2 as CONTENT_CHANGED and removes n:2.
		if w.Counts != (Counts{Unchanged: n, Removed: 1}) || len(w.Changes) != 1 || w.Changes[0].Kind != Removed || w.Changes[0].BaseContent.Payload != `{"n":1}` {
			t.Fatalf("exact multiset intersection must survive: %+v", w)
		}
	}
}

func TestShiftAfterDivergenceUsesEitherEndpoint(t *testing.T) {
	point := epoch.Add(5 * time.Minute)
	for _, reverse := range []bool{false, true} {
		bi, di := intent(time.Minute, `{}`), intent(10*time.Minute, `{}`)
		if reverse {
			bi, di = di, bi
		}
		w := WindowResult{BaseIntents: []evaluate.Intent{bi}, DraftIntents: []evaluate.Intent{di}, Devices: []DeviceCoverage{{Device: "d1", Divergence: &point}}}
		diff(&w, side{}, side{}, 15*time.Minute)
		// Fails without fix 8: the earlier endpoint at minute 1 hides the endpoint after minute 5.
		if len(w.Changes) != 1 || w.Changes[0].Kind != Shifted || !w.Changes[0].AfterDivergence {
			t.Fatal(w.Changes)
		}
	}
}

// A device no expectation names still has to be complete for a PASSED review: readings it
// delivered late were declined by the engine, so its part of the diff is not the whole story.
func TestCompletenessIncludesLateDeclinesOnUntouchedDevices(t *testing.T) {
	p := fixture(t)
	p.Devices = []string{"d1", "d2"}
	src := p.Source.(*source)
	src.msgs = append(src.msgs,
		sample(t, 5, "d2", -time.Minute, "60"), sample(t, 6, "d2", 5*time.Minute, "90"), sample(t, 7, "d2", 0, "60"),
		sample(t, 8, "d2", 59*time.Minute, "60"), sample(t, 9, "d2", 61*time.Minute, "90"))
	r := run(t, p)
	for _, e := range r.Expectations {
		if e.Verdict != Pass {
			t.Fatal(e)
		}
	}
	late := false
	for _, w := range r.Windows {
		for _, c := range w.Devices {
			late = late || (c.Device == "d2" && (c.LateBase > 0 || c.LateDraft > 0))
		}
	}
	if !late {
		t.Fatal("fixture requires a late decline on d2")
	}
	// Fails without the late-decline completeness rule: every expectation passes, so PASSED.
	if r.Outcome != "INCONCLUSIVE" {
		t.Fatal(r.Outcome, r.Incomplete)
	}
	found := false
	for _, reason := range r.Incomplete {
		found = found || (strings.Contains(reason, "d2") && strings.Contains(reason, "late"))
	}
	if !found {
		t.Fatal(r.Incomplete)
	}
}
