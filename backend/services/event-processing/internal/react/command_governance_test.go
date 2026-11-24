// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

func governedCommandRule() rules.Rule {
	r := sendCmdRule("reboot", "")
	r.When = rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: ptrF(30)}
	return r
}

func TestCommandUsesCommandGate(t *testing.T) {
	sink := &fakeSink{}
	gate := &fakeGate{}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, nil, newFakeMetrics(), WithCommandRateGate(gate))
	res := d.Dispatch(context.Background(), evt())
	if len(sink.sent) != 0 || len(res.Shed) != 1 || len(gate.charged) != 1 || res.Outcome != Done {
		t.Fatalf("command bypassed command gate: sends=%d result=%+v charges=%d", len(sink.sent), res, len(gate.charged))
	}
}

func TestCommandLoopBound(t *testing.T) {
	sink := &fakeSink{}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, nil, newFakeMetrics())
	ev := evt()
	for i := 0; i < 121; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		d.Dispatch(context.Background(), ev)
	}
	if len(sink.sent) != 120 {
		t.Fatalf("loop sent %d commands, want 120", len(sink.sent))
	}
}

// A detection re-derived by startup replay may be the only delivery of a command a
// crash interrupted (deliver-before-checkpoint), so it is SENT, with the same
// idempotency token as the live dispatch it may duplicate. It is not a fresh
// trigger, so it neither charges the outbound gate (closed here, to prove it is not
// asked) nor spends the live loop allowance.
func TestReplayedCommandIsSentWithoutSpendingEitherBudget(t *testing.T) {
	sink := &fakeSink{}
	connectorGate := &fakeGate{admit: false}
	gate := &fakeGate{admit: false}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, connectorGate, newFakeMetrics(), WithCommandRateGate(gate))
	live := evt()
	var ev runtime.DerivedEvent
	b, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	wire["replayed"] = true
	if b, err = json.Marshal(wire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 121; i++ {
		if res := d.Dispatch(context.Background(), ev); res.Outcome != Done || len(res.Shed) != 0 {
			t.Fatalf("replayed command refused: %+v", res)
		}
	}
	if len(sink.sent) != 121 || len(gate.charged) != 0 || len(connectorGate.charged) != 0 || len(d.commandLoop.keys) != 0 {
		t.Fatalf("replay sent=%d charged=%d loopKeys=%d; want 121 sent, nothing charged", len(sink.sent), len(gate.charged), len(d.commandLoop.keys))
	}
	if sink.sent[0].Token != idempotencyToken(live, governedCommandRule().Actions[0]) {
		t.Fatal("a replayed command must carry the live dispatch's idempotency token")
	}
	gate.admit = true
	for i := 0; i < 120; i++ {
		live.OccurredTime = live.OccurredTime.Add(time.Nanosecond)
		d.Dispatch(context.Background(), live)
	}
	if len(sink.sent) != 241 {
		t.Fatalf("replay spent the live loop budget: %d live sends of 120", len(sink.sent)-121)
	}
}

// A looping rule's refused attempts must not keep drawing on the tenant's outbound
// budget: the loop guard is asked before the gate.
func TestLoopRefusalDoesNotSpendTenantBudget(t *testing.T) {
	sink := &fakeSink{}
	gate := &fakeGate{admit: true}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, nil, newFakeMetrics(), WithCommandRateGate(gate))
	ev := evt()
	for i := 0; i < commandLoopSends+50; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		d.Dispatch(context.Background(), ev)
	}
	if len(sink.sent) != commandLoopSends || len(gate.charged) != commandLoopSends {
		t.Fatalf("sent=%d charged=%d; a loop-refused attempt must not charge the tenant's outbound budget", len(sink.sent), len(gate.charged))
	}
}

// An attempt the rate gate refuses was never sent, so it gives its loop allowance
// back and holds no slot of the bounded guard.
func TestRateRefusalRefundsLoopAllowance(t *testing.T) {
	sink := &fakeSink{}
	gate := &fakeGate{admit: false}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, nil, newFakeMetrics(), WithCommandRateGate(gate))
	ev := evt()
	for i := 0; i < 50; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		d.Dispatch(context.Background(), ev)
	}
	if len(sink.sent) != 0 || len(d.commandLoop.keys) != 0 {
		t.Fatalf("rate-refused attempts left loop state: sent=%d keys=%d", len(sink.sent), len(d.commandLoop.keys))
	}
	gate.admit = true
	for i := 0; i < commandLoopSends; i++ {
		ev.OccurredTime = ev.OccurredTime.Add(time.Nanosecond)
		d.Dispatch(context.Background(), ev)
	}
	if len(sink.sent) != commandLoopSends {
		t.Fatalf("rate refusals spent the loop allowance: %d of %d sent", len(sink.sent), commandLoopSends)
	}
}

func TestCommandLoopWindowExpiresWithoutClockRewindOrRepublishBypass(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	for i := 0; i < commandLoopSends; i++ {
		if code := g.admit("acme", "acme/p@1/r1", "d", at); code != "" {
			t.Fatalf("ordinary send %d refused: %s", i, code)
		}
	}
	for _, clock := range []time.Time{at, at.Add(-time.Hour), at.Add(commandLoopWindow - time.Nanosecond)} {
		if code := g.admit("acme", "acme/p@2/r1", "d", clock); code != CommandLoop {
			t.Fatalf("clock rewind or profile republish reset the logical rule's allowance: %s", code)
		}
	}
	if code := g.admit("acme", "acme/p@2/r1", "d", at.Add(commandLoopWindow)); code != "" {
		t.Fatalf("expired loop window refused an ordinary command: %s", code)
	}
}

func TestCommandLoopStateIsBoundedWithoutEvictingLiveKeys(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	for i := 0; i < commandLoopKeys; i++ {
		g.keys[commandLoopKey{tenant: "acme", device: fmt.Sprint(i)}] = commandLoopEntry{until: at.Add(commandLoopWindow), sends: commandLoopSends}
	}
	if code := g.admit("acme", "acme/p@1/r1", "new", at); code != CommandGuardFull || len(g.keys) != commandLoopKeys {
		t.Fatalf("capacity churn evicted live safety state: %s, %d keys", code, len(g.keys))
	}
	if code := g.admit("acme", "acme/p@1/r1", "new", at.Add(commandLoopWindow)); code != "" || len(g.keys) != 1 {
		t.Fatalf("expired safety state was not reclaimed: %s, %d keys", code, len(g.keys))
	}
}

func TestCommandSimulationDoesNotSpendEitherBudget(t *testing.T) {
	connectorGate := &fakeGate{admit: true}
	gate := &fakeGate{admit: true}
	recording := &fakeSink{}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, recording, nil, nil, connectorGate, newFakeMetrics(), WithCommandRateGate(gate), WithCommandSimulation())
	for i := 0; i < 121; i++ {
		d.Dispatch(context.Background(), evt())
	}
	if len(recording.sent) != 121 || len(gate.charged) != 0 || len(connectorGate.charged) != 0 || len(d.commandLoop.keys) != 0 {
		t.Fatal("simulation spent governance tokens or recorded loop state")
	}
}
