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

func TestCommandUsesOutboundGate(t *testing.T) {
	sink := &fakeSink{}
	gate := &fakeGate{}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, gate, newFakeMetrics())
	res := d.Dispatch(context.Background(), evt())
	if len(sink.sent) != 0 || len(res.Shed) != 1 || len(gate.charged) != 1 || res.Outcome != Done {
		t.Fatalf("command bypassed outbound gate: sends=%d result=%+v charges=%d", len(sink.sent), res, len(gate.charged))
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

func TestCommandReplayDoesNotSendOrCount(t *testing.T) {
	sink := &fakeSink{}
	gate := &fakeGate{admit: true}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, gate, newFakeMetrics())
	var ev runtime.DerivedEvent
	if err := json.Unmarshal([]byte(`{"tenant":"acme","ruleId":"acme/p@1/r1","series":"device-1","replayed":true}`), &ev); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 121; i++ {
		d.Dispatch(context.Background(), ev)
	}
	if len(sink.sent) != 0 || len(gate.charged) != 0 {
		t.Fatalf("replay sent=%d charged=%d", len(sink.sent), len(gate.charged))
	}
	for i := 0; i < 120; i++ {
		d.Dispatch(context.Background(), evt())
	}
	if len(sink.sent) != 120 {
		t.Fatalf("replay spent the live loop budget: %d sends", len(sink.sent))
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
	gate := &fakeGate{admit: true}
	recording := &fakeSink{}
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, recording, nil, nil, gate, newFakeMetrics(), WithCommandSimulation())
	for i := 0; i < 121; i++ {
		d.Dispatch(context.Background(), evt())
	}
	if len(recording.sent) != 121 || len(gate.charged) != 0 || len(d.commandLoop.keys) != 0 {
		t.Fatal("simulation spent governance tokens or recorded loop state")
	}
}
