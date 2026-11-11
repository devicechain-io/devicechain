// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/ruletest"
)

var epoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type source struct {
	msgs   []messaging.Message
	first  time.Time
	opens  int
	block  bool
	forbid *testing.T
}

func (s *source) NewReplayReaderFromTime(_ string, _ time.Time) (messaging.ReplayReader, time.Time, error) {
	if s.forbid != nil {
		s.forbid.Fatal("unsupported review opened source")
	}
	s.opens++
	return &reader{msgs: s.msgs, block: s.block}, s.first, nil
}

type reader struct {
	msgs  []messaging.Message
	block bool
}

func (r *reader) Read(ctx context.Context) (messaging.Message, error) {
	if len(r.msgs) > 0 {
		m := r.msgs[0]
		r.msgs = r.msgs[1:]
		return m, nil
	}
	if r.block {
		<-ctx.Done()
		return messaging.Message{}, ctx.Err()
	}
	return messaging.Message{}, io.EOF
}
func (r *reader) Close() error { return nil }
func sample(t *testing.T, seq uint64, dev string, offset time.Duration, v string) messaging.Message {
	t.Helper()
	at := epoch.Add(offset)
	b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "test", SourceDeviceToken: dev, ProfileVersionToken: "p@1", OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement, Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: v}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return messaging.Message{Subject: "dc.acme.resolved-events", Value: b, StreamSeq: seq, AppendTime: at}
}
func envelope(payload string) *RuleEnvelope {
	return &RuleEnvelope{Token: "hot", Enabled: true, Definition: json.RawMessage(`{"name":"hot","type":"threshold","when":{"metric":"temp","op":"gt","threshold":80},"actions":[{"type":"sendCommand","sendCommand":{"command":"boost","payload":` + quoted(payload) + `}}]}`)}
}
func quoted(s string) string { b, _ := json.Marshal(s); return string(b) }
func fixture(t *testing.T) Params {
	b := envelope(`{"minutes":15}`)
	return Params{Tenant: "acme", ProfileToken: "p", Base: b, Draft: envelope(`{"minutes":15}`), Devices: []string{"d1"}, Windows: []Window{{Incident, epoch, epoch.Add(10 * time.Minute)}, {Normal, epoch.Add(time.Hour), epoch.Add(70 * time.Minute)}}, Now: epoch.Add(2 * time.Hour), Source: &source{msgs: []messaging.Message{sample(t, 1, "d1", -time.Minute, "60"), sample(t, 2, "d1", time.Minute, "90"), sample(t, 3, "d1", 59*time.Minute, "60"), sample(t, 4, "d1", 61*time.Minute, "90")}}, Suffix: "resolved-events", VersionRules: func(context.Context, string) (*RuleEnvelope, bool, error) { return b, true, nil }, Expectations: []Expectation{{Incident, ruletest.Expect{Intent: &ruletest.IntentExpect{Device: "d1", Command: "boost"}}}, {Normal, ruletest.Expect{Intent: &ruletest.IntentExpect{Device: "d1", Command: "boost"}}}}}
}
func run(t *testing.T, p Params) Result {
	t.Helper()
	r, e := Run(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestNoopAndDeterminism(t *testing.T) {
	p := fixture(t)
	a := run(t, p)
	b := run(t, p)
	if a.Outcome != "PASSED" {
		t.Fatalf("%+v", a)
	}
	for _, w := range a.Windows {
		if len(w.BaseIntents) == 0 || len(w.Changes) != 0 {
			t.Fatalf("%+v", w)
		}
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatal("not deterministic")
	}
}
func TestPayloadAndDisabled(t *testing.T) {
	p := fixture(t)
	p.Draft = envelope(`{"minutes":60}`)
	r := run(t, p)
	found := false
	for _, c := range r.Windows[0].Changes {
		if c.Subject == "sendCommand" && c.Kind == ContentChanged {
			found = true
		}
	}
	if !found {
		t.Fatal("missing content change")
	}
	p.Draft.Enabled = false
	r = run(t, p)
	for _, c := range r.Windows[0].Changes {
		if c.Kind != Removed {
			t.Fatalf("%+v", c)
		}
	}
}
func TestEligibilityNoRead(t *testing.T) {
	for _, def := range []string{
		`{"name":"x","type":"threshold","when":{"metric":"temp","op":"gt","thresholdAttr":"limit"}}`,
		`{"name":"x","type":"absence","timeout":"5m"}`,
	} {
		p := fixture(t)
		p.Draft.Definition = json.RawMessage(def)
		p.Source = &source{forbid: t}
		r := run(t, p)
		if r.Fidelity.Status != "UNSUPPORTED" || r.Outcome != "INCONCLUSIVE" || len(r.Windows) != 0 {
			t.Fatalf("%+v", r)
		}
	}
	p := fixture(t)
	p.Draft.EntityGroupToken = "group"
	p.Source = &source{forbid: t}
	if r := run(t, p); len(r.Windows) != 0 {
		t.Fatal(r)
	}
}
func TestDegradedAndUnknown(t *testing.T) {
	for _, kind := range []string{"aged", "scan", "read", "budget", "corrupt", "no readings", "unknown", "raised", "divergence"} {
		t.Run(kind, func(t *testing.T) {
			p := fixture(t)
			s := p.Source.(*source)
			switch kind {
			case "aged":
				s.first = epoch
			case "scan":
				p.MaxScan = 1
			case "read":
				p.MaxRead = 1
			case "budget":
				s.block = true
				p.WindowBudget = time.Millisecond
			case "corrupt":
				s.msgs = append([]messaging.Message{{Subject: "dc.acme.resolved-events", Value: []byte("bad"), StreamSeq: 1}}, s.msgs...)
			case "no readings":
				p.Devices = append(p.Devices, "empty")
				p.Expectations[0].Expect.Intent.Device = ""
			case "unknown":
				s.msgs = s.msgs[1:]
			case "raised":
				s.msgs[0] = sample(t, 1, "d1", -time.Minute, "90")
				p.Expectations[0].Expect = ruletest.Expect{NeverRaised: &ruletest.EdgeExpect{Device: "d1"}}
			case "divergence":
				p.VersionRules = nil
			}
			r := run(t, p)
			if r.Outcome == "PASSED" || r.Expectations[0].Verdict != Inconclusive {
				t.Fatalf("%+v", r)
			}
			if kind == "aged" || kind == "scan" || kind == "read" || kind == "budget" || kind == "corrupt" {
				if len(r.Windows[0].Degraded) == 0 {
					t.Fatal("missing degradation")
				}
			}
		})
	}
}
func TestValidationBeforeRead(t *testing.T) {
	for _, kind := range []string{"devices", "windows", "duration", "recent", "expectations", "allow", "count"} {
		t.Run(kind, func(t *testing.T) {
			p := fixture(t)
			p.Source = &source{forbid: t}
			switch kind {
			case "devices":
				for i := 0; i < 50; i++ {
					p.Devices = append(p.Devices, fmt.Sprintf("extra-%d", i))
				}
			case "windows":
				p.Windows = append(p.Windows, p.Windows[0])
			case "duration":
				p.Windows[0].Start = p.Windows[0].End.Add(-25 * time.Hour)
			case "recent":
				p.Now = epoch
			case "expectations":
				for i := 0; i < 11; i++ {
					p.Expectations = append(p.Expectations, p.Expectations[0])
				}
			case "allow":
				p.Expectations[0].Expect.AllowDegraded = true
			case "count":
				n := 1
				p.Expectations[0].Expect.Intent.Count = &n
			}
			_, err := Run(context.Background(), p)
			if _, ok := err.(*InputError); !ok {
				t.Fatalf("want InputError: %v", err)
			}
		})
	}
}
