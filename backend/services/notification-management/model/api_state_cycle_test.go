// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// testCycle is the alarm cycle (RaisedTime) the single-cycle state tests run in.
var (
	testCycle    = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	testCycleKey = sql.NullTime{Time: testCycle, Valid: true}
)

// An alarm token outlives a clear: device-management re-raises the SAME token on a fresh
// cycle. These tests pin that the per-alarm state row follows the cycle, and that the
// unordered consumer cannot let one cycle's late events move another's state.

func mustState(t *testing.T, api *Api, ctx context.Context, token string) *NotificationState {
	t.Helper()
	states, err := api.NotificationStatesByAlarmToken(ctx, []string{token})
	if err != nil || len(states) != 1 {
		t.Fatalf("state for %s: got %d rows (err %v)", token, len(states), err)
	}
	return states[0]
}

func openTokens(t *testing.T, api *Api, ctx context.Context) []string {
	t.Helper()
	open, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatalf("open states: %v", err)
	}
	return tokensOf(open)
}

// A notification for a LATER cycle starts it: the previous cycle's acknowledgement, clear
// and escalation tier are gone, and the row is an escalation candidate again.
func TestANewCycleReopensTheState(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	c2 := c1.Add(time.Hour)

	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c1, c1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s := mustState(t, api, ctx, "a")
		if won, err := api.ClaimEscalation(ctx, "a", s.EscalationLevel, s.CycleRaisedAt, c1.Add(time.Duration(i+1)*time.Minute)); err != nil || !won {
			t.Fatalf("cycle one tier %d: won=%v err=%v", i+1, won, err)
		}
	}
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", c1, c1.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, "a", "k", "CRITICAL", c1, c1.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := api.RecordNotification(ctx, "a", "k", "MAJOR", c2, c2); err != nil {
		t.Fatal(err)
	}
	s := mustState(t, api, ctx, "a")
	if s.AcknowledgedAt.Valid || s.ClearedAt.Valid {
		t.Fatalf("cycle two inherited cycle one's stamps: ack=%v clear=%v", s.AcknowledgedAt, s.ClearedAt)
	}
	if s.EscalationLevel != 0 || s.LastEscalatedAt.Valid {
		t.Fatalf("cycle two inherited cycle one's tier: level=%d lastEscalated=%v", s.EscalationLevel, s.LastEscalatedAt)
	}
	if s.NotifyCount != 1 || !s.FirstNotifiedAt.Valid || !s.FirstNotifiedAt.Time.Equal(c2) {
		t.Fatalf("cycle two's notification progress: count=%d first=%v, want 1 and %v", s.NotifyCount, s.FirstNotifiedAt, c2)
	}
	if !s.LastNotifiedAt.Time.Equal(c2) || s.Severity != "MAJOR" {
		t.Fatalf("cycle two's page not recorded: last=%v severity=%q", s.LastNotifiedAt, s.Severity)
	}
	if !s.CycleRaisedAt.Valid || !s.CycleRaisedAt.Time.Equal(c2) {
		t.Fatalf("CycleRaisedAt = %v, want %v", s.CycleRaisedAt, c2)
	}
	if got := openTokens(t, api, ctx); len(got) != 1 || got[0] != "a" {
		t.Fatalf("open candidates = %v, want [a]", got)
	}
}

// The unordered consumer can deliver cycle one's acknowledgement or clear AFTER cycle two
// began. It must not silence cycle two.
func TestAStaleTerminalEventDoesNotSilenceTheNewCycle(t *testing.T) {
	for _, column := range []string{"acknowledged_at", "cleared_at"} {
		t.Run(column, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
			c2 := c1.Add(time.Hour)
			if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c1, c1); err != nil {
				t.Fatal(err)
			}
			if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c2, c2); err != nil {
				t.Fatal(err)
			}
			if err := api.markTerminal(ctx, "a", "k", "CRITICAL", column, c1, c1.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			s := mustState(t, api, ctx, "a")
			if s.AcknowledgedAt.Valid || s.ClearedAt.Valid {
				t.Fatalf("cycle one's late %s stamped cycle two: %+v", column, s)
			}
			if got := openTokens(t, api, ctx); len(got) != 1 {
				t.Fatalf("cycle two must still escalate; open = %v", got)
			}
		})
	}
}

// The other direction: cycle one's RAISED redelivered after cycle two was acknowledged
// must not reopen it, nor count against cycle two.
func TestAStaleRaiseDoesNotReopenTheNewCycle(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	c2 := c1.Add(time.Hour)
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c2, c2); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", c2, c2.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := api.RecordNotification(ctx, "a", "k", "WARNING", c1, c1); err != nil {
		t.Fatal(err)
	}
	s := mustState(t, api, ctx, "a")
	if !s.AcknowledgedAt.Valid {
		t.Fatalf("a stale RAISED reopened an acknowledged cycle: %+v", s)
	}
	if s.NotifyCount != 1 || s.Severity != "CRITICAL" || !s.CycleRaisedAt.Time.Equal(c2) {
		t.Fatalf("a stale RAISED changed cycle two: count=%d severity=%q cycle=%v", s.NotifyCount, s.Severity, s.CycleRaisedAt)
	}
}

// Cycle two's acknowledgement can overtake cycle two's RAISED. The row must move to cycle
// two resolved, so that RAISED, when it lands, finds an acknowledged row — the tombstone
// guarantee, carried across cycles.
func TestANewCyclesTerminalEventAheadOfItsRaiseStillResolvesIt(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	c2 := c1.Add(time.Hour)
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c1, c1); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, "a", "k", "CRITICAL", c1, c1.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ackAt := c2.Add(time.Minute)
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", c2, ackAt); err != nil {
		t.Fatal(err)
	}
	s := mustState(t, api, ctx, "a")
	if s.ClearedAt.Valid || !s.AcknowledgedAt.Valid || !s.AcknowledgedAt.Time.Equal(ackAt) {
		t.Fatalf("cycle two's ack: cleared=%v acked=%v, want cycle one's clear gone and ack at %v",
			s.ClearedAt, s.AcknowledgedAt, ackAt)
	}
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c2, c2); err != nil {
		t.Fatal(err)
	}
	if got := openTokens(t, api, ctx); len(got) != 0 {
		t.Fatalf("cycle two was acknowledged before its RAISED arrived; it must not escalate: open = %v", got)
	}
}

// A tombstone records the cycle it resolves. Cycle two's acknowledgement can be the first
// thing this service ever sees for a token; cycle one's RAISED redelivered after it is
// stale, and must not relabel the row as cycle one — or cycle two's own RAISED would then
// look like a newer cycle and reopen an alarm that is already acknowledged.
func TestATombstoneRecordsItsCycle(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	c2 := c1.Add(time.Hour)
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", c2, c2.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c1, c1); err != nil {
		t.Fatal(err)
	}
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c2, c2); err != nil {
		t.Fatal(err)
	}
	if got := openTokens(t, api, ctx); len(got) != 0 {
		t.Fatalf("cycle two was acknowledged first; it must not escalate: open = %v", got)
	}
}

// The events of ONE cycle disagree below a microsecond: device-management builds the
// RAISED from its in-memory row (nanoseconds) and the acknowledgement from the row read
// back from PostgreSQL (microseconds). They must still be one cycle.
func TestOneCyclesEventsAgreeAcrossTimestampPrecision(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	raisedNs := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	raisedUs := raisedNs.Truncate(time.Microsecond)
	at := time.Now().Add(-time.Hour).UTC()

	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", raisedNs, at); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", raisedUs, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// And a redelivery of the nanosecond RAISED is the same cycle again, not a newer one.
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", raisedNs, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s := mustState(t, api, ctx, "a")
	if !s.AcknowledgedAt.Valid {
		t.Fatalf("the same cycle's RAISED, carried at higher precision, reopened its acknowledgement: %+v", s)
	}
	if s.NotifyCount != 2 || !s.FirstNotifiedAt.Time.Equal(at) {
		t.Fatalf("one cycle was split in two: count=%d first=%v, want 2 and %v", s.NotifyCount, s.FirstNotifiedAt, at)
	}
}

// A row written before cycles were tracked has none; the first event naming a cycle
// adopts it WITHOUT a reset (a reset could let a stale RAISED reopen an acknowledged
// alarm), and the cycle after that is ordered normally.
func TestARowWithNoCycleAdoptsTheFirstOneNamed(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	c2 := c1.Add(time.Hour)
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", time.Time{}, c1); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", time.Time{}, c1.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := mustState(t, api, ctx, "a"); s.CycleRaisedAt.Valid {
		t.Fatalf("an event naming no cycle recorded one: %v", s.CycleRaisedAt)
	}
	// A terminal event that stamps nothing still records the cycle it adopts.
	if err := api.MarkAcknowledged(ctx, "a", "k", "CRITICAL", c1, c1.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s := mustState(t, api, ctx, "a")
	if !s.AcknowledgedAt.Valid || !s.AcknowledgedAt.Time.Equal(c1.Add(time.Minute)) {
		t.Fatalf("adoption reset or moved the acknowledgement: %v", s.AcknowledgedAt)
	}
	if !s.CycleRaisedAt.Valid || !s.CycleRaisedAt.Time.Equal(c1) {
		t.Fatalf("CycleRaisedAt = %v, want adopted %v", s.CycleRaisedAt, c1)
	}
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c2, c2); err != nil {
		t.Fatal(err)
	}
	if got := openTokens(t, api, ctx); len(got) != 1 {
		t.Fatalf("the cycle after adoption must reopen; open = %v", got)
	}
}

// The escalation claim is pinned to the cycle the scheduler read: a claim planned against
// cycle one must lose once cycle two has begun, even though cycle two is back at tier 0.
func TestAnEscalationClaimForAnEarlierCycleLoses(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	c1 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	c2 := c1.Add(time.Hour)
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c1, c1); err != nil {
		t.Fatal(err)
	}
	read := mustState(t, api, ctx, "a")
	if err := api.MarkCleared(ctx, "a", "k", "CRITICAL", c1, c1.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := api.RecordNotification(ctx, "a", "k", "CRITICAL", c2, c2); err != nil {
		t.Fatal(err)
	}
	won, err := api.ClaimEscalation(ctx, "a", read.EscalationLevel, read.CycleRaisedAt, c2.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("a claim planned against cycle one escalated cycle two")
	}
	// The counterweight: the same claim against the cycle the row is in wins.
	won, err = api.ClaimEscalation(ctx, "a", 0, mustState(t, api, ctx, "a").CycleRaisedAt, c2.Add(time.Second))
	if err != nil || !won {
		t.Fatalf("a claim against the current cycle must win: won=%v err=%v", won, err)
	}
}
