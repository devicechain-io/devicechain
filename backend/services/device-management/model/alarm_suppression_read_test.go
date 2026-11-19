// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func TestSuppressionWindowSearchPaginatesEndedHistory(t *testing.T) {
	api, ctx, _ := newSuppressionTestApi(t)
	rows := make([]AlarmSuppressionWindow, 10001)
	for i := range rows {
		rows[i] = suppressionTestWindow()
		rows[i].Token = fmt.Sprintf("w-%05d", i)
		rows[i].CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&rows, 100).Error)
	for page, token := range map[int32]string{1: "w-10000", 2: "w-09999", 10001: "w-00000"} {
		result, err := api.AlarmSuppressionWindowsSearch(ctx, AlarmSuppressionWindowSearchCriteria{Pagination: rdb.Pagination{PageNumber: page, PageSize: 1}})
		require.NoError(t, err)
		require.EqualValues(t, len(rows), result.Pagination.TotalRecords)
		require.Len(t, result.Results, 1)
		require.Equal(t, token, result.Results[0].Token)
	}
	status := "ENDED"
	result, err := api.AlarmSuppressionWindowsSearch(ctx, AlarmSuppressionWindowSearchCriteria{Status: &status})
	require.NoError(t, err)
	require.EqualValues(t, len(rows), result.Pagination.TotalRecords)
	target := "missing"
	result, err = api.AlarmSuppressionWindowsSearch(ctx, AlarmSuppressionWindowSearchCriteria{TargetToken: &target})
	require.NoError(t, err)
	require.Zero(t, result.Pagination.TotalRecords)
}

func TestSuppressionWindowScopedHistoryIgnoresOtherCustomers(t *testing.T) {
	api, ctx, device := newSuppressionTestApi(t)
	suppressionReadSQLiteOverlap(t, api.RDB.Database)
	oldLimit := suppressionVisibleScanLimit
	suppressionVisibleScanLimit = 2
	t.Cleanup(func() { suppressionVisibleScanLimit = oldLimit })
	rt := seedRelType(t, api, ctx, "acme", "tracks", true)
	seedEdge(t, api, ctx, "acme", "owner", device, rt, "customer", "A")
	own := suppressionTestWindow()
	own.ScopeAnchors = rdb.Anchors{"customer:A"}
	require.NoError(t, api.RDB.DB(ctx).Create(&own).Error)
	for i := 0; i < 3; i++ {
		other := suppressionTestWindow()
		other.Token = fmt.Sprintf("other-%d", i)
		other.ScopeAnchors = rdb.Anchors{"customer:B"}
		other.Targets = []AlarmSuppressionTarget{{Type: "CUSTOMER", Token: "B"}}
		require.NoError(t, api.RDB.DB(ctx).Create(&other).Error)
	}
	caller := suppressionCaller(ctx, false, "customer:A")
	found, err := api.AlarmSuppressionWindowsSearch(caller, AlarmSuppressionWindowSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	require.EqualValues(t, 1, found.Pagination.TotalRecords)
	// Historical preview also ignores all finished windows, irrespective of owner.
	preview, err := api.AlarmSuppressionsForDevice(caller, "pump", *own.StartTime)
	require.NoError(t, err)
	require.Len(t, preview, 1)
	for i := 0; i < 2; i++ {
		more := own
		more.Model, more.Token = gorm.Model{}, fmt.Sprintf("own-%d", i)
		require.NoError(t, api.RDB.DB(ctx).Create(&more).Error)
	}
	_, err = api.AlarmSuppressionWindowsSearch(caller, AlarmSuppressionWindowSearchCriteria{})
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
	require.Contains(t, err.Error(), "caller-visible")
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

// SQLite does not implement PostgreSQL's text[] overlap operator. Keep the real
// scope callbacks, translating only their generated overlap expression to exact
// quoted-element membership in rdb.Anchors' array encoding. Production overlap
// SQL is exercised by the model's Postgres integration suite.
func suppressionReadSQLiteOverlap(t *testing.T, db *gorm.DB) {
	t.Helper()
	var translate func(clause.Expression) clause.Expression
	translate = func(expr clause.Expression) clause.Expression {
		switch e := expr.(type) {
		case clause.Where:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.AndConditions:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.OrConditions:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.NotConditions:
			for i, v := range e.Exprs {
				e.Exprs[i] = translate(v)
			}
			return e
		case clause.Expr:
			if strings.HasPrefix(e.SQL, "? && ARRAY[") {
				parts := make([]string, 0, len(e.Vars)-1)
				vars := make([]any, 0, 2*(len(e.Vars)-1))
				for _, anchor := range e.Vars[1:] {
					parts = append(parts, "instr(?, ?) > 0")
					vars = append(vars, e.Vars[0], fmt.Sprintf("%q", anchor))
				}
				return clause.Expr{SQL: "(" + strings.Join(parts, " OR ") + ")", Vars: vars}
			}
		}
		return expr
	}
	callback := func(tx *gorm.DB) {
		if where, ok := tx.Statement.Clauses["WHERE"]; ok {
			where.Expression = translate(where.Expression)
			tx.Statement.Clauses["WHERE"] = where
		}
	}
	require.NoError(t, db.Callback().Query().After("dc:scope_query").Before("gorm:query").Register("test:suppression_overlap", callback))
	require.NoError(t, db.Callback().Row().After("dc:scope_row").Before("gorm:row").Register("test:suppression_overlap", callback))
	require.NoError(t, db.Callback().Update().After("dc:scope_update").Before("gorm:update").Register("test:suppression_overlap", callback))
}
