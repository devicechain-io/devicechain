// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// The window audit follows the held EVENT, ordered by occurred time like the event columns:
// an older redelivery never rewrites it, a newer event held by quiet hours alone clears it
// (the page now describes an event no window held), and an old pod's upsert, which does not
// name the column, leaves it as it is.
func TestSuppressionDeferralAuditFollowsTheHeldEvent(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "tenant")
	now := time.Now().UTC().Truncate(time.Microsecond)
	row := func(at time.Time, window string) *NotificationDeferral {
		return &NotificationDeferral{AlarmToken: "a", PolicyId: 1, AlarmKey: "k", Severity: "MAJOR", OccurredTime: at, ReleaseAt: at.Add(time.Minute), Event: datatypes.JSON(`{}`), HeldByWindow: sql.NullString{String: window, Valid: window != ""}}
	}
	audit := func() sql.NullString {
		rows, err := api.NotificationDeferralsByAlarmToken(ctx, []string{"a"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0].HeldByWindow
	}
	require.NoError(t, api.DeferNotifications(ctx, []*NotificationDeferral{row(now, "window")}))
	require.Equal(t, sql.NullString{String: "window", Valid: true}, audit())
	// An OLDER event carrying another window is a redelivery: the newer event and its audit stay.
	require.NoError(t, api.DeferNotifications(ctx, []*NotificationDeferral{row(now.Add(-time.Second), "older")}))
	require.Equal(t, sql.NullString{String: "window", Valid: true}, audit())
	// Model an old pod's upsert: it does not mention the new column, so it keeps the audit.
	require.NoError(t, api.RDB.DB(ctx).Exec(`INSERT INTO notification_deferrals
	 (tenant_id,alarm_token,policy_id,alarm_key,severity,occurred_time,release_at,event)
	 VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(tenant_id,alarm_token,policy_id)
	 DO UPDATE SET release_at=excluded.release_at`, "tenant", "a", 1, "k", "MAJOR", now, now.Add(time.Hour), `{}`).Error)
	require.Equal(t, sql.NullString{String: "window", Valid: true}, audit())
	// A NEWER event held by quiet hours alone replaces the event, and with it the audit.
	require.NoError(t, api.DeferNotifications(ctx, []*NotificationDeferral{row(now.Add(time.Second), "")}))
	require.False(t, audit().Valid)
}

func TestSuppressionRescheduleLeavesConcurrentReplacementAlone(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "tenant")
	now := time.Now().UTC().Truncate(time.Microsecond)
	d := &NotificationDeferral{AlarmToken: "a", PolicyId: 1, AlarmKey: "k", Severity: "MAJOR", OccurredTime: now, ReleaseAt: now, Event: datatypes.JSON(`{}`)}
	require.NoError(t, api.DeferNotifications(ctx, []*NotificationDeferral{d}))
	rows, err := api.NotificationDeferralsByAlarmToken(ctx, []string{"a"})
	require.NoError(t, err)
	stale := rows[0]
	require.NoError(t, api.RDB.DB(ctx).Model(&NotificationDeferral{ID: stale.ID}).Updates(map[string]any{"updated_at": now.Add(time.Hour), "release_at": now.Add(2 * time.Hour), "held_by_window": "new"}).Error)
	require.NoError(t, api.RescheduleSuppressedNotificationDeferral(ctx, stale, now.Add(time.Minute), "stale"))
	rows, err = api.NotificationDeferralsByAlarmToken(ctx, []string{"a"})
	require.NoError(t, err)
	require.True(t, rows[0].ReleaseAt.Equal(now.Add(2*time.Hour)))
	require.Equal(t, "new", rows[0].HeldByWindow.String)
}
