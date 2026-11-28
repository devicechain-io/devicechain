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

// A backlog drained after an outage is metered on the timeline it was produced on.
// Six minutes of compliant traffic, each command to its own device, is more rule and
// device pairs than a tenant's share holds in one window, and a device commanded
// every two seconds receives more than a loop's allowance in six minutes. Charged at
// drain time both would be refused; on their own timeline neither is. A device that
// really is looping inside the backlog is still cut.
func TestCommandBacklogIsGuardedOnItsOwnTimeline(t *testing.T) {
	sink := &fakeSink{}
	produced := evt().OccurredTime
	drain := produced.Add(time.Hour)
	d := NewDispatcher(fakeResolver{rule: governedCommandRule(), found: true}, sink, nil, nil, nil, newFakeMetrics(),
		WithCommandRateGate(&fakeGate{admit: true}), WithClock(func() time.Time { return drain }))
	refused := map[string]map[string]int{}
	dispatch := func(device string, at time.Time) {
		ev := evt()
		ev.Series, ev.OccurredTime, ev.TriggeredAt = device, at, at
		for _, shed := range d.Dispatch(context.Background(), ev).Shed {
			if refused[shed.Code] == nil {
				refused[shed.Code] = map[string]int{}
			}
			refused[shed.Code][device]++
		}
	}
	const minutes, perSecond = 6, 60
	loopStart := produced.Add(3 * time.Minute)
	for s := 0; s < minutes*60; s++ {
		at := produced.Add(time.Duration(s) * time.Second)
		for i := 0; i < perSecond; i++ {
			dispatch(fmt.Sprintf("d-%d-%d", s, i), at)
		}
		if s%2 == 0 {
			dispatch("steady", at)
		}
		// 200 commands to one device within ten seconds of the backlog: a loop.
		if !at.Before(loopStart) && at.Before(loopStart.Add(10*time.Second)) {
			for i := 0; i < 20; i++ {
				dispatch("looping", at.Add(time.Duration(i)*time.Millisecond))
			}
		}
	}
	if n := len(refused[CommandGuardFull]); n != 0 {
		t.Fatalf("a compliant backlog filled the guard: %d devices refused with %s", n, CommandGuardFull)
	}
	if refused[CommandLoop]["steady"] != 0 {
		t.Fatalf("a device commanded every two seconds read as a loop: %d refusals", refused[CommandLoop]["steady"])
	}
	if got := refused[CommandLoop]["looping"]; got != 200-commandLoopSends || len(refused[CommandLoop]) != 1 {
		t.Fatalf("the loop inside the backlog was not cut at its allowance: %v", refused[CommandLoop])
	}
}

// Trigger times arrive out of order (redeliveries, lanes, groups draining together),
// but the guard opens its windows at its mark, so the expiry queue stays in order and
// a window that has ended is never hidden behind one that has not.
func TestCommandLoopWindowsOpenInOrderUnderOutOfOrderTimes(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	for i, offset := range []time.Duration{30 * time.Second, 0, 10 * time.Second, -time.Hour, 45 * time.Second, 5 * time.Second} {
		if code := g.admit("acme", "acme/p@1/r1", fmt.Sprint(i), at.Add(offset)); code != "" {
			t.Fatalf("key %d refused: %s", i, code)
		}
	}
	for i := g.head + 1; i < len(g.expiry); i++ {
		if g.expiry[i].until.Before(g.expiry[i-1].until) {
			t.Fatalf("expiry queue out of order at %d: %v before %v", i, g.expiry[i].until, g.expiry[i-1].until)
		}
	}
}

// A trigger time is never trusted past the dispatch clock: a detection stamped in the
// future is metered at the dispatch time, so it cannot open windows ahead of it.
func TestCommandLoopNeverMetersPastTheDispatchClock(t *testing.T) {
	now := evt().OccurredTime
	ev := evt()
	ev.TriggeredAt = now.Add(time.Hour)
	if got := loopMeteringTime(ev, now); !got.Equal(now) {
		t.Fatalf("a future trigger time was metered at %v, want the dispatch time %v", got, now)
	}
	ev.TriggeredAt = time.Time{}
	if got := loopMeteringTime(ev, now); !got.Equal(now) {
		t.Fatalf("a missing trigger time was metered at %v, want the dispatch time", got)
	}
	ev.TriggeredAt = now.Add(-time.Minute)
	if got := loopMeteringTime(ev, now); !got.Equal(ev.TriggeredAt) {
		t.Fatalf("a past trigger time was metered at %v, want %v", got, ev.TriggeredAt)
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

// fillLoopKeys opens n new keys for tenant at at, each admitted.
func fillLoopKeys(t *testing.T, g *commandLoopGuard, tenant string, n int, at time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		if code := g.admit(tenant, tenant+"/p@1/r1", fmt.Sprint(i), at); code != "" {
			t.Fatalf("%s key %d of %d refused: %s", tenant, i, n, code)
		}
	}
}

func TestCommandLoopStateIsBoundedWithoutEvictingLiveKeys(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	fillLoopKeys(t, g, "acme", commandLoopTenantKeys, at)
	if code := g.admit("acme", "acme/p@1/r1", "new", at); code != CommandGuardFull || len(g.keys) != commandLoopTenantKeys {
		t.Fatalf("capacity churn evicted live safety state: %s, %d keys", code, len(g.keys))
	}
	if code := g.admit("acme", "acme/p@1/r1", "new", at.Add(commandLoopWindow)); code != "" || len(g.keys) != 1 {
		t.Fatalf("expired safety state was not reclaimed: %s, %d keys", code, len(g.keys))
	}
}

// The table serves every tenant of the process. One tenant using all of its share
// refuses only its own new keys: another tenant's commands are still admitted.
func TestCommandLoopTenantSaturatingItsShareDoesNotRefuseAnother(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	fillLoopKeys(t, g, "noisy", commandLoopTenantKeys, at)
	if code := g.admit("noisy", "noisy/p@1/r1", "one-more", at); code != CommandGuardFull {
		t.Fatalf("a tenant past its share was admitted: %q", code)
	}
	if code := g.admit("quiet", "quiet/p@1/r1", "d", at); code != "" {
		t.Fatalf("another tenant's saturation refused this tenant's command: %s", code)
	}
}

// The share is a fraction of the table, which stays bounded as a whole: once enough
// tenants fill their shares to fill it, a new key of any tenant is refused.
func TestCommandLoopTableBoundHoldsAcrossTenants(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	for i := 0; i < commandLoopKeys/commandLoopTenantKeys; i++ {
		fillLoopKeys(t, g, fmt.Sprintf("t%d", i), commandLoopTenantKeys, at)
	}
	if code := g.admit("late", "late/p@1/r1", "d", at); code != CommandGuardFull || len(g.keys) != commandLoopKeys {
		t.Fatalf("the table grew past its bound: %q, %d keys of %d", code, len(g.keys), commandLoopKeys)
	}
}

// A key whose window has ended holds no slot: it is released before a new key is
// refused, not at the next periodic sweep, and its tenant's share is returned.
func TestCommandLoopExpiredKeysAreEvictedBeforeRefusing(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	half := commandLoopTenantKeys / 2
	fillLoopKeys(t, g, "acme", half, at)
	for i := 0; i < commandLoopTenantKeys-half; i++ {
		if code := g.admit("acme", "acme/p@1/r2", fmt.Sprint(i), at.Add(commandLoopWindow/2)); code != "" {
			t.Fatalf("second half key %d refused: %s", i, code)
		}
	}
	if code := g.admit("acme", "acme/p@1/r3", "d", at.Add(commandLoopWindow-time.Nanosecond)); code != CommandGuardFull {
		t.Fatalf("a full share admitted a new key before any window ended: %q", code)
	}
	// The first half's windows have ended; the second half's have not.
	if code := g.admit("acme", "acme/p@1/r3", "d", at.Add(commandLoopWindow)); code != "" {
		t.Fatalf("an expired key still held its slot: %s", code)
	}
	if want := commandLoopTenantKeys - half + 1; len(g.keys) != want || g.tenants["acme"] != want {
		t.Fatalf("after expiry: %d keys, tenant holds %d; want %d live", len(g.keys), g.tenants["acme"], want)
	}
	// A live key's allowance survived the eviction around it.
	if entry := g.keys[commandLoopKey{tenant: "acme", rule: stableContributorID("acme/p@1/r2"), device: "0"}]; entry.sends != 1 {
		t.Fatalf("a live key was evicted or reset: %+v", entry)
	}
	// Every remaining window ends; the whole share is returned.
	if code := g.admit("acme", "acme/p@1/r4", "d", at.Add(2*commandLoopWindow)); code != "" || len(g.keys) != 1 || g.tenants["acme"] != 1 {
		t.Fatalf("expired keys kept their slots: %q, %d keys, tenant holds %d", code, len(g.keys), g.tenants["acme"])
	}
}

// A refunded attempt returns its tenant's slot, and the record its window left in the
// expiry queue cannot end a later window of the same key.
func TestCommandLoopRefundReturnsTheTenantSlot(t *testing.T) {
	g := newCommandLoopGuard()
	at := evt().OccurredTime
	if code := g.admit("acme", "acme/p@1/r1", "d", at); code != "" {
		t.Fatal(code)
	}
	g.refund("acme", "acme/p@1/r1", "d")
	if len(g.keys) != 0 || len(g.tenants) != 0 {
		t.Fatalf("refund kept state: %d keys, tenants %v", len(g.keys), g.tenants)
	}
	later := at.Add(commandLoopWindow / 2)
	for i := 0; i < commandLoopSends; i++ {
		if code := g.admit("acme", "acme/p@1/r1", "d", later); code != "" {
			t.Fatalf("send %d refused: %s", i, code)
		}
	}
	// The refunded window's record expires here; the live window must not end with it.
	if code := g.admit("acme", "acme/p@1/r1", "d", at.Add(commandLoopWindow)); code != CommandLoop {
		t.Fatalf("a stale expiry record reset a live loop window: %q", code)
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
