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

// Ack/clear stamps are update-if-exists, idempotent, and a no-op when no row exists.
func TestMarkAcknowledgedAndClearedIdempotent(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")

	now := time.Now().UTC()
	// No row yet: both are silent no-ops.
	if err := api.MarkAcknowledged(ctx, "ghost", now); err != nil {
		t.Fatalf("ack no-row: %v", err)
	}
	if err := api.MarkCleared(ctx, "ghost", now); err != nil {
		t.Fatalf("clear no-row: %v", err)
	}
	if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"ghost"}); len(states) != 0 {
		t.Fatalf("no-op created a row: %d", len(states))
	}

	if err := api.RecordNotification(ctx, "alarm-2", "k", "CRITICAL", now); err != nil {
		t.Fatalf("record: %v", err)
	}
	ackAt := now.Add(time.Minute)
	if err := api.MarkAcknowledged(ctx, "alarm-2", ackAt); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// A second ack must not overwrite the first stamp (WHERE acknowledged_at IS NULL).
	if err := api.MarkAcknowledged(ctx, "alarm-2", ackAt.Add(time.Hour)); err != nil {
		t.Fatalf("re-ack: %v", err)
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"alarm-2"})
	if !states[0].AcknowledgedAt.Valid || !states[0].AcknowledgedAt.Time.Equal(ackAt) {
		t.Fatalf("AcknowledgedAt = %+v, want %v", states[0].AcknowledgedAt, ackAt)
	}

	clearAt := now.Add(2 * time.Minute)
	if err := api.MarkCleared(ctx, "alarm-2", clearAt); err != nil {
		t.Fatalf("clear: %v", err)
	}
	states, _ = api.NotificationStatesByAlarmToken(ctx, []string{"alarm-2"})
	if !states[0].ClearedAt.Valid {
		t.Fatalf("ClearedAt not set")
	}
}

// PruneClearedStates removes only rows cleared before the cutoff, tenant-scoped; a
// recently-cleared or never-cleared row survives.
func TestPruneClearedStates(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	now := time.Now().UTC()

	// Old cleared → pruned.
	mustRecord(t, api, ctx, "old-cleared", now.Add(-10*24*time.Hour))
	mustClear(t, api, ctx, "old-cleared", now.Add(-8*24*time.Hour))
	// Recently cleared → survives.
	mustRecord(t, api, ctx, "new-cleared", now.Add(-time.Hour))
	mustClear(t, api, ctx, "new-cleared", now.Add(-time.Minute))
	// Never cleared → survives.
	mustRecord(t, api, ctx, "active", now.Add(-time.Hour))

	before := now.Add(-7 * 24 * time.Hour)
	removed, err := api.PruneClearedStates(ctx, before)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"old-cleared"}); len(states) != 0 {
		t.Fatalf("old-cleared not pruned")
	}
	for _, tok := range []string{"new-cleared", "active"} {
		if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{tok}); len(states) != 1 {
			t.Fatalf("%s should survive", tok)
		}
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
	if err := api.MarkCleared(ctx, token, at); err != nil {
		t.Fatalf("clear %s: %v", token, err)
	}
}
