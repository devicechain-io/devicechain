// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecordPagedEscalationClock(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	eventAt := at.Add(-2 * time.Hour)
	record := func(cycle, eventAt, sent time.Time) *NotificationState {
		t.Helper()
		row, err := api.RecordPagedFrom(ctx, "a", "k", "CRITICAL", cycle, eventAt, nil, AlarmSource{}, eventAt, sent)
		require.NoError(t, err)
		rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, row.EscalationBaseAt.Valid, rows[0].EscalationBaseAt.Valid)
		require.True(t, row.EscalationBaseAt.Time.Equal(rows[0].EscalationBaseAt.Time))
		return rows[0]
	}
	row := record(eventAt, eventAt, at)
	require.True(t, row.EscalationBaseAt.Valid)
	require.True(t, row.EscalationBaseAt.Time.Equal(at))
	require.True(t, row.LastNotifiedAt.Time.Equal(eventAt), "served stamp retains event time")
	row = record(eventAt, eventAt.Add(time.Minute), at.Add(time.Minute))
	require.True(t, row.EscalationBaseAt.Time.Equal(at.Add(time.Minute)))
	row = record(eventAt, eventAt, at)
	require.True(t, row.EscalationBaseAt.Time.Equal(at.Add(time.Minute)), "late completion cannot regress base")
	claimedAt := at.Add(31 * time.Minute)
	claimed, err := api.ClaimEscalation(ctx, "a", 0, row.CycleRaisedAt, claimedAt)
	require.NoError(t, err)
	require.True(t, claimed)
	rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a"})
	require.NoError(t, err)
	require.True(t, rows[0].EscalationBaseAt.Time.Equal(claimedAt))
	require.NoError(t, api.MarkCleared(ctx, "a", "k", "CRITICAL", eventAt, eventAt.Add(time.Hour)))
	// A later cycle must reset even a future base left by the previous cycle.
	nextCycle := eventAt.Add(90 * time.Minute)
	row = record(nextCycle, nextCycle, at.Add(2*time.Minute))
	require.True(t, row.EscalationBaseAt.Time.Equal(at.Add(2*time.Minute)))
	require.Equal(t, 0, row.EscalationLevel)
	require.False(t, row.LastEscalatedAt.Valid)
	// An earlier cycle's late page cannot re-arm this one.
	row = record(eventAt, eventAt, at.Add(3*time.Hour))
	require.True(t, row.EscalationBaseAt.Time.Equal(at.Add(2*time.Minute)))
}
