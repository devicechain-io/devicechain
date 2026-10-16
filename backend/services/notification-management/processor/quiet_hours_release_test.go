// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// funcAdapter delivers by calling fn, so a test can act in the middle of a send.
type funcAdapter struct {
	calls int
	fn    func(call int) error
}

func (f *funcAdapter) Deliver(_ context.Context, _ *model.NotificationChannel, _ string, _ []string, _ *RenderedNotification) error {
	f.calls++
	return f.fn(f.calls)
}

// holdOne dispatches one MAJOR raise inside quiet hours and checks it was held.
func holdOne(t *testing.T, n *PolicyNotifier, api *model.Api, ctx context.Context, at time.Time) {
	t.Helper()
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", at)); err != nil {
		t.Fatal(err)
	}
	if held := heldRows(t, api, ctx); len(held) != 1 {
		t.Fatalf("held %d notification(s), want 1", len(held))
	}
}

// 🔴 A held notification is the ONLY page about its alarm. A sender that dies between
// taking it and sending it (a rolling deploy as the night's backlog releases) must leave it
// to be sent again, not take it with it.
func TestAReleaseWhoseSenderDiesIsSentLater(t *testing.T) {
	clock := insideQuiet
	n, api, _, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	holdOne(t, n, api, ctx, clock)

	dies := &funcAdapter{fn: func(int) error { panic("the pod is gone") }}
	n.adapters[model.ChannelTypeSMTP] = dies
	clock = quietEnds
	func() {
		defer func() { _ = recover() }()
		_ = n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds)
	}()
	if dies.calls != 1 {
		t.Fatalf("the dying sender was called %d time(s), want 1", dies.calls)
	}

	// Another replica, after the dead one's lease has run out.
	works := &fakeAdapter{}
	n.adapters[model.ChannelTypeSMTP] = works
	later := quietEnds.Add(n.releaseLease() + time.Second)
	clock = later
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), later); err != nil {
		t.Fatal(err)
	}
	if works.calls != 1 {
		t.Fatalf("the page whose sender died was sent %d time(s) afterwards, want 1", works.calls)
	}
	if held := heldRows(t, api, ctx); len(held) != 0 {
		t.Fatalf("%d row(s) left after the release", len(held))
	}
}

// While one replica is sending a release, another replica's pass must not send it too.
func TestAReleaseInFlightIsNotSentByAnotherReplica(t *testing.T) {
	clock := insideQuiet
	n, api, _, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	holdOne(t, n, api, ctx, clock)
	clock = quietEnds

	other := &funcAdapter{fn: func(int) error { return nil }}
	replica2 := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: other})
	replica2.api, replica2.store, replica2.now = api, &fakeSecretStore{}, n.now

	first := &funcAdapter{fn: func(call int) error {
		// Mid-send: the other replica's pass runs, a little later.
		if err := replica2.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds.Add(30*time.Second)); err != nil {
			t.Errorf("replica 2: %v", err)
		}
		return nil
	}}
	n.adapters[model.ChannelTypeSMTP] = first
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
		t.Fatal(err)
	}
	if first.calls != 1 || other.calls != 0 {
		t.Fatalf("pages: replica 1 = %d, replica 2 = %d; want 1 and 0", first.calls, other.calls)
	}
}

// An alarm token outlives a clear. A SECOND cycle cleared (or acknowledged) while held must
// be dropped like the first: the state's terminal stamps are the first cycle's and do not
// move, so they cannot show it.
func TestASecondCycleResolvedWhileHeldIsNotPaged(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind dmmodel.AlarmEventType
	}{
		{"cleared", dmmodel.AlarmEventCleared},
		{"acknowledged", dmmodel.AlarmEventAcknowledged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := insideQuiet
			n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
			// Cycle 1, resolved an hour before.
			if err := n.Notify(ctx, eventAt(tc.kind, "MAJOR", clock.Add(-time.Hour))); err != nil {
				t.Fatal(err)
			}
			// Cycle 2 is held, then resolved too.
			holdOne(t, n, api, ctx, clock)
			if err := n.Notify(ctx, eventAt(tc.kind, "MAJOR", clock.Add(10*time.Minute))); err != nil {
				t.Fatal(err)
			}
			if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
				t.Fatal(err)
			}
			if fa.calls != 0 {
				t.Fatalf("paged about a second cycle %s while held: calls = %d", tc.name, fa.calls)
			}
			if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietDroppedResolved)); got != 1 {
				t.Fatalf("dropped_resolved = %v, want 1", got)
			}
		})
	}
}

// A resolution only answers what was held BEFORE it: a later cycle's held page stays.
func TestAResolutionDoesNotDropALaterCyclesHeldPage(t *testing.T) {
	clock := insideQuiet
	n, api, _, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	holdOne(t, n, api, ctx, clock)
	// A clear that occurred BEFORE the held raise, delivered late (the pool is unordered).
	if err := n.Notify(ctx, eventAt(dmmodel.AlarmEventCleared, "MAJOR", clock.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if held := heldRows(t, api, ctx); len(held) != 1 {
		t.Fatalf("an earlier clear dropped a later held raise: held = %d", len(held))
	}
}

// When quiet hours end and the alarm pages afresh before the scheduler's next pass, the
// held page is answered: no second page about the older event, and the escalation state
// keeps the newer severity.
func TestAFreshPageSupersedesWhatWasHeld(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	holdOne(t, n, api, ctx, clock)

	clock = quietEnds.Add(5 * time.Second)
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventEscalated, "CRITICAL", clock)); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("the fresh escalation paged %d time(s), want 1", fa.calls)
	}
	clock = quietEnds.Add(time.Minute)
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("the older held page went out after the fresh one: calls = %d", fa.calls)
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	if len(states) != 1 || states[0].Severity != "CRITICAL" {
		t.Fatalf("state = %+v, want severity CRITICAL", states)
	}
	if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietSuperseded)); got != 1 {
		t.Fatalf("superseded = %v, want 1", got)
	}
}

// A failed release waits longer each time, rather than spending its attempts on five
// consecutive scheduler ticks.
func TestAFailedReleaseBacksOff(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	fa.failTimes = 1 << 30
	n.attempts = 1
	holdOne(t, n, api, ctx, clock)
	clock = quietEnds
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err == nil {
		t.Fatal("a release that reached nobody reported success")
	}
	held := heldRows(t, api, ctx)
	if len(held) != 1 || !held[0].ReleaseAt.Equal(quietEnds.Add(time.Minute)) || held[0].Attempts != 1 {
		t.Fatalf("held = %+v, want one row due a minute later with attempts 1", held)
	}
	clock = quietEnds.Add(time.Minute)
	_ = n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock)
	held = heldRows(t, api, ctx)
	if len(held) != 1 || !held[0].ReleaseAt.Equal(clock.Add(2*time.Minute)) {
		t.Fatalf("held = %+v, want the second retry two minutes later", held)
	}
}

// The last failed release is DEAD-LETTERED, like an ordinary alarm that reached nobody: a
// counter alone is not a record of a page that was never sent.
func TestAnExhaustedReleaseIsDeadLettered(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	fa.failTimes = 1 << 30
	n.attempts = 1
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "notification-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	dead := &deadRecorder{}
	n.UseDeadLetters(deadletter.NewProducer(ms).NewSink(dead))
	holdOne(t, n, api, ctx, clock)

	at := quietEnds
	for pass := 1; pass <= maxReleaseAttempts; pass++ {
		clock = at
		_ = n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), at)
		if pass < maxReleaseAttempts && len(dead.msgs) != 0 {
			t.Fatalf("pass %d dead-lettered before the attempts ran out", pass)
		}
		at = at.Add(releaseRetryDelay(pass))
	}
	if len(dead.msgs) != 1 {
		t.Fatalf("dead letters = %d, want 1", len(dead.msgs))
	}
	if dead.tenants[0] == "" {
		t.Fatal("the dead letter was written without the tenant")
	}
	var env deadletter.Envelope
	if err := json.Unmarshal(dead.msgs[0].Value, &env); err != nil {
		t.Fatal(err)
	}
	if env.Reference != "a1" || env.Reason != deadletter.ReasonExhausted || env.Kind != deadletter.KindNotification {
		t.Fatalf("dead letter = %+v", env)
	}
	if len(heldRows(t, api, ctx)) != 0 {
		t.Fatal("the exhausted row is still held")
	}
}
