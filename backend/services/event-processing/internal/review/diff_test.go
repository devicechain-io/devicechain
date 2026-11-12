// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
)

func intent(at time.Duration, payload string) evaluate.Intent {
	return evaluate.Intent{At: epoch.Add(at), Series: "d1", Kind: evaluate.KindSendCommand, Command: "boost", Payload: payload}
}
func diffWindow(bs, ds []evaluate.Intent, shift time.Duration) WindowResult {
	w := WindowResult{BaseIntents: bs, DraftIntents: ds}
	diff(&w, side{}, side{}, shift)
	return w
}
func TestShiftWalkBoundaryContentAndSymmetry(t *testing.T) {
	for _, tc := range []struct {
		delta, window time.Duration
		want          Counts
	}{
		{time.Minute, time.Minute, Counts{Shifted: 1}},
		{-time.Minute, time.Minute, Counts{Shifted: 1}},
		{time.Minute + time.Nanosecond, time.Minute, Counts{Added: 1, Removed: 1}},
		{0, time.Minute, Counts{ContentChanged: 1}},
	} {
		bs := []evaluate.Intent{intent(0, `{"a":1}`)}
		ds := []evaluate.Intent{intent(tc.delta, `{"a":2}`)}
		a := diffWindow(bs, ds, tc.window)
		b := diffWindow(ds, bs, tc.window)
		if a.Counts != tc.want || a.Counts.Added != b.Counts.Removed || a.Counts.Removed != b.Counts.Added {
			t.Fatalf("%+v %+v", a, b)
		}
		if len(a.Changes) == 1 {
			if a.Changes[0].Shift != -b.Changes[0].Shift || !a.Changes[0].ContentChanged {
				t.Fatal(a.Changes)
			}
		}
	}
	// Canonical payload order and absolute expiry/idempotency identities do not change content.
	a, b := intent(0, `{"a":1,"b":2}`), intent(0, `{ "b":2,"a":1 }`)
	a.Token = "old"
	b.Token = "new"
	ta, tb := epoch, epoch.Add(time.Hour)
	a.ExpiresAt = &ta
	b.ExpiresAt = &tb
	w := diffWindow([]evaluate.Intent{a}, []evaluate.Intent{b}, 0)
	if w.Counts.Unchanged != 1 || len(w.Changes) != 0 {
		t.Fatal(w)
	}
}
func TestDiffMultisetAndDeterminism(t *testing.T) {
	var bs, ds []evaluate.Intent
	for i := 0; i < 90; i++ {
		b := intent(time.Duration(i*30)*time.Minute, fmt.Sprintf(`{"n":%d}`, i))
		b.Series = fmt.Sprintf("d%d", i%3)
		bs = append(bs, b)
		if i%4 != 0 {
			d := b
			d.At = d.At.Add(time.Duration(i%3) * time.Minute)
			if i%5 == 0 {
				d.Payload = `{"n":-1}`
			}
			ds = append(ds, d)
		}
	}
	first := diffWindow(bs, ds, 3*time.Minute)
	encoded, _ := json.Marshal(first)
	for i := 0; i < 10; i++ {
		next := diffWindow(bs, ds, 3*time.Minute)
		b, _ := json.Marshal(next)
		if string(encoded) != string(b) {
			t.Fatal("nondeterministic")
		}
	}
	// Every changed element is represented exactly once on its side; unchanged
	// elements are the precise remainder, even when a shifted pair changes content.
	baseUsed, draftUsed := 0, 0
	for _, c := range first.Changes {
		if c.Base != nil {
			baseUsed++
		}
		if c.Draft != nil {
			draftUsed++
		}
	}
	if baseUsed+first.Counts.Unchanged != len(bs) || draftUsed+first.Counts.Unchanged != len(ds) {
		t.Fatal("walk lost or duplicated an element")
	}
	reverse := diffWindow(ds, bs, 3*time.Minute)
	if first.Counts.Added != reverse.Counts.Removed || first.Counts.Removed != reverse.Counts.Added || first.Counts.Shifted != reverse.Counts.Shifted {
		t.Fatal("swap invariants")
	}
}
func TestDiffTruncationAndDivergenceStrict(t *testing.T) {
	var ds []evaluate.Intent
	for i := 0; i < MaxListedChanges+5; i++ {
		ds = append(ds, intent(time.Duration(i)*time.Second, `{}`))
	}
	w := diffWindow(nil, ds, 0)
	if w.Counts.Added != len(ds) || len(w.Changes) != MaxListedChanges || !w.ChangesTruncated {
		t.Fatal(w.Counts)
	}
	point := epoch
	w = WindowResult{DraftIntents: ds[:2], Devices: []DeviceCoverage{{Device: "d1", Divergence: &point}}}
	diff(&w, side{}, side{}, 0)
	if w.Changes[0].AfterDivergence || !w.Changes[1].AfterDivergence {
		t.Fatal(w.Changes)
	}
}
func TestDiffContentDiscriminatorsAndReading(t *testing.T) {
	s := side{}
	s.def.Type = "aggregate"
	s.def.Window = 300000000000
	edges := []evaluate.Edge{{At: epoch, Series: "d1", RuleToken: "hot", Raise: true, Severity: "major", Value: 92, HasValue: true}}
	ins := []evaluate.Intent{{At: epoch, Series: "d1", Raise: true, Kind: "raiseAlarm", AlarmKey: "hot", Severity: "major"}, {At: epoch, Series: "d1", Raise: true, Kind: "httpCall", URL: "https://example.com", Method: "POST", Body: "body"}, {At: epoch, Series: "d1", Raise: true, Kind: "publish", Connector: "mqtt", Body: "body"}}
	es := elements(edges, ins, s)
	if len(es) != 4 || es[0].key.subject != "EDGE_RAISED" || es[1].key.discriminator != "hot" || es[2].key.discriminator != "https://example.com" || es[3].key.discriminator != "mqtt" || es[2].content.BodyDigest != digest("body") {
		t.Fatal(es)
	}
	for _, e := range es {
		if !reflect.DeepEqual(e.reading, ReadingRef{Device: "d1", At: epoch, Value: 92, HasValue: true, Span: 5 * time.Minute}) {
			t.Fatal(e)
		}
	}
}

func TestConnectorSeverityAndRelativeExpiryChanges(t *testing.T) {
	for _, kind := range []string{"severity", "body", "method", "expiry"} {
		b, d := side{}, side{}
		w := WindowResult{}
		switch kind {
		case "severity":
			w.BaseEdges = []evaluate.Edge{{At: epoch, Series: "d1", RuleToken: "hot", Raise: true, Severity: "minor"}}
			w.DraftEdges = []evaluate.Edge{{At: epoch, Series: "d1", RuleToken: "hot", Raise: true, Severity: "major"}}
		case "body", "method":
			in := evaluate.Intent{At: epoch, Series: "d1", Kind: "httpCall", URL: "https://example.com", Body: "a", Method: "POST"}
			w.BaseIntents = []evaluate.Intent{in}
			if kind == "body" {
				in.Body = "b"
			} else {
				in.Method = "PUT"
			}
			w.DraftIntents = []evaluate.Intent{in}
		case "expiry":
			p := fixture(t)
			p.Base.Definition = json.RawMessage(`{"name":"x","type":"threshold","when":{"metric":"temp","op":"gt","threshold":80},"actions":[{"type":"sendCommand","sendCommand":{"command":"boost","payload":"{}","expiresAfter":"1m"}}]}`)
			p.Draft.Definition = json.RawMessage(`{"name":"x","type":"threshold","when":{"metric":"temp","op":"gt","threshold":80},"actions":[{"type":"sendCommand","sendCommand":{"command":"boost","payload":"{}","expiresAfter":"2m"}}]}`)
			var err error
			b, err = prepare(p, p.Base, "base")
			if err != nil {
				t.Fatal(err)
			}
			d, err = prepare(p, p.Draft, "draft")
			if err != nil {
				t.Fatal(err)
			}
			bi, di := intent(0, `{}`), intent(0, `{}`)
			bt, dt := epoch.Add(time.Minute), epoch.Add(2*time.Minute)
			bi.ExpiresAt = &bt
			di.ExpiresAt = &dt
			w.BaseIntents = []evaluate.Intent{bi}
			w.DraftIntents = []evaluate.Intent{di}
		}
		diff(&w, b, d, 0)
		if w.Counts.ContentChanged != 1 || len(w.Changes) != 1 || w.Changes[0].Kind != ContentChanged {
			t.Fatalf("%s %+v", kind, w)
		}
	}
}
