// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A page held through a clear and re-raise belongs to the cycle it was raised in. These
// pin what its release does to the alarm's notification state, which by then may be on
// another cycle.

// A held page of a NEW cycle, released onto a row the previous cycle left cleared, starts
// that cycle: the row reopens and escalates from the release, as it would had the page
// gone out when it was raised.
func TestAReleasedPageOfANewCycleStartsThatCycle(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	c1 := clock.Add(-3 * time.Hour)
	if err := api.RecordNotification(ctx, "a1", "k", "MAJOR", c1, c1); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, "a1", "k", "MAJOR", c1, c1.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	holdOne(t, n, api, ctx, clock) // cycle two, raised at clock
	clock = quietEnds
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("released %d time(s), want 1", fa.calls)
	}
	states, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %v (err %v)", states, err)
	}
	s := states[0]
	if !s.CycleRaisedAt.Valid || !s.CycleRaisedAt.Time.Equal(insideQuiet) {
		t.Fatalf("CycleRaisedAt = %v, want the held page's cycle %v", s.CycleRaisedAt, insideQuiet)
	}
	if s.ClearedAt.Valid || s.NotifyCount != 1 || !s.FirstNotifiedAt.Time.Equal(quietEnds) {
		t.Fatalf("the released cycle inherited cycle one: clear=%v count=%d first=%v",
			s.ClearedAt, s.NotifyCount, s.FirstNotifiedAt)
	}
	open, err := api.OpenNotificationStates(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("the released cycle must escalate; open = %d (err %v)", len(open), err)
	}
}

// A held page of an EARLIER cycle than the one the state is on is about an alarm that has
// since cleared — it could not have re-raised otherwise — so it is dropped as resolved,
// and the current cycle's state is left exactly as it was.
func TestAHeldPageOfAnEarlierCycleIsDroppedAsResolved(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	holdOne(t, n, api, ctx, clock) // cycle one, raised at clock
	// Cycle two, paged by another path; cycle one's CLEARED has not arrived.
	c2 := clock.Add(time.Hour)
	if err := api.RecordNotification(ctx, "a1", "k", "CRITICAL", c2, c2); err != nil {
		t.Fatal(err)
	}
	clock = quietEnds
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 0 {
		t.Fatalf("paged %d time(s) about a cycle that has since cleared", fa.calls)
	}
	if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietDroppedResolved)); got != 1 {
		t.Fatalf("dropped_resolved = %v, want 1", got)
	}
	if held := heldRows(t, api, ctx); len(held) != 0 {
		t.Fatalf("%d row(s) left behind", len(held))
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	if len(states) != 1 {
		t.Fatalf("states = %v", states)
	}
	s := states[0]
	if s.NotifyCount != 1 || s.Severity != "CRITICAL" || !s.CycleRaisedAt.Time.Equal(c2) ||
		!s.FirstNotifiedAt.Time.Equal(c2) {
		t.Fatalf("the earlier cycle's page moved the current cycle: count=%d severity=%q cycle=%v first=%v",
			s.NotifyCount, s.Severity, s.CycleRaisedAt, s.FirstNotifiedAt)
	}
}

// The counterweight: a held page of the cycle the state is ON is released as usual.
func TestAHeldPageOfTheCurrentCycleIsReleased(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	// The state is on the held page's own cycle, at sub-microsecond precision.
	raised := clock.Add(123 * time.Nanosecond)
	if err := api.RecordNotification(ctx, "a1", "k", "MAJOR", raised.Truncate(time.Microsecond), clock.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ev := eventAt(dmmodel.AlarmEventEscalated, "MAJOR", raised)
	if err := n.dispatch(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if held := heldRows(t, api, ctx); len(held) != 1 {
		t.Fatalf("held %d, want 1", len(held))
	}
	clock = quietEnds
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("released %d time(s), want 1", fa.calls)
	}
}
