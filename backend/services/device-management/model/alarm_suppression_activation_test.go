// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type suppressionCycleRig struct {
	api       *Api
	ctx       context.Context
	device    uint
	now       time.Time
	window    *AlarmSuppressionWindow
	publisher *captureAlarmPublisher
}

func newSuppressionCycleRig(t *testing.T) suppressionCycleRig {
	t.Helper()
	api, ctx, id := newSuppressionTestApi(t)
	api.RDB.Database.Logger = logger.Default.LogMode(logger.Silent)
	require.NoError(t, RegisterPositionWithholding(api.RDB.Database))
	now := time.Now().UTC().Truncate(time.Microsecond)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	w := suppressionTestWindow()
	w.StartTime, w.EndTime = &start, &end
	window, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	publisher := &captureAlarmPublisher{}
	api.AlarmPublisher = publisher
	return suppressionCycleRig{api, ctx, id, now, window, publisher}
}

func (r suppressionCycleRig) edge(at time.Time, rule, edge, severity string) error {
	return r.api.ApplyAlarmContributorEdge(r.ctx, r.device, "hot", "temp", rule, edge, severity, nil, at, new(bool))
}

func (r suppressionCycleRig) alarm(t *testing.T) *Alarm {
	t.Helper()
	alarm, err := r.api.alarmByOriginatorKey(r.ctx, "device", r.device, "hot")
	require.NoError(t, err)
	require.NotNil(t, alarm, "the contributor fold must preserve the alarm")
	return alarm
}

func (r suppressionCycleRig) stamps(t *testing.T) []AlarmCycleSuppression {
	t.Helper()
	var stamps []AlarmCycleSuppression
	require.NoError(t, r.api.RDB.DB(r.ctx).Order("raised_time").Find(&stamps).Error)
	return stamps
}

func failCycleStamp(t *testing.T) error {
	t.Helper()
	failure := errors.New("forced stamp insert failure")
	cycleStampInsertHookForTest = func(*gorm.DB) error { return failure }
	t.Cleanup(func() { cycleStampInsertHookForTest = nil })
	return failure
}

func TestCycleStampFailureRollsBackCreate(t *testing.T) {
	r := newSuppressionCycleRig(t)
	failure := failCycleStamp(t)
	require.ErrorIs(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"), failure)
	found, err := r.api.alarmByOriginatorKey(r.ctx, "device", r.device, "hot")
	require.NoError(t, err)
	require.Nil(t, found)
	require.Empty(t, r.stamps(t))
	require.Empty(t, r.publisher.events)
	cycleStampInsertHookForTest = nil
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.Len(t, r.stamps(t), 1, "redelivery must create the rolled-back cycle")
}

func TestCycleStampFailureRollsBackReactivation(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NoError(t, r.edge(r.now.Add(time.Second), "r", AlarmEdgeResolved, ""))
	old := r.alarm(t)
	r.publisher.events = nil
	failure := failCycleStamp(t)
	require.ErrorIs(t, r.edge(r.now.Add(2*time.Second), "r", AlarmEdgeRaised, "MINOR"), failure)
	after := r.alarm(t)
	require.Equal(t, string(AlarmStateCleared), after.State)
	require.Equal(t, old.ContributorVersion, after.ContributorVersion)
	require.True(t, old.RaisedTime.Equal(after.RaisedTime))
	require.Equal(t, old.Contributors, after.Contributors)
	require.Len(t, r.stamps(t), 1)
	require.Empty(t, r.publisher.events)
}

func TestCycleStampWrittenOnCreateAndReactivation(t *testing.T) {
	r := newSuppressionCycleRig(t)
	rt := seedRelType(t, r.api, r.ctx, "acme", "owns", true)
	seedEdge(t, r.api, r.ctx, "acme", "owns-pump", r.device, rt, "customer", "owner")
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NoError(t, r.edge(r.now.Add(time.Second), "r", AlarmEdgeResolved, ""))
	require.NoError(t, r.edge(r.now.Add(2*time.Second), "r", AlarmEdgeRaised, "MINOR"))
	stamps := r.stamps(t)
	require.Len(t, stamps, 2)
	alarm := r.alarm(t)
	require.Equal(t, alarm.ID, stamps[1].AlarmId)
	require.Equal(t, alarm.TenantId, stamps[1].TenantId)
	require.Equal(t, alarm.Token, stamps[1].AlarmToken)
	require.Equal(t, alarm.ScopeAnchors, stamps[1].ScopeAnchors)
	require.Equal(t, rdb.Anchors{"customer:owner"}, stamps[1].ScopeAnchors)
	require.True(t, alarm.RaisedTime.Equal(stamps[1].RaisedTime))
	require.Equal(t, r.window.Token, r.publisher.events[0].RaisedDuringSuppressionToken)
	require.Equal(t, r.window.Token, r.publisher.events[2].RaisedDuringSuppressionToken)
}

func TestEscalationDoesNotStamp(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NoError(t, r.edge(r.now.Add(time.Second), "critical", AlarmEdgeRaised, "CRITICAL"))
	require.NoError(t, r.edge(r.now.Add(2*time.Second), "critical", AlarmEdgeResolved, ""))
	require.Len(t, r.stamps(t), 1)
	require.Equal(t, AlarmEventEscalated, r.publisher.events[1].EventType)
	require.Empty(t, r.publisher.events[1].RaisedDuringSuppressionToken)
	require.Empty(t, r.publisher.events[2].RaisedDuringSuppressionToken)
}

func TestStaleOrDuplicateEdgeDoesNotEvaluate(t *testing.T) {
	r := newSuppressionCycleRig(t)
	calls := 0
	suppressionEvaluationHookForTest = func(ctx context.Context, id uint, key, severity string, at time.Time) (*SuppressionMatch, error) {
		calls++
		return r.api.SuppressionFor(ctx, id, key, severity, at)
	}
	t.Cleanup(func() { suppressionEvaluationHookForTest = nil })
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	before := calls
	require.Positive(t, before)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NoError(t, r.edge(r.now.Add(-time.Second), "r", AlarmEdgeRaised, "CRITICAL"))
	require.Equal(t, before, calls)
	require.Len(t, r.stamps(t), 1)
	require.Len(t, r.publisher.events, 1)
}

func TestPriorCycleStampSurvivesNextCycle(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	initial := r.stamps(t)
	require.Len(t, initial, 1)
	first := initial[0]
	require.NoError(t, r.edge(r.now.Add(time.Second), "r", AlarmEdgeResolved, ""))
	_, err := r.api.EndAlarmSuppressionWindow(r.ctx, r.window.Token)
	require.NoError(t, err)
	require.NoError(t, r.edge(r.now.Add(2*time.Hour), "r", AlarmEdgeRaised, "MINOR"))
	stamps := r.stamps(t)
	require.Len(t, stamps, 1)
	require.Equal(t, first.ID, stamps[0].ID)
	require.True(t, first.RaisedTime.Equal(stamps[0].RaisedTime))
	require.Empty(t, r.publisher.events[2].RaisedDuringSuppressionToken)
	current, err := r.api.AlarmCycleSuppressionsFor(r.ctx, []Alarm{*r.alarm(t)})
	require.NoError(t, err)
	require.Empty(t, current)
}

func TestOldPodReactivationDoesNotInheritStamp(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	alarm := r.alarm(t)
	current, err := r.api.AlarmCycleSuppressionsFor(r.ctx, []Alarm{*alarm})
	require.NoError(t, err)
	require.Equal(t, r.window.Token, current[alarm.ID])
	assertSearch := func(stamped bool, want int) {
		t.Helper()
		found, err := r.api.Alarms(r.ctx, AlarmSearchCriteria{RaisedDuringSuppression: &stamped})
		require.NoError(t, err)
		require.Len(t, found.Results, want)
		require.EqualValues(t, want, found.Pagination.TotalRecords)
	}
	assertSearch(true, 1)
	assertSearch(false, 0)
	next := alarm.RaisedTime.Add(time.Hour)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(alarm).Update("raised_time", next).Error)
	alarm = r.alarm(t)
	current, err = r.api.AlarmCycleSuppressionsFor(r.ctx, []Alarm{*alarm})
	require.NoError(t, err)
	require.Empty(t, current, "an old pod's next cycle has no history stamp")
	assertSearch(true, 0)
	assertSearch(false, 1)
	require.Len(t, r.stamps(t), 1)
}

func TestBackfillStampsEventTimeButLiveIsWallTime(t *testing.T) {
	r := newSuppressionCycleRig(t)
	past := r.now.Add(-24 * time.Hour)
	start, end := past.Add(-time.Hour), past.Add(time.Hour)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(r.window).Updates(map[string]any{"start_time": start, "end_time": end}).Error)
	r.api.invalidateSuppressionWindows(r.ctx)
	require.NoError(t, r.edge(past, "r", AlarmEdgeRaised, "MINOR"))
	stamps := r.stamps(t)
	require.Len(t, stamps, 1)
	require.True(t, past.Equal(stamps[0].RaisedTime))
	require.Len(t, r.publisher.events, 1)
	event := r.publisher.events[0]
	require.Equal(t, r.window.Token, event.RaisedDuringSuppressionToken)
	require.Nil(t, event.SuppressedUntil)
	require.Empty(t, event.SuppressionWindowToken)
}

func TestSuppressedConditionEventuallyPagesWithoutAnotherRisingEdge(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now.Add(-time.Minute), "r", AlarmEdgeRaised, "MINOR"))
	alarm := r.alarm(t)
	require.NotNil(t, alarm, "suppression must preserve the rising edge and the ACTIVE condition")
	require.Equal(t, string(AlarmStateActive), alarm.State)
	require.Len(t, r.publisher.events, 1)
	require.NotNil(t, r.publisher.events[0].SuppressedUntil)
	status, err := r.api.AlarmSuppressionStatus(r.ctx, []string{alarm.Token})
	require.NoError(t, err)
	require.Len(t, status, 1)
	require.NotNil(t, status[0].Match)
	ended := time.Now().UTC().Add(-time.Second)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(r.window).Update("ended_at", ended).Error)
	r.api.invalidateSuppressionWindows(r.ctx)
	status, err = r.api.AlarmSuppressionStatus(r.ctx, []string{alarm.Token})
	require.NoError(t, err)
	require.Len(t, status, 1)
	require.Nil(t, status[0].Match)
	require.Equal(t, string(AlarmStateActive), r.alarm(t).State)
	require.Len(t, r.publisher.events, 1, "window end needs no new edge or synthetic event")
}

func TestLiveSuppressionOnRaisedAndEscalated(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NoError(t, r.edge(r.now.Add(time.Second), "critical", AlarmEdgeRaised, "CRITICAL"))
	for _, event := range r.publisher.events {
		require.NotNil(t, event.SuppressedUntil)
		require.Equal(t, time.UTC, event.SuppressedUntil.Location())
		require.Equal(t, r.window.Token, event.SuppressionWindowToken)
	}
}

func TestAckAndClearDoNotEvaluate(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	calls := 0
	suppressionEvaluationHookForTest = func(context.Context, uint, string, string, time.Time) (*SuppressionMatch, error) {
		calls++
		return nil, errors.New("must not evaluate ack or clear")
	}
	t.Cleanup(func() { suppressionEvaluationHookForTest = nil })
	_, err := r.api.AcknowledgeAlarm(r.ctx, r.alarm(t).Token, nil)
	require.NoError(t, err)
	_, err = r.api.ClearAlarm(r.ctx, r.alarm(t).Token)
	require.NoError(t, err)
	require.Zero(t, calls)
	for _, event := range r.publisher.events[1:] {
		require.Nil(t, event.SuppressedUntil)
		require.Empty(t, event.SuppressionWindowToken)
	}
}

func TestLiveEvaluationErrorPublishesWithoutSuppression(t *testing.T) {
	r := newSuppressionCycleRig(t)
	before := testutil.ToFloat64(suppressionEvaluationFailures.WithLabelValues("live"))
	calls := 0
	suppressionEvaluationHookForTest = func(ctx context.Context, _ uint, _, _ string, at time.Time) (*SuppressionMatch, error) {
		calls++
		if calls == 1 {
			return nil, nil
		} // cycle-start decision
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 500*time.Millisecond)
		require.WithinDuration(t, time.Now(), at, time.Second)
		return nil, errors.New("live lookup failure")
	}
	t.Cleanup(func() { suppressionEvaluationHookForTest = nil })
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.Len(t, r.publisher.events, 1)
	require.Nil(t, r.publisher.events[0].SuppressedUntil)
	require.Empty(t, r.publisher.events[0].SuppressionWindowToken)
	require.Equal(t, before+1, testutil.ToFloat64(suppressionEvaluationFailures.WithLabelValues("live")))
}

func TestCycleEvaluationErrorStillRaisesWithoutStamp(t *testing.T) {
	r := newSuppressionCycleRig(t)
	before := testutil.ToFloat64(suppressionEvaluationFailures.WithLabelValues("cycle_start"))
	suppressionEvaluationHookForTest = func(context.Context, uint, string, string, time.Time) (*SuppressionMatch, error) {
		return nil, errors.New("lookup failure")
	}
	t.Cleanup(func() { suppressionEvaluationHookForTest = nil })
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NotNil(t, r.alarm(t))
	require.Empty(t, r.stamps(t))
	require.Len(t, r.publisher.events, 1)
	require.Empty(t, r.publisher.events[0].RaisedDuringSuppressionToken)
	require.Equal(t, before+1, testutil.ToFloat64(suppressionEvaluationFailures.WithLabelValues("cycle_start")))
}

func TestCycleCASLoserDoesNotStamp(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeResolved, ""))
	old := r.alarm(t)
	require.Empty(t, r.stamps(t), "a cleared tombstone is not a cycle start")
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&Alarm{}).Where("id = ?", old.ID).Update("contributor_version", old.ContributorVersion+1).Error)
	err := r.api.updateAlarmFromContributors(r.ctx, old, "MINOR", true, old.LastValue, []byte(`{}`), r.now.Add(time.Second), new(bool))
	require.ErrorIs(t, err, errAlarmContributorConflict)
	require.Empty(t, r.stamps(t))
	require.Empty(t, r.publisher.events)
}

func TestAlarmSuppressionStatusVisibilityAndLimits(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	visible := r.alarm(t)
	hidden := *visible
	hidden.Model, hidden.Token, hidden.AlarmKey, hidden.PositionDerived = gorm.Model{}, "hidden", "fence", verdict(true)
	require.NoError(t, r.api.RDB.DB(r.ctx).Create(&hidden).Error)
	other := *visible
	other.Model, other.Token, other.TenantId = gorm.Model{}, "other", "other"
	require.NoError(t, r.api.RDB.DB(core.WithTenant(context.Background(), "other")).Create(&other).Error)
	viewer := suppressionCaller(r.ctx, false)
	status, err := r.api.AlarmSuppressionStatus(viewer, []string{visible.Token, hidden.Token, other.Token, "unknown"})
	require.NoError(t, err)
	require.Len(t, status, 1)
	require.Equal(t, visible.Token, status[0].AlarmToken)
	require.NotNil(t, status[0].Match)
	_, err = r.api.ClearAlarm(r.ctx, visible.Token)
	require.NoError(t, err)
	status, err = r.api.AlarmSuppressionStatus(viewer, []string{visible.Token})
	require.NoError(t, err)
	require.Len(t, status, 1)
	require.Nil(t, status[0].Match)
	_, err = r.api.AlarmSuppressionStatus(viewer, make([]string, 201))
	require.ErrorIs(t, err, ErrInvalidAlarmSuppression)
	_, err = r.api.AlarmSuppressionStatus(auth.WithClaims(r.ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme"}), []string{visible.Token})
	require.ErrorIs(t, err, auth.ErrForbidden)
	// The notification service's alarm:read token is sufficient.
	service := auth.WithClaims(r.ctx, &auth.Claims{TokenType: auth.TokenTypeService, Tenant: "acme", Authorities: []string{string(auth.AlarmRead)}})
	status, err = r.api.AlarmSuppressionStatus(service, []string{visible.Token})
	require.NoError(t, err)
	require.Len(t, status, 1)
}

func TestAlarmSuppressionStatusEvaluationErrorIsNotNilMatch(t *testing.T) {
	r := newSuppressionCycleRig(t)
	require.NoError(t, r.edge(r.now, "r", AlarmEdgeRaised, "MINOR"))
	require.NoError(t, r.api.RDB.Database.Migrator().DropTable(&Device{}))
	status, err := r.api.AlarmSuppressionStatus(r.ctx, []string{r.alarm(t).Token})
	require.Error(t, err)
	require.Nil(t, status)
}

func TestSuppressionZeroWindowsReadsNoDeviceFacts(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	facts := 0
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:zero_windows", func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Table != "alarm_suppression_windows" {
			facts++
		}
	}))
	for _, at := range []time.Time{time.Now(), time.Now().Add(-24 * time.Hour)} {
		match, err := api.SuppressionFor(ctx, id, "hot", "MINOR", at)
		require.NoError(t, err)
		require.Nil(t, match)
	}
	require.Zero(t, facts)
}

func TestSuppressionStaticGroupQueryCountIsBatched(t *testing.T) {
	api, ctx, id := newSuppressionTestApi(t)
	var device Device
	require.NoError(t, api.RDB.DB(ctx).First(&device, id).Error)
	rt := seedRelType(t, api, ctx, "acme", MembershipRelationshipType, false)
	windows := []compiledWindow{}
	for i := 0; i < 21; i++ {
		g := EntityGroup{MemberType: "device", MembershipMode: string(MembershipStatic)}
		g.Token = fmt.Sprintf("fleet-%d", i)
		require.NoError(t, api.RDB.DB(ctx).Create(&g).Error)
		edge := EntityRelationship{SourceType: "group", SourceId: g.ID, TargetType: "device", TargetId: id, RelationshipTypeId: rt}
		edge.Token = fmt.Sprintf("member-%d", i)
		require.NoError(t, api.RDB.DB(ctx).Create(&edge).Error)
		w := suppressionTestWindow()
		w.Targets = []AlarmSuppressionTarget{{Type: "ENTITY_GROUP", Token: g.Token}}
		windows = append(windows, compiledWindow{window: w})
	}
	statements := 0
	count := func(*gorm.DB) { statements++ }
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:batch_queries", count))
	require.NoError(t, api.RDB.Database.Callback().Row().Before("gorm:row").Register("test:batch_rows", count))
	_, err := api.suppressionDeviceFacts(suppressionFactContext(ctx), &device, windows[:1])
	require.NoError(t, err)
	one := statements
	statements = 0
	facts, err := api.suppressionDeviceFacts(suppressionFactContext(ctx), &device, windows)
	require.NoError(t, err)
	require.Equal(t, one, statements, "named static groups must not add one query each")
	require.Len(t, facts.groupTokens, len(windows))
}

func TestSuppressionCycleKeyPreservesContributorPrecision(t *testing.T) {
	r := newSuppressionCycleRig(t)
	first, second := r.now.Add(100*time.Nanosecond), r.now.Add(200*time.Nanosecond)
	require.NoError(t, r.edge(first, "r", AlarmEdgeRaised, "MAJOR"))
	require.NoError(t, r.edge(second, "r", AlarmEdgeRaised, "MINOR"))
	alarm := r.alarm(t)
	require.Equal(t, "MINOR", alarm.Severity)
	contributors, err := decodeContributors(alarm.Contributors)
	require.NoError(t, err)
	require.True(t, contributors["r"].DecisionTs.Equal(second))
	require.True(t, alarm.RaisedTime.Equal(r.now))
	require.Equal(t, first, r.publisher.events[0].OccurredTime)
	require.Len(t, r.stamps(t), 1)
}
