// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSuppressionWindowOpeningsAndStatus(t *testing.T) {
	w := suppressionTestWindow()
	start, end := *w.StartTime, *w.EndTime
	require.Equal(t, "SCHEDULED", AlarmSuppressionWindowStatus(w, start.Add(-time.Second)))
	require.Equal(t, "ACTIVE", AlarmSuppressionWindowStatus(w, start))
	require.Equal(t, "ENDED", AlarmSuppressionWindowStatus(w, end))
	require.Empty(t, AlarmSuppressionWindowOpenings(w, end, 5))
	require.Empty(t, AlarmSuppressionWindowOpenings(w, start, 0))
	require.Empty(t, AlarmSuppressionWindowOpenings(w, start, -1))
	ivs := AlarmSuppressionWindowOpenings(w, start, 5)
	require.Equal(t, []Interval{{Start: start, End: end}}, ivs)
	ended := start.Add(10 * time.Minute)
	w.EndedAt = &ended
	require.Equal(t, ended, AlarmSuppressionWindowOpenings(w, start, 1)[0].End)
	require.Equal(t, "ENDED", AlarmSuppressionWindowStatus(w, ended))
	w.DeletedAt = gorm.DeletedAt{Time: start, Valid: true}
	require.Equal(t, "ENDED", AlarmSuppressionWindowStatus(w, start))
}

func TestSuppressionWindowOpeningsDSTAndRecurringGap(t *testing.T) {
	w := suppressionRecurringWindow()
	from := utcAt(2026, 3, 29, 1, 0)
	ivs := AlarmSuppressionWindowOpenings(w, from, 2)
	require.Len(t, ivs, 2)
	require.Equal(t, from, ivs[0].Start)
	require.Equal(t, 2*time.Hour, ivs[0].End.Sub(ivs[0].Start))
	require.True(t, ivs[1].Start.After(ivs[0].End), "advance beyond the previous interval")
	require.Equal(t, "ACTIVE", AlarmSuppressionWindowStatus(w, from))
	require.Equal(t, "SCHEDULED", AlarmSuppressionWindowStatus(w, ivs[0].End))
	require.Len(t, AlarmSuppressionWindowOpenings(w, from), 5)
	require.Len(t, AlarmSuppressionWindowOpenings(w, from, 100), 20)
	validFrom, validUntil := from.Add(time.Minute), from.Add(time.Hour)
	w.ValidFrom, w.ValidUntil = &validFrom, &validUntil
	ivs = AlarmSuppressionWindowOpenings(w, from, 20)
	require.Equal(t, []Interval{{Start: validFrom, End: validUntil}}, ivs)
	require.Equal(t, "SCHEDULED", AlarmSuppressionWindowStatus(w, from))
	require.Equal(t, "ENDED", AlarmSuppressionWindowStatus(w, validUntil))
}

func TestSuppressionWindowSearchStatusTargetAndBounds(t *testing.T) {
	r := newSuppressionCycleRig(t)
	scheduled := suppressionTestWindow()
	scheduled.Token = "future"
	start, end := r.now.Add(2*time.Hour), r.now.Add(3*time.Hour)
	scheduled.StartTime, scheduled.EndTime = &start, &end
	_, err := r.api.CreateAlarmSuppressionWindow(r.ctx, &scheduled)
	require.NoError(t, err)
	ended := suppressionTestWindow()
	ended.Token = "past"
	ended.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "other-device"}}
	start, end = r.now.Add(-3*time.Hour), r.now.Add(-2*time.Hour)
	ended.StartTime, ended.EndTime = &start, &end
	_, err = r.api.CreateAlarmSuppressionWindow(r.ctx, &ended)
	require.NoError(t, err)
	for status, token := range map[string]string{"ACTIVE": r.window.Token, "SCHEDULED": scheduled.Token, "ENDED": ended.Token} {
		found, err := r.api.AlarmSuppressionWindowsSearch(r.ctx, AlarmSuppressionWindowSearchCriteria{Status: &status})
		require.NoError(t, err)
		require.Len(t, found.Results, 1)
		require.Equal(t, token, found.Results[0].Token)
		require.EqualValues(t, 1, found.Pagination.TotalRecords)
	}
	target := "pump"
	found, err := r.api.AlarmSuppressionWindowsSearch(r.ctx, AlarmSuppressionWindowSearchCriteria{TargetToken: &target, Pagination: rdb.Pagination{PageNumber: 2, PageSize: 1}})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	require.EqualValues(t, 2, found.Pagination.TotalRecords)
	require.Equal(t, r.window.Token, found.Results[0].Token, "DefaultOrder places the newer scheduled window first")
	bad := "TYPO"
	_, err = r.api.AlarmSuppressionWindowsSearch(r.ctx, AlarmSuppressionWindowSearchCriteria{Status: &bad})
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
	byTokens, err := r.api.AlarmSuppressionWindowsByTokens(r.ctx, []string{r.window.Token, "missing"})
	require.NoError(t, err)
	require.Len(t, byTokens, 1)
	_, err = r.api.AlarmSuppressionWindowsByTokens(r.ctx, make([]string, 101))
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
	empty, err := r.api.AlarmSuppressionWindowsByTokens(r.ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestSuppressionWindowSearchRefusesOversizedTenant(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	rows := make([]AlarmSuppressionWindow, 10001)
	for i := range rows {
		rows[i] = suppressionTestWindow()
		rows[i].Token = fmt.Sprintf("w-%d", i)
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&rows, 100).Error)
	_, err := api.AlarmSuppressionWindowsSearch(ctx, AlarmSuppressionWindowSearchCriteria{})
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
}

func TestSuppressionDevicePreviewIgnoresAlarmKeysAndBypass(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(r.window).Updates(map[string]any{"alarm_keys": `["other"]`, "bypass_severity": "INDETERMINATE"}).Error)
	windows, err := r.api.AlarmSuppressionsForDevice(r.ctx, "pump", time.Time{})
	require.NoError(t, err)
	require.Len(t, windows, 1)
	windows, err = r.api.AlarmSuppressionsForDevice(r.ctx, "pump", r.now.Add(2*time.Hour))
	require.NoError(t, err)
	require.Empty(t, windows)
	_, err = r.api.AlarmSuppressionsForDevice(r.ctx, "missing", r.now)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = r.api.AlarmSuppressionsForDevice(auth.WithClaims(context.Background(), nil), "pump", r.now)
	require.Error(t, err)
}

func TestSuppressionStatusLoadsWindowsOnce(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	alarms := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		alarm := *r.alarm(t)
		alarm.Model = gorm.Model{}
		alarm.Token = fmt.Sprintf("alarm-%d", i)
		alarm.AlarmKey = alarm.Token
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(&alarm).Error)
		alarms = append(alarms, alarm.Token)
	}
	count := 0
	counter := func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Table == "alarm_suppression_windows" {
			count++
		}
	}
	require.NoError(t, r.api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:window_snapshot", counter))
	require.NoError(t, r.api.RDB.Database.Callback().Row().Before("gorm:row").Register("test:window_snapshot", counter))
	r.api.invalidateSuppressionWindows(r.ctx)
	status, err := r.api.AlarmSuppressionStatus(r.ctx, alarms)
	require.NoError(t, err)
	require.Len(t, status, 3)
	require.Equal(t, 2, count, "one signature and one load for the entire batch")
}

func TestLiveSuppressionRequiresPublisherAndDeviceTransition(t *testing.T) {
	r := newSuppressionCycleRig(t)
	calls := 0
	suppressionEvaluationHookForTest = func(context.Context, uint, string, string, time.Time) (*SuppressionMatch, error) {
		calls++
		return nil, nil
	}
	t.Cleanup(func() { suppressionEvaluationHookForTest = nil })
	r.api.AlarmPublisher = nil
	r.api.emitAlarmEvent(r.ctx, &AlarmStateChangeEvent{EventType: AlarmEventRaised, OriginatorType: "device", OriginatorId: r.device})
	require.Zero(t, calls)
	r.api.AlarmPublisher = r.publisher
	r.api.emitAlarmEvent(r.ctx, &AlarmStateChangeEvent{EventType: AlarmEventRaised, OriginatorType: "asset"})
	r.api.emitAlarmEvent(r.ctx, &AlarmStateChangeEvent{EventType: AlarmEventDeescalated, OriginatorType: "device", OriginatorId: r.device})
	require.Zero(t, calls)
}
