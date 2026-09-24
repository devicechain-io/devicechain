// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/prometheus/client_golang/prometheus"
)

// testShedBudget is the configuration default, which is what every dispatcher built through the
// test helpers letters with.
var testShedBudget = ShedLetterBudget{PerTenantPerSecond: 1, PerTenantBurst: 60, GlobalPerSecond: 10, GlobalBurst: 100}

// meterGate records every time it is asked to meter and answers with admit.
type meterGate struct {
	mu    sync.Mutex
	admit bool
	at    []time.Time
}

func (g *meterGate) AllowAt(_ string, at time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.at = append(g.at, at)
	return g.admit
}

// connSink records the connector requests handed to it.
type connSink struct {
	mu  sync.Mutex
	got []react.ConnectorRequest
}

func (s *connSink) Dispatch(_ context.Context, req react.ConnectorRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, req)
	return nil
}

func (s *connSink) snapshot() []react.ConnectorRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]react.ConnectorRequest(nil), s.got...)
}

// connectorRule is a rule whose actions are the given ones, in order.
func connectorRule(actions ...rules.Action) rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Actions: actions}
}

func httpCallAction() rules.Action {
	return rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{URL: "https://hooks.example/x"}}
}

func publishAction() rules.Action {
	return rules.Action{Type: rules.ActionPublish, Publish: &rules.PublishAction{ConnectorRef: "kafka-main"}}
}

func sendCommandAction() rules.Action {
	return rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "setMode"}}
}

// reactWithConnectors builds a REACT consumer the way main.go does — through NewReactDispatcher,
// with a dead-letter sink from a producer on a Microservice with its own registry — over a
// connector sink and a source gate.
func reactWithConnectors(t *testing.T, rule rules.Rule, commands react.CommandSink, conns react.ConnectorSink,
	gate react.ConnectorRateGate, dead deadletter.Writer, budget ShedLetterBudget) (*ReactDispatcher, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	rd := NewReactDispatcher(ms, nil, reactFakeResolver{rule: rule, found: true}, commands, nil, conns, gate,
		deadletter.NewProducer(ms).NewSink(dead), budget, NewReactMetrics(ms))
	rd.procCtx = context.Background()
	return rd, reg
}

// labelledCounter reads name{action=action} off reg.
func labelledCounter(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatalf("the registry exports no %s{%s=%q}", name, label, value)
	return 0
}

const (
	shedLettered   = "devicechain_eventprocessing_react_connector_shed_dead_lettered_total"
	shedUnlettered = "devicechain_eventprocessing_react_connector_shed_unlettered_total"
	clockFallbacks = "devicechain_eventprocessing_rate_clock_fallback_total"
)

// A connector action the source gate shed is recorded as a dead letter: reason shed, the
// derived-events stream's kind, the rule as its reference, the derived event as its payload, and a
// dedup id of its own part of the message. The event is still acked.
func TestShedActionIsDeadLetteredWithReasonShed(t *testing.T) {
	dead := &deadRecorder{}
	rd, reg := reactWithConnectors(t, connectorRule(httpCallAction()), nil, &connSink{}, &meterGate{}, dead, testShedBudget)
	ack := &fakeAck{}
	msg := derivedMsg(t, "acme", sendCmdEvent(), 1, ack)

	rd.handle(msg)

	letters := dead.letters(t)
	if len(letters) != 1 {
		t.Fatalf("wrote %d letters, want 1", len(letters))
	}
	e := letters[0]
	if e.Reason != deadletter.ReasonShed || e.Kind != deadletter.KindDetectionAction {
		t.Errorf("reason/kind = %q/%q, want shed/detection-action", e.Reason, e.Kind)
	}
	if e.Reference != "acme/p@1/r1" || string(e.Payload) != string(msg.Value) {
		t.Errorf("reference = %q, payload = %q; want the rule id and the derived event", e.Reference, e.Payload)
	}
	token, ok := strings.CutPrefix(e.Detail, "httpCall/shed/")
	if !ok || token == "" {
		t.Fatalf("detail = %q, want httpCall/shed/<token>", e.Detail)
	}
	if want := deadletter.OriginID(msg) + ".part." + token; dead.msgs[0].DedupID != want {
		t.Errorf("dedup id = %q, want %q", dead.msgs[0].DedupID, want)
	}
	if dead.tenants[0] != "acme" {
		t.Errorf("the letter was written for tenant %q, want acme", dead.tenants[0])
	}
	if ack.acks != 1 {
		t.Errorf("acks = %d, want 1", ack.acks)
	}
	if got := labelledCounter(t, reg, shedLettered, "action", "httpCall"); got != 1 {
		t.Errorf("%s{httpCall} = %v, want 1", shedLettered, got)
	}
}

// Two actions of one event shed on one delivery are two letters, with two ids.
func TestTwoShedActionsInOneEventWriteTwoLetters(t *testing.T) {
	dead := &deadRecorder{}
	rd, _ := reactWithConnectors(t, connectorRule(httpCallAction(), publishAction()), nil, &connSink{},
		&meterGate{}, dead, testShedBudget)
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{}))

	if len(dead.msgs) != 2 {
		t.Fatalf("wrote %d letters, want 2", len(dead.msgs))
	}
	if dead.msgs[0].DedupID == dead.msgs[1].DedupID || dead.msgs[0].DedupID == "" {
		t.Fatalf("the two letters share dedup id %q: the broker would store one", dead.msgs[0].DedupID)
	}
}

// Letters are only for an attempt that stands. A shed alongside a sibling that failed is a Retry:
// the whole event redelivers and is re-metered, so no letter is written and nothing is acked.
func TestARetriedEventWritesNoShedLetter(t *testing.T) {
	dead := &deadRecorder{}
	rd, _ := reactWithConnectors(t, connectorRule(httpCallAction(), sendCommandAction()),
		&reactFakeSink{fail: true}, &connSink{}, &meterGate{}, dead, testShedBudget)
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, ack))

	if len(dead.msgs) != 0 {
		t.Fatalf("a retried event wrote %d letters", len(dead.msgs))
	}
	if ack.acks != 0 {
		t.Fatalf("a retried event was acked (%d)", ack.acks)
	}
}

// A letter that cannot be written is a counted loss, and it never holds the event back.
func TestAShedLetterWriteFailureIsCountedAndAcked(t *testing.T) {
	dead := &deadRecorder{err: errors.New("broker is away")}
	rd, reg := reactWithConnectors(t, connectorRule(httpCallAction()), nil, &connSink{}, &meterGate{}, dead, testShedBudget)
	ack := &fakeAck{}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, ack))

	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1: a lost letter must not hold the event", ack.acks)
	}
	if got := gatheredCounter(t, reg, reactLost); got != 1 {
		t.Fatalf("%s = %v, want 1", reactLost, got)
	}
	if got := labelledCounter(t, reg, shedLettered, "action", "httpCall"); got != 0 {
		t.Fatalf("a lost letter was counted as written (%v)", got)
	}
}

// The letters stay inside both budgets however fast a tenant sheds, everything over them is
// counted, and each tenant's excess is summarised in one letter when the window closes.
func TestShedLettersStayWithinTheTenantAndGlobalBudgets(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	t.Run("one tenant", func(t *testing.T) {
		dead := &deadRecorder{}
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
		reg := prometheus.NewRegistry()
		ms.UseMetricsRegistry(reg)
		s := newShedLetterer(deadletter.NewProducer(ms).NewSink(dead), testShedBudget, NewReactMetrics(ms))
		const sheds = 10_000
		msg := derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{})
		start := time.Now()
		for i := 0; i < sheds; i++ {
			s.letter(ctx, msg, sendCmdEvent(), []react.ShedAction{{Kind: "httpCall", Token: fmt.Sprintf("t%05d", i)}})
		}
		elapsed := time.Since(start)
		// The burst, plus whatever the per-tenant rate refilled while the loop ran.
		ceiling := testShedBudget.PerTenantBurst + int(elapsed.Seconds()*testShedBudget.PerTenantPerSecond) + 1
		lettered := len(dead.msgs)
		if lettered < testShedBudget.PerTenantBurst || lettered > ceiling {
			t.Fatalf("wrote %d individual letters in %v, want between %d and %d", lettered, elapsed,
				testShedBudget.PerTenantBurst, ceiling)
		}
		if got := labelledCounter(t, reg, shedUnlettered, "action", "httpCall"); got != float64(sheds-lettered) {
			t.Fatalf("%s = %v, want %d", shedUnlettered, got, sheds-lettered)
		}
		s.flush(context.Background())
		letters := dead.letters(t)
		if len(letters) != lettered+1 {
			t.Fatalf("flush wrote %d summary letters, want 1", len(letters)-lettered)
		}
		summary := letters[len(letters)-1]
		if want := fmt.Sprintf("httpCall=%d publish=0 window=", sheds-lettered); !strings.HasPrefix(summary.Detail, want) ||
			summary.Reason != deadletter.ReasonShed || summary.Kind != deadletter.KindDetectionAction ||
			dead.tenants[len(dead.tenants)-1] != "acme" || dead.msgs[len(dead.msgs)-1].DedupID != "" {
			t.Fatalf("summary = %+v (tenant %q), want %q… for acme with reason shed", summary,
				dead.tenants[len(dead.tenants)-1], want)
		}
		s.flush(context.Background())
		if len(dead.msgs) != lettered+1 {
			t.Fatal("a second flush with nothing new wrote another summary")
		}
	})
	t.Run("many tenants", func(t *testing.T) {
		dead := &deadRecorder{}
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		s := newShedLetterer(deadletter.NewProducer(ms).NewSink(dead), testShedBudget, NewReactMetrics(ms))
		start := time.Now()
		for tn := 0; tn < 50; tn++ {
			tenant := fmt.Sprintf("t%02d", tn)
			ev := sendCmdEvent()
			ev.Tenant, ev.RuleID = tenant, tenant+"/p@1/r1"
			msg := derivedMsg(t, tenant, ev, 1, &fakeAck{})
			for i := 0; i < 10; i++ {
				s.letter(core.WithTenant(context.Background(), tenant), msg, ev,
					[]react.ShedAction{{Kind: "publish", Token: fmt.Sprintf("t%02d", i)}})
			}
		}
		ceiling := testShedBudget.GlobalBurst + int(time.Since(start).Seconds()*testShedBudget.GlobalPerSecond) + 1
		if len(dead.msgs) > ceiling || len(dead.msgs) < testShedBudget.GlobalBurst {
			t.Fatalf("50 tenants wrote %d letters, want the global budget (%d..%d)", len(dead.msgs),
				testShedBudget.GlobalBurst, ceiling)
		}
	})
}

// Stop summarises the window it cuts short: the counts since the last flush are not dropped.
func TestStopFlushesTheSummary(t *testing.T) {
	dead := &deadRecorder{}
	rd, _ := reactWithConnectors(t, connectorRule(httpCallAction()), nil, &connSink{}, &meterGate{}, dead,
		ShedLetterBudget{}) // no budget: every shed is summarised
	rd.reader = &fakeReader{}
	if err := rd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 1, &fakeAck{}))
	rd.handle(derivedMsg(t, "acme", sendCmdEvent(), 2, &fakeAck{}))
	if len(dead.msgs) != 0 {
		t.Fatalf("a zero budget wrote %d individual letters", len(dead.msgs))
	}
	if err := rd.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	letters := dead.letters(t)
	if len(letters) != 1 || !strings.HasPrefix(letters[0].Detail, "httpCall=2 publish=0 window=") {
		t.Fatalf("Stop wrote %+v, want one summary of 2 httpCall sheds", letters)
	}
}

// REACT meters on ONE time and forwards that same time on the wire: the stamped trigger time,
// capped at the derived event's broker time; the broker time when there is no stamp; and now
// (the zero time) when there is neither. Each fallback is counted by the clock it fell back to.
func TestMeteringFallsBackAppendThenNow(t *testing.T) {
	appended := time.Now().Add(-time.Minute).UTC()
	stamped := appended.Add(-10 * time.Second)
	cases := []struct {
		name        string
		triggeredAt time.Time
		appendTime  time.Time
		want        time.Time
	}{
		{"stamped", stamped, appended, stamped},
		{"stamp after the broker time is capped", appended.Add(time.Hour), appended, appended},
		{"no stamp", time.Time{}, appended, appended},
		{"neither", time.Time{}, time.Time{}, time.Time{}},
	}
	gate := &meterGate{admit: true}
	sink := &connSink{}
	rd, reg := reactWithConnectors(t, connectorRule(httpCallAction()), nil, sink, gate, &deadRecorder{}, testShedBudget)
	for i, c := range cases {
		ev := sendCmdEvent()
		ev.TriggeredAt = c.triggeredAt
		msg := derivedMsg(t, "acme", ev, 1, &fakeAck{})
		msg.AppendTime = c.appendTime
		rd.handle(msg)
		if !gate.at[i].Equal(c.want) {
			t.Errorf("%s: gate metered %v, want %v", c.name, gate.at[i], c.want)
		}
		if got := sink.snapshot()[i].TriggeredAt; !got.Equal(c.want) {
			t.Errorf("%s: forwarded triggeredAt %v, want the metered %v", c.name, got, c.want)
		}
	}
	if got := labelledCounter(t, reg, clockFallbacks, "source", "append"); got != 2 {
		t.Errorf("append fallbacks = %v, want 2", got)
	}
	if got := labelledCounter(t, reg, clockFallbacks, "source", "now"); got != 1 {
		t.Errorf("now fallbacks = %v, want 1", got)
	}
}
