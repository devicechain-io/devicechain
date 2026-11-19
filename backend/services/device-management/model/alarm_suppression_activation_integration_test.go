// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newSuppressionPostgresRig(t *testing.T) (scopeAlarmRig, *captureAlarmPublisher) {
	t.Helper()
	r := newScopeAlarmRig(t)
	require.NoError(t, schema.NewAlarmSuppressionSchema().Migrate(r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))))
	now := time.Now().UTC()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "acme-pump"}}
	w.StartTime, w.EndTime = &start, &end
	_, err := r.api.CreateAlarmSuppressionWindow(r.operator, &w)
	require.NoError(t, err)
	publisher := &captureAlarmPublisher{}
	r.api.AlarmPublisher = publisher
	return r, publisher
}

func TestSuppressionPostgresStampTransactionAndTimestampPrecision(t *testing.T) {
	r, publisher := newSuppressionPostgresRig(t)
	// A non-UTC, sub-microsecond event time exercises the real timestamp engine.
	at := time.Now().In(time.FixedZone("source", -7*3600)).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	raw := core.WithTenant(context.Background(), r.tenant)
	fold := func(at time.Time, edge string) error {
		ctx, cancel := context.WithTimeout(raw, 5*time.Second)
		defer cancel()
		return r.api.ApplyAlarmContributorEdge(ctx, r.devices["acme-pump"], "hot", "temp", "r", edge, "MINOR", nil, at, new(bool))
	}
	pool, err := r.api.RDB.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	defer pool.SetMaxOpenConns(0)
	failure := errors.New("postgres stamp failure")
	cycleStampInsertHookForTest = func(*gorm.DB) error { return failure }
	t.Cleanup(func() { cycleStampInsertHookForTest = nil })
	require.ErrorIs(t, fold(at, AlarmEdgeRaised), failure)
	var count int64
	require.NoError(t, r.api.RDB.DB(raw).Model(&Alarm{}).Where("alarm_key = ?", "hot").Count(&count).Error)
	require.Zero(t, count)
	require.Empty(t, publisher.events)
	cycleStampInsertHookForTest = nil
	require.NoError(t, fold(at, AlarmEdgeRaised))
	alarm := r.alarm(t, "hot")
	expected := at.UTC().Truncate(time.Microsecond)
	require.True(t, expected.Equal(alarm.RaisedTime))
	stamps, err := r.api.AlarmCycleSuppressionsFor(r.operator, []Alarm{*alarm})
	require.NoError(t, err)
	require.Equal(t, "service", stamps[alarm.ID])
	stamped := true
	found, err := r.api.Alarms(r.operator, AlarmSearchCriteria{RaisedDuringSuppression: &stamped})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	require.NoError(t, fold(at.Add(time.Second), AlarmEdgeResolved))
	old := r.alarm(t, "hot")
	publisher.events = nil
	cycleStampInsertHookForTest = func(*gorm.DB) error { return failure }
	require.ErrorIs(t, fold(at.Add(2*time.Second), AlarmEdgeRaised), failure)
	after := r.alarm(t, "hot")
	require.Equal(t, string(AlarmStateCleared), after.State)
	require.Equal(t, old.ContributorVersion, after.ContributorVersion)
	require.True(t, old.RaisedTime.Equal(after.RaisedTime))
	require.Empty(t, publisher.events)
	cycleStampInsertHookForTest = nil
	require.NoError(t, fold(at.Add(2*time.Second), AlarmEdgeRaised))
	require.NoError(t, r.api.RDB.DB(raw).Model(&AlarmCycleSuppression{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	// Simulate a pre-suppression pod moving the current cycle without inserting history.
	alarm = r.alarm(t, "hot")
	require.NoError(t, r.api.RDB.DB(raw).Model(alarm).Update("raised_time", at.UTC().Add(time.Minute).Truncate(time.Microsecond)).Error)
	stamps, err = r.api.AlarmCycleSuppressionsFor(r.operator, []Alarm{*r.alarm(t, "hot")})
	require.NoError(t, err)
	require.Empty(t, stamps)
	found, err = r.api.Alarms(r.operator, AlarmSearchCriteria{RaisedDuringSuppression: &stamped})
	require.NoError(t, err)
	require.Empty(t, found.Results)
	stamped = false
	found, err = r.api.Alarms(r.operator, AlarmSearchCriteria{RaisedDuringSuppression: &stamped})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
}

func TestSuppressionPostgresStatusAndWindowPaginationVisibility(t *testing.T) {
	r, _ := newSuppressionPostgresRig(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	r.raise(t, "acme-pump", "a-hot", now)
	r.raise(t, "globex-pump", "g-hot", now)
	a, g := r.alarm(t, "a-hot"), r.alarm(t, "g-hot")
	status, err := r.api.AlarmSuppressionStatus(r.member("customer:acme"), []string{a.Token, g.Token, "missing"})
	require.NoError(t, err)
	require.Len(t, status, 1)
	require.Equal(t, a.Token, status[0].AlarmToken)
	require.NotNil(t, status[0].Match)
	status, err = r.api.AlarmSuppressionStatus(r.member("customer:globex"), []string{a.Token, g.Token})
	require.NoError(t, err)
	require.Len(t, status, 1)
	require.Equal(t, g.Token, status[0].AlarmToken)
	require.Nil(t, status[0].Match)
	// A mixed-target window has a deliberately stale permissive storage stamp, so
	// whole-target filtering (not just the SQL anchor filter) must precede pagination.
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		token   string
		targets []AlarmSuppressionTarget
		anchors rdb.Anchors
	}{
		{"second", []AlarmSuppressionTarget{{Type: "DEVICE", Token: "acme-pump"}}, rdb.Anchors{"customer:acme"}},
		{"mixed", []AlarmSuppressionTarget{{Type: "DEVICE", Token: "acme-pump"}, {Type: "DEVICE", Token: "globex-pump"}}, rdb.Anchors{"customer:acme"}},
	} {
		w := suppressionTestWindow()
		w.Token, w.Targets, w.ScopeAnchors = tc.token, tc.targets, tc.anchors
		w.StartTime, w.EndTime = &start, &end
		require.NoError(t, r.api.RDB.DB(r.operator).Create(&w).Error)
	}
	member := r.member("customer:acme")
	page, err := r.api.AlarmSuppressionWindowsSearch(member, AlarmSuppressionWindowSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1}})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Pagination.TotalRecords)
	require.Len(t, page.Results, 1)
	require.Equal(t, "second", page.Results[0].Token)
	page, err = r.api.AlarmSuppressionWindowsSearch(member, AlarmSuppressionWindowSearchCriteria{Pagination: rdb.Pagination{PageNumber: 2, PageSize: 1}})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Pagination.TotalRecords)
	require.Len(t, page.Results, 1)
	require.Equal(t, "service", page.Results[0].Token)
	windows, err := r.api.AlarmSuppressionWindowsByTokens(member, []string{"mixed", "service", "missing"})
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, "service", windows[0].Token)
	_, err = r.api.AlarmSuppressionsForDevice(member, "globex-pump", now)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// Exercise Postgres's jsonb candidate query through the real outbox and APIs.
func TestSuppressionPostgresOwnershipRefresh(t *testing.T) {
	r, _ := newSuppressionPostgresRig(t)
	pub := &captureOwnership{}
	r.api.DeviceOwnershipPublisher = pub
	_, err := r.api.RemoveEntityRelationship(r.operator, "acme-pump-to-acme")
	require.NoError(t, err)
	r.assign(t, "acme-pump", "globex")
	_, err = r.api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	rows, err := r.api.AlarmSuppressionWindows(r.member("customer:globex"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = r.api.AlarmSuppressionWindowByToken(r.member("customer:globex"), "service")
	require.NoError(t, err)
	rows, err = r.api.AlarmSuppressionWindows(r.member("customer:acme"))
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = r.api.AlarmSuppressionWindowByToken(r.member("customer:acme"), "service")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
