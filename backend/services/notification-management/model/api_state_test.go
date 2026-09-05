// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// RecordNotification creates a state row on first sight of an alarm and advances the
// notified stamps + count on subsequent notifications.
func TestRecordNotificationUpsert(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	t0 := time.Now().Add(-time.Minute).UTC()
	if err := api.RecordNotification(ctx, "alarm-1", "temperature.high", "CRITICAL", t0); err != nil {
		t.Fatalf("first record: %v", err)
	}
	states, err := api.NotificationStatesByAlarmToken(ctx, []string{"alarm-1"})
	if err != nil || len(states) != 1 {
		t.Fatalf("expected 1 state, got %d (err %v)", len(states), err)
	}
	s := states[0]
	if s.NotifyCount != 1 || !s.FirstNotifiedAt.Valid || !s.LastNotifiedAt.Valid {
		t.Fatalf("unexpected first state: %+v", s)
	}
	if s.Severity != "CRITICAL" {
		t.Fatalf("severity = %q, want CRITICAL", s.Severity)
	}

	t1 := t0.Add(30 * time.Second)
	if err := api.RecordNotification(ctx, "alarm-1", "temperature.high", "MAJOR", t1); err != nil {
		t.Fatalf("second record: %v", err)
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"alarm-1"})
	s = states[0]
	if s.NotifyCount != 2 {
		t.Fatalf("NotifyCount = %d, want 2", s.NotifyCount)
	}
	if !s.FirstNotifiedAt.Time.Equal(t0) {
		t.Fatalf("FirstNotifiedAt moved: %v != %v", s.FirstNotifiedAt.Time, t0)
	}
	if !s.LastNotifiedAt.Time.Equal(t1) {
		t.Fatalf("LastNotifiedAt = %v, want %v", s.LastNotifiedAt.Time, t1)
	}
	if s.Severity != "MAJOR" {
		t.Fatalf("severity not advanced: %q", s.Severity)
	}
}

// Ack/clear stamps are idempotent update-if-exists on an existing row: a re-stamp must
// not move the first-seen resolution time.
func TestMarkAcknowledgedAndClearedIdempotent(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	now := time.Now().UTC()
	if err := api.RecordNotification(ctx, "alarm-2", "k", "CRITICAL", now); err != nil {
		t.Fatalf("record: %v", err)
	}
	ackAt := now.Add(time.Minute)
	if err := api.MarkAcknowledged(ctx, "alarm-2", "k", "CRITICAL", ackAt); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// A second ack must not overwrite the first stamp.
	if err := api.MarkAcknowledged(ctx, "alarm-2", "k", "CRITICAL", ackAt.Add(time.Hour)); err != nil {
		t.Fatalf("re-ack: %v", err)
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"alarm-2"})
	if !states[0].AcknowledgedAt.Valid || !states[0].AcknowledgedAt.Time.Equal(ackAt) {
		t.Fatalf("AcknowledgedAt = %+v, want %v", states[0].AcknowledgedAt, ackAt)
	}

	clearAt := now.Add(2 * time.Minute)
	if err := api.MarkCleared(ctx, "alarm-2", "k", "CRITICAL", clearAt); err != nil {
		t.Fatalf("clear: %v", err)
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"alarm-2"})
	if !states[0].ClearedAt.Valid || !states[0].ClearedAt.Time.Equal(clearAt) {
		t.Fatalf("ClearedAt = %+v, want %v", states[0].ClearedAt, clearAt)
	}
}

// MarkCleared/MarkAcknowledged UPSERT a resolved tombstone when no row exists yet, so an
// out-of-order terminal event (processed before the RAISED that would create the row)
// leaves a row the escalation scheduler treats as resolved — a later RAISED cannot
// resurrect it into an escalating open row.
func TestMarkTerminalUpsertsTombstone(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	now := time.Now().UTC()

	// Clear arrives with no prior row: a cleared tombstone is created.
	if err := api.MarkCleared(ctx, "raced", "temp.high", "CRITICAL", now); err != nil {
		t.Fatalf("clear tombstone: %v", err)
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"raced"})
	if len(states) != 1 {
		t.Fatalf("expected tombstone row, got %d", len(states))
	}
	if !states[0].ClearedAt.Valid || states[0].AlarmKey != "temp.high" || states[0].Severity != "CRITICAL" {
		t.Fatalf("unexpected tombstone: %+v", states[0])
	}

	// A later RAISED bumps NotifyCount but must preserve the cleared stamp (stays resolved).
	if err := api.RecordNotification(ctx, "raced", "temp.high", "CRITICAL", now.Add(time.Second)); err != nil {
		t.Fatalf("late raise: %v", err)
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"raced"})
	if !states[0].ClearedAt.Valid {
		t.Fatalf("late RAISED resurrected a cleared alarm: %+v", states[0])
	}
	// And such a resolved row is excluded from the escalation candidate set.
	open, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatalf("open states: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("cleared row should not be an escalation candidate, got %d", len(open))
	}

	// Ack tombstone on a separate alarm.
	if err := api.MarkAcknowledged(ctx, "raced-ack", "k", "MAJOR", now); err != nil {
		t.Fatalf("ack tombstone: %v", err)
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"raced-ack"})
	if len(states) != 1 || !states[0].AcknowledgedAt.Valid {
		t.Fatalf("expected acked tombstone, got %+v", states)
	}
}

// OpenNotificationStates returns only notified, unresolved rows; ClaimEscalation is an
// atomic compare-and-swap that advances the tier once per level and never fires against a
// resolved alarm.
func TestOpenStatesAndClaimEscalation(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	now := time.Now().UTC()

	// Open (notified, unresolved) → a candidate.
	mustRecord(t, api, ctx, "open-1", now.Add(-time.Hour))
	// Acknowledged → not a candidate.
	mustRecord(t, api, ctx, "acked", now.Add(-time.Hour))
	if err := api.MarkAcknowledged(ctx, "acked", "k", "CRITICAL", now); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// Cleared → not a candidate.
	mustRecord(t, api, ctx, "cleared", now.Add(-time.Hour))
	mustClear(t, api, ctx, "cleared", now)

	open, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatalf("open states: %v", err)
	}
	if len(open) != 1 || open[0].AlarmToken != "open-1" {
		t.Fatalf("open candidates = %v, want [open-1]", tokensOf(open))
	}

	// Claim the first tier: wins, advances level to 1 and re-arms the notify window.
	escAt := now.Add(time.Minute)
	claimed, err := api.ClaimEscalation(ctx, "open-1", 0, escAt)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"open-1"})
	s := states[0]
	if s.EscalationLevel != 1 || !s.LastEscalatedAt.Valid || s.NotifyCount != 2 {
		t.Fatalf("post-claim state: %+v", s)
	}
	if !s.LastNotifiedAt.Time.Equal(escAt) {
		t.Fatalf("LastNotifiedAt not re-armed: %v != %v", s.LastNotifiedAt.Time, escAt)
	}

	// A stale re-claim at the same expected level LOSES (the CAS token moved) → no change.
	claimed, err = api.ClaimEscalation(ctx, "open-1", 0, escAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("stale claim err: %v", err)
	}
	if claimed {
		t.Fatal("stale claim at level 0 should lose after the level advanced")
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"open-1"})
	if states[0].EscalationLevel != 1 {
		t.Fatalf("stale claim advanced level: %d", states[0].EscalationLevel)
	}

	// Claiming a resolved alarm never wins (the CAS predicate excludes acked/cleared).
	claimed, err = api.ClaimEscalation(ctx, "acked", 0, escAt)
	if err != nil {
		t.Fatalf("claim acked err: %v", err)
	}
	if claimed {
		t.Fatal("resolved alarm should not be claimable")
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"acked"})
	if states[0].EscalationLevel != 0 {
		t.Fatalf("resolved alarm escalated: level=%d", states[0].EscalationLevel)
	}
}

// tokensOf lists the alarm tokens of a state slice for assertion messages.
func tokensOf(states []*NotificationState) []string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.AlarmToken)
	}
	return out
}

// PruneResolvedStates removes rows whose terminal stamps are all older than the cutoff,
// tenant-scoped; a recently-resolved or still-open row survives.
//
// 🔴 THE acked-old CASE IS THE DEFECT. The prune used to filter on cleared_at alone, and
// markTerminal writes a tombstone row for EVERY acknowledged or cleared alarm in every
// tenant — including tenants with no policies at all, because the row closes an ordering
// race rather than recording a page. An alarm a human acknowledged and that then stopped
// on its own, with no CLEARED ever arriving, was therefore settled forever, excluded from
// escalation, and matched by no prune predicate: one immortal row per such alarm, for the
// life of the instance.
//
// The acked-old-cleared-new case is the counterweight that keeps the widening honest: it
// would pass just as well under "prune anything with any terminal stamp older than the
// cutoff", which would cut a row's grace window short whenever its two stamps straddle
// the cutoff.
func TestPruneResolvedStates(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	now := time.Now().UTC()

	// Old cleared → pruned.
	mustRecord(t, api, ctx, "old-cleared", now.Add(-10*24*time.Hour))
	mustClear(t, api, ctx, "old-cleared", now.Add(-8*24*time.Hour))
	// Old acknowledged, never cleared → pruned. This is the class that used to be immortal.
	mustRecord(t, api, ctx, "old-acked", now.Add(-10*24*time.Hour))
	mustAck(t, api, ctx, "old-acked", now.Add(-8*24*time.Hour))
	// Acknowledged before the cutoff but cleared after it → survives its full grace window.
	mustRecord(t, api, ctx, "acked-old-cleared-new", now.Add(-10*24*time.Hour))
	mustAck(t, api, ctx, "acked-old-cleared-new", now.Add(-8*24*time.Hour))
	mustClear(t, api, ctx, "acked-old-cleared-new", now.Add(-time.Minute))
	// Recently cleared → survives.
	mustRecord(t, api, ctx, "new-cleared", now.Add(-time.Hour))
	mustClear(t, api, ctx, "new-cleared", now.Add(-time.Minute))
	// Recently acknowledged → survives.
	mustRecord(t, api, ctx, "new-acked", now.Add(-time.Hour))
	mustAck(t, api, ctx, "new-acked", now.Add(-time.Minute))
	// Still open → survives.
	mustRecord(t, api, ctx, "active", now.Add(-time.Hour))

	before := now.Add(-7 * 24 * time.Hour)
	removed, err := api.PruneResolvedStates(ctx, before)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (the old cleared row AND the old acknowledged-only row, "+
			"which a cleared_at-only predicate would leave in the table forever)", removed)
	}
	for _, tok := range []string{"old-cleared", "old-acked"} {
		if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{tok}); len(states) != 0 {
			t.Fatalf("%s not pruned", tok)
		}
	}
	for _, tok := range []string{"acked-old-cleared-new", "new-cleared", "new-acked", "active"} {
		if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{tok}); len(states) != 1 {
			t.Fatalf("%s should survive", tok)
		}
	}
}

// A tenant's prune must not reach another tenant's rows: the sweep runs per tenant and
// leans entirely on the rdb tenant-scope predicate to keep each delete local.
func TestPruneResolvedStatesIsTenantScoped(t *testing.T) {
	api := newTestApi(t)
	now := time.Now().UTC()
	for _, tenant := range []string{"A", "B"} {
		ctx := tenantCtx(tenant)
		mustRecord(t, api, ctx, "old-acked", now.Add(-10*24*time.Hour))
		mustAck(t, api, ctx, "old-acked", now.Add(-8*24*time.Hour))
	}

	before := now.Add(-7 * 24 * time.Hour)
	removed, err := api.PruneResolvedStates(tenantCtx("A"), before)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1: tenant A's prune must not reach tenant B's row", removed)
	}
	if states, _ := api.NotificationStatesByAlarmToken(tenantCtx("B"), []string{"old-acked"}); len(states) != 1 {
		t.Fatal("tenant B's row was pruned by tenant A's sweep")
	}
}

// DistinctStateTenants lists every tenant with state rows under a system context.
func TestDistinctStateTenants(t *testing.T) {
	api := newTestApi(t)
	now := time.Now().UTC()
	mustRecord(t, api, tenantCtx("A"), "a1", now)
	mustRecord(t, api, tenantCtx("B"), "b1", now)
	mustRecord(t, api, tenantCtx("A"), "a2", now)

	tenants, err := api.DistinctStateTenants(core.WithSystemContext(context.Background()))
	if err != nil {
		t.Fatalf("distinct tenants: %v", err)
	}
	got := map[string]bool{}
	for _, tn := range tenants {
		got[tn] = true
	}
	if !got["A"] || !got["B"] || len(got) != 2 {
		t.Fatalf("tenants = %v, want {A,B}", tenants)
	}
}

// mustRecord records a notification or fails the test.
func mustRecord(t *testing.T, api *Api, ctx context.Context, token string, at time.Time) {
	t.Helper()
	if err := api.RecordNotification(ctx, token, "k", "CRITICAL", at); err != nil {
		t.Fatalf("record %s: %v", token, err)
	}
}

// mustClear stamps a clear or fails the test.
func mustClear(t *testing.T, api *Api, ctx context.Context, token string, at time.Time) {
	t.Helper()
	if err := api.MarkCleared(ctx, token, "k", "CRITICAL", at); err != nil {
		t.Fatalf("clear %s: %v", token, err)
	}
}

// mustAck stamps an acknowledgement or fails the test.
func mustAck(t *testing.T, api *Api, ctx context.Context, token string, at time.Time) {
	t.Helper()
	if err := api.MarkAcknowledged(ctx, token, "k", "CRITICAL", at); err != nil {
		t.Fatalf("ack %s: %v", token, err)
	}
}
