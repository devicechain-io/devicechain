// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

var t0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }

func params(rs []Rule, evs []Event) Params {
	return Params{Tenant: "acme", ProfileVersion: "p@1", Rules: rs, Events: evs}
}

func reading(dev string, at time.Time, metric string, v float64) Event {
	return Event{Device: dev, At: at, Metrics: []Metric{{Name: metric, Value: v}}}
}

func alarmAction() rules.Action {
	return rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}}
}

func hotRule(actions ...rules.Action) Rule {
	return Rule{Token: "hot", Def: rules.Rule{
		Name: "hot", Type: rules.TypeThreshold, Severity: rules.SeverityCritical,
		When:    rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: f64(80)},
		Actions: actions,
	}}
}

func mustRun(t *testing.T, p Params) Result {
	t.Helper()
	res, err := Run(context.Background(), p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// A rising crossing derives a raised edge and the alarm raise REACT would request; the falling
// crossing derives a resolved edge and the paired clear. Both name the rule by its token.
func TestThresholdEdgesAndAlarmIntents(t *testing.T) {
	res := mustRun(t, params([]Rule{hotRule(alarmAction())}, []Event{
		reading("d1", t0, "temperature", 70),
		reading("d1", t0.Add(time.Minute), "temperature", 90),
		reading("d1", t0.Add(2*time.Minute), "temperature", 60),
	}))
	if len(res.Edges) != 2 {
		t.Fatalf("edges = %+v, want a raise then a resolve", res.Edges)
	}
	r, c := res.Edges[0], res.Edges[1]
	if !r.Raise || r.Series != "d1" || r.RuleToken != "hot" || !r.At.Equal(t0.Add(time.Minute)) || r.Kind != "threshold" ||
		r.Severity != "critical" || !r.HasValue || r.Value != 90 {
		t.Fatalf("raise edge = %+v", r)
	}
	if c.Raise || !c.At.Equal(t0.Add(2*time.Minute)) || c.HasValue {
		t.Fatalf("resolve edge = %+v", c)
	}
	if len(res.Intents) != 2 {
		t.Fatalf("intents = %+v, want raise then clear", res.Intents)
	}
	ra, cl := res.Intents[0], res.Intents[1]
	if ra.Kind != KindRaiseAlarm || ra.AlarmKey != "overheat" || ra.Severity != "critical" || ra.Value == nil || *ra.Value != 90 || !ra.Raise {
		t.Fatalf("raise intent = %+v", ra)
	}
	if cl.Kind != KindClearAlarm || cl.AlarmKey != "overheat" || cl.Raise || cl.Value != nil {
		t.Fatalf("clear intent = %+v", cl)
	}
	if ra.Token == "" || ra.Token == cl.Token {
		t.Fatalf("raise and clear must carry distinct non-empty tokens: %q %q", ra.Token, cl.Token)
	}
	if res.Stats.EventsFed != 3 || res.Stats.RulesEvaluated != 1 {
		t.Fatalf("stats = %+v", res.Stats)
	}
}

// The boundary is part of the rule: a reading AT the threshold does not satisfy ">".
func TestThresholdBoundaryIsExclusive(t *testing.T) {
	res := mustRun(t, params([]Rule{hotRule()}, []Event{reading("d1", t0, "temperature", 80)}))
	if len(res.Edges) != 0 {
		t.Fatalf("edges = %+v, want none at exactly the threshold", res.Edges)
	}
}

// A sendCommand intent carries the rendered key and arguments; it fires on the rising edge only,
// and its expiry is measured from the virtual instant, not the wall clock.
func TestSendCommandIntentAndVirtualExpiry(t *testing.T) {
	cmd := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{
		Command: "set-fan", Payload: `{"speed":3}`, ExpiresAfter: "15m"}}
	res := mustRun(t, params([]Rule{hotRule(cmd)}, []Event{
		reading("d1", t0, "temperature", 95),
		reading("d1", t0.Add(time.Hour), "temperature", 50),
	}))
	if len(res.Edges) != 2 || len(res.Intents) != 1 {
		t.Fatalf("edges=%d intents=%+v, want a command on the raise and none on the resolve", len(res.Edges), res.Intents)
	}
	in := res.Intents[0]
	if in.Kind != KindSendCommand || in.Command != "set-fan" || in.Payload != `{"speed":3}` || in.Series != "d1" {
		t.Fatalf("intent = %+v", in)
	}
	if in.ExpiresAt == nil || !in.ExpiresAt.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("expiresAt = %v, want the detection instant + 15m (%v)", in.ExpiresAt, t0.Add(15*time.Minute))
	}
}

// A connector action's body is rendered by REACT's own template engine from the derived event.
func TestConnectorIntentsRenderTheirBody(t *testing.T) {
	http := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{
		URL: "https://hooks.example.com/alerts", BodyTemplate: `"device=" + series`}}
	pub := rules.Action{Type: rules.ActionPublish, Publish: &rules.PublishAction{
		ConnectorRef: "plant-mqtt", PayloadTemplate: `"hot:" + series`}}
	res := mustRun(t, params([]Rule{hotRule(http, pub)}, []Event{reading("d9", t0, "temperature", 99)}))
	if len(res.Intents) != 2 {
		t.Fatalf("intents = %+v", res.Intents)
	}
	h, p := res.Intents[0], res.Intents[1]
	if h.Kind != KindHTTPCall || h.URL != "https://hooks.example.com/alerts" || h.Method != "POST" || h.Body != "device=d9" {
		t.Fatalf("httpCall intent = %+v", h)
	}
	if p.Kind != KindPublish || p.Connector != "plant-mqtt" || p.Body != "hot:d9" {
		t.Fatalf("publish intent = %+v", p)
	}
}

// A guard that is false on the detection suppresses the action, as it does in production.
func TestGuardSuppressesTheIntent(t *testing.T) {
	cmd := rules.Action{Type: rules.ActionSendCommand, Guard: "value > 95.0",
		SendCommand: &rules.SendCommandAction{Command: "shutdown"}}
	res := mustRun(t, params([]Rule{hotRule(alarmAction(), cmd)}, []Event{
		reading("d1", t0, "temperature", 90),
		reading("d2", t0, "temperature", 99),
	}))
	var cmds []string
	for _, in := range res.Intents {
		if in.Kind == KindSendCommand {
			cmds = append(cmds, in.Series)
		}
	}
	if !reflect.DeepEqual(cmds, []string{"d2"}) {
		t.Fatalf("shutdown went to %v, want only d2 (value 99 > 95)", cmds)
	}
}

func absenceRule(timeout time.Duration) Rule {
	return Rule{Token: "silent", Def: rules.Rule{
		Name: "silent", Type: rules.TypeAbsence, Severity: rules.SeverityMajor, Ttl: rules.Duration(timeout),
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "offline"}}},
	}}
}

// The acceptance case: a rule that waits seven days is decided by advancing seven days, which is
// arithmetic. The edge is stamped at the deadline to the nanosecond, the run takes milliseconds,
// and no wall clock is read.
func TestSevenDayAbsenceIsTestedInMilliseconds(t *testing.T) {
	week := 7 * 24 * time.Hour
	// The platform caps a rule's duration (a day unless the operator raises it); a week-long rule
	// is a rule on an instance configured for one, and the evaluation honours the same cap.
	rules.SetPlatformMaxRuleDuration(8 * 24 * time.Hour)
	t.Cleanup(func() { rules.SetPlatformMaxRuleDuration(0) })
	p := params([]Rule{absenceRule(week)}, []Event{reading("d1", t0, "temperature", 20)})
	p.AdvanceTo = t0.Add(week + time.Hour)
	started := time.Now()
	res := mustRun(t, p)
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("a 7-day absence took %s; it must be arithmetic, not waiting", took)
	}
	if len(res.Edges) != 1 || !res.Edges[0].Raise || !res.Edges[0].At.Equal(t0.Add(week)) || res.Edges[0].HasValue {
		t.Fatalf("edges = %+v, want one value-less raise at exactly t0+7d", res.Edges)
	}
	if len(res.Intents) != 1 || res.Intents[0].Kind != KindRaiseAlarm || res.Intents[0].AlarmKey != "offline" {
		t.Fatalf("intents = %+v", res.Intents)
	}

	// One second short of the deadline decides nothing: the fast-forward is exact.
	p.AdvanceTo = t0.Add(week - time.Second)
	if res := mustRun(t, p); len(res.Edges) != 0 {
		t.Fatalf("edges = %+v before the deadline, want none", res.Edges)
	}
	// AND an edge stamped exactly on AdvanceTo is inside the run.
	p.AdvanceTo = t0.Add(week)
	if res := mustRun(t, p); len(res.Edges) != 1 {
		t.Fatalf("edges = %+v at the deadline instant, want the raise", res.Edges)
	}
}

// A heartbeat after the deadline is the recovery that resolves the absence.
func TestAbsenceResolvesOnTheNextReading(t *testing.T) {
	res := mustRun(t, params([]Rule{absenceRule(time.Hour)}, []Event{
		reading("d1", t0, "temperature", 1),
		reading("d1", t0.Add(3*time.Hour), "temperature", 1),
	}))
	if len(res.Edges) != 2 || !res.Edges[0].Raise || res.Edges[1].Raise || !res.Edges[1].At.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("edges = %+v, want raise at +1h and resolve at +3h", res.Edges)
	}
	if len(res.Intents) != 2 || res.Intents[1].Kind != KindClearAlarm {
		t.Fatalf("intents = %+v", res.Intents)
	}
}

// The same input yields the same output, byte for byte: nothing in a run depends on the clock or on
// map iteration order.
func TestRunIsDeterministic(t *testing.T) {
	cmd := rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "c", ExpiresAfter: "1h"}}
	var evs []Event
	for i := 0; i < 200; i++ {
		v := 70.0
		if i%7 == 0 {
			v = 90
		}
		evs = append(evs, reading([]string{"a", "b", "c"}[i%3], t0.Add(time.Duration(i)*time.Minute), "temperature", v))
	}
	p := params([]Rule{hotRule(alarmAction(), cmd), absenceRule(30 * time.Minute)}, evs)
	p.AdvanceTo = t0.Add(48 * time.Hour)
	first := mustRun(t, p)
	for i := 0; i < 5; i++ {
		if again := mustRun(t, p); !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs from the first", i)
		}
	}
	if len(first.Edges) == 0 || len(first.Intents) == 0 {
		t.Fatal("the determinism fixture derived nothing, so it proves nothing")
	}
}

// Edges come out in time order whatever order the rules were given in.
func TestEdgeOrderIsStable(t *testing.T) {
	a := mustRun(t, params([]Rule{hotRule(), absenceRule(time.Hour)}, []Event{
		reading("d1", t0, "temperature", 90), reading("d2", t0, "temperature", 90),
	}))
	b := mustRun(t, params([]Rule{absenceRule(time.Hour), hotRule()}, []Event{
		reading("d1", t0, "temperature", 90), reading("d2", t0, "temperature", 90),
	}))
	if !reflect.DeepEqual(a.Edges, b.Edges) {
		t.Fatalf("rule order changed the result:\n%+v\n%+v", a.Edges, b.Edges)
	}
}

// A reading delivered far behind the others is declined by the engine and counted, so the author is
// told rather than shown a quiet rule.
func TestLateDeliveryIsReported(t *testing.T) {
	agg := Rule{Token: "agg", Def: rules.Rule{Name: "agg", Type: rules.TypeAggregate, Severity: rules.SeverityMinor,
		Metric: "temperature", Agg: rules.AggAvg, Op: rules.OpGt, Threshold: f64(1), Window: rules.Duration(10 * time.Minute), Mode: rules.ModeSliding}}
	evs := []Event{
		reading("d1", t0.Add(2*time.Hour), "temperature", 5),
		reading("d1", t0, "temperature", 5), // delivered after a reading two hours ahead of it
	}
	res := mustRun(t, params([]Rule{agg}, evs))
	if res.Stats.LateReadings == 0 || !strings.Contains(res.Degraded, "arrived too far behind") {
		t.Fatalf("stats=%+v degraded=%q, want the late reading reported", res.Stats, res.Degraded)
	}
}

// The duplicate guard follows the live setting: with it on a duplicate is dropped and counted.
func TestAppliedGuardDropsADuplicate(t *testing.T) {
	evs := []Event{reading("d1", t0, "temperature", 90), reading("d1", t0, "temperature", 90)}
	off := mustRun(t, params([]Rule{hotRule()}, evs))
	if off.Stats.EventsFed != 2 || off.Stats.EventsDropped != 0 {
		t.Fatalf("guard off: %+v", off.Stats)
	}
	p := params([]Rule{hotRule()}, evs)
	p.AppliedGuard = true
	on := mustRun(t, p)
	if on.Stats.EventsFed != 1 || on.Stats.EventsDropped != 1 {
		t.Fatalf("guard on: %+v, want the duplicate dropped", on.Stats)
	}
}

func TestIntentLimitTruncatesLoudly(t *testing.T) {
	var evs []Event
	for i := 0; i < 20; i++ {
		evs = append(evs, reading("d1", t0.Add(time.Duration(2*i)*time.Minute), "temperature", 90),
			reading("d1", t0.Add(time.Duration(2*i+1)*time.Minute), "temperature", 10))
	}
	p := params([]Rule{hotRule(alarmAction())}, evs)
	p.MaxIntents = 5
	res := mustRun(t, p)
	if len(res.Intents) != 5 || !strings.Contains(res.Degraded, "intent limit of 5") {
		t.Fatalf("intents=%d degraded=%q", len(res.Intents), res.Degraded)
	}
	if len(res.Edges) != 40 {
		t.Fatalf("edges = %d, want every edge kept (only intents are limited)", len(res.Edges))
	}
}

func TestRefusals(t *testing.T) {
	ok := []Event{reading("d1", t0, "temperature", 1)}
	cases := map[string]struct {
		mut  func(*Params)
		want string
		kind string
	}{
		"too many events":      {func(p *Params) { p.MaxEvents = 1; p.Events = append(p.Events, ok[0]) }, "above the limit of 1", "limit"},
		"event limit ceiling":  {func(p *Params) { p.MaxEvents = HardMaxEvents + 1 }, "above the maximum", "limit"},
		"intent limit ceiling": {func(p *Params) { p.MaxIntents = HardMaxIntents + 1 }, "above the maximum", "limit"},
		"no rules":             {func(p *Params) { p.Rules = nil }, "no rules", "input"},
		"no tenant":            {func(p *Params) { p.Tenant = "" }, "tenant", "input"},
		"no device":            {func(p *Params) { p.Events = []Event{{At: t0, Metrics: ok[0].Metrics}} }, "no device", "input"},
		"no time":              {func(p *Params) { p.Events = []Event{{Device: "d", Metrics: ok[0].Metrics}} }, "no occurred time", "input"},
		"no readings":          {func(p *Params) { p.Events = []Event{{Device: "d", At: t0}} }, "no readings", "input"},
		"nameless reading":     {func(p *Params) { p.Events = []Event{{Device: "d", At: t0, Metrics: []Metric{{Value: 1}}}} }, "no name", "input"},
		"non finite": {func(p *Params) {
			p.Events = []Event{{Device: "d", At: t0, Metrics: []Metric{{Name: "m", Value: math.NaN()}}}}
		}, "finite", "input"},
		"advance before":  {func(p *Params) { p.AdvanceTo = t0.Add(-time.Hour) }, "before the last event", "input"},
		"duplicate token": {func(p *Params) { p.Rules = append(p.Rules, p.Rules[0]) }, "share the token", "input"},
		"empty token":     {func(p *Params) { p.Rules[0].Token = "" }, "no token", "input"},
		"bad rule":        {func(p *Params) { p.Rules[0].Def.When.Metric = "" }, `rule "hot"`, "rule"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := params([]Rule{hotRule()}, append([]Event(nil), ok...))
			c.mut(&p)
			_, err := Run(context.Background(), p)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			var le *LimitError
			var ie *InputError
			var re *RuleError
			got := map[string]bool{"limit": errors.As(err, &le), "input": errors.As(err, &ie), "rule": errors.As(err, &re)}
			if !got[c.kind] {
				t.Fatalf("err %T (%v) is not a %s error: %v", err, err, c.kind, got)
			}
		})
	}
}

func TestCancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, params([]Rule{hotRule()}, []Event{reading("d", t0, "temperature", 1)})); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The evaluation applies the platform's rule-duration cap, exactly as publish does: a rule that
// could not be published cannot be tested into passing.
func TestRuleDurationCapIsTheProductionCap(t *testing.T) {
	_, err := Run(context.Background(), params([]Rule{absenceRule(7 * 24 * time.Hour)}, []Event{reading("d1", t0, "temperature", 1)}))
	var re *RuleError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "maximum rule duration") {
		t.Fatalf("err = %v, want the duration cap", err)
	}
}

// A rule that tests geofence containment needs a position and a fence set that an evaluation does not
// supply; it is refused instead of being evaluated against nothing and reported as a rule that never
// fires.
func TestGeofenceRuleIsRefused(t *testing.T) {
	r := Rule{Token: "fence", Def: rules.Rule{Name: "yard", Type: rules.TypeThreshold, When: rules.Condition{CEL: `geo.inFence("yard")`}}}
	_, err := Run(context.Background(), params([]Rule{r}, []Event{reading("d", t0, "temperature", 1)}))
	var re *RuleError
	if !errors.As(err, &re) || re.Token != "fence" || !strings.Contains(err.Error(), "geofence containment") {
		t.Fatalf("err = %v, want a refusal that names the rule and the reason", err)
	}
}

// Edges at one instant list a raise before a resolve, then by device and rule.
func TestEdgeLessOrdersARaiseBeforeAResolveAtOneInstant(t *testing.T) {
	raise := Edge{At: t0, Series: "d", RuleToken: "r", Raise: true}
	resolve := Edge{At: t0, Series: "d", RuleToken: "r"}
	if !edgeLess(raise, resolve) || edgeLess(resolve, raise) {
		t.Fatal("a raise must sort before a resolve at the same instant")
	}
	if edgeLess(raise, raise) {
		t.Fatal("an edge is not less than itself")
	}
	later := Edge{At: t0.Add(time.Second), Series: "a", RuleToken: "a"}
	if !edgeLess(resolve, later) || edgeLess(later, resolve) {
		t.Fatal("time orders before device")
	}
	other := Edge{At: t0, Series: "e", RuleToken: "a"}
	if !edgeLess(raise, other) || edgeLess(other, raise) {
		t.Fatal("device orders before rule")
	}
	rule2 := Edge{At: t0, Series: "d", RuleToken: "s", Raise: true}
	if !edgeLess(raise, rule2) || edgeLess(rule2, raise) {
		t.Fatal("rule orders before direction")
	}
}

// The final advance goes one nanosecond past the instant, so a reading AT advanceTo that a rate rule
// decides only once the frontier is strictly past it is decided.
func TestTheLastReadingIsDecidedByTheAdvance(t *testing.T) {
	rate := Rule{Token: "fast", Def: rules.Rule{Name: "fast", Type: rules.TypeDeltaRate, Severity: rules.SeverityMinor,
		Metric: "temperature", Op: rules.OpGt, Threshold: f64(5)}}
	res := mustRun(t, params([]Rule{rate}, []Event{
		reading("d1", t0, "temperature", 10),
		reading("d1", t0.Add(time.Minute), "temperature", 40),
	}))
	if len(res.Edges) != 1 || !res.Edges[0].Raise || !res.Edges[0].At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("edges = %+v, want the jump at the last reading to raise", res.Edges)
	}
}
