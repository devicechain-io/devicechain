// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Uses the existing real-Postgres harness: text[] visibility, audit callbacks and FK
// replay are verified on the same engine as deployment, without adding a new harness.
func TestSuppressionPostgresScopeAndCycleCascade(t *testing.T) {
	r := newScopeAlarmRig(t)
	require.NoError(t, schema.NewAlarmSuppressionSchema().Migrate(r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))))
	require.NoError(t, schema.NewAlarmSuppressionSchema().Migrate(r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))))
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{"DEVICE", "acme-pump"}}
	member := r.member("customer:acme")
	created, err := r.api.CreateAlarmSuppressionWindow(member, &w)
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"customer:acme"}, created.ScopeAnchors)
	visible, err := r.api.AlarmSuppressionWindows(member)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	visible, err = r.api.AlarmSuppressionWindows(r.member("customer:globex"))
	require.NoError(t, err)
	require.Empty(t, visible)
	targets := []AlarmSuppressionTarget{{"DEVICE", "globex-pump"}}
	_, err = r.api.UpdateAlarmSuppressionWindow(member, w.Token, &AlarmSuppressionUpdateRequest{Targets: OptionalAlarmSuppressionTargets{Set: true, Value: targets}})
	require.ErrorIs(t, err, auth.ErrForbidden)
	reason := "extended work"
	updated, err := r.api.UpdateAlarmSuppressionWindow(member, w.Token, &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Version)
	_, err = r.api.EndAlarmSuppressionWindow(member, w.Token)
	require.NoError(t, err)
	_, err = r.api.DeleteAlarmSuppressionWindow(member, w.Token)
	require.NoError(t, err)
	at := time.Now().UTC()
	r.raise(t, "acme-pump", "hot", at)
	alarm := r.alarm(t, "hot")
	stamp := AlarmCycleSuppression{AlarmId: alarm.ID, RaisedTime: alarm.RaisedTime, AlarmToken: alarm.Token, WindowToken: w.Token, ScopeAnchors: alarm.ScopeAnchors}
	require.NoError(t, r.api.RDB.DB(r.operator).Create(&stamp).Error)
	require.NoError(t, r.api.RDB.DB(r.operator).Unscoped().Delete(alarm).Error)
	var count int64
	require.NoError(t, r.api.RDB.DB(r.operator).Model(&AlarmCycleSuppression{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestSuppressionPostgresReadVisibilityAfterTransfer(t *testing.T) {
	r := newScopeAlarmRig(t)
	require.NoError(t, schema.NewAlarmSuppressionSchema().Migrate(r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))))
	a, b := r.member("customer:acme"), r.member("customer:globex")
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{"DEVICE", "acme-pump"}}
	_, err := r.api.CreateAlarmSuppressionWindow(a, &w)
	require.NoError(t, err)
	assertVisible := func(ctx context.Context, token string, want bool) {
		t.Helper()
		rows, err := r.api.AlarmSuppressionWindows(ctx)
		require.NoError(t, err)
		found := false
		for _, row := range rows {
			found = found || row.Token == token
		}
		require.Equal(t, want, found)
		row, err := r.api.AlarmSuppressionWindowByToken(ctx, token)
		if want {
			require.NoError(t, err)
			require.NotNil(t, row)
		} else {
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		}
	}
	assertVisible(a, w.Token, true)
	require.NoError(t, r.api.RDB.DB(r.operator).Where("token = ?", "acme-pump-to-acme").Delete(&EntityRelationship{}).Error)
	r.assign(t, "acme-pump", "globex")
	assertVisible(a, w.Token, false)
	assertVisible(b, w.Token, false) // The original A visibility stamp remains the first filter.
	w.Token = "b-created"
	_, err = r.api.CreateAlarmSuppressionWindow(b, &w)
	require.NoError(t, err)
	assertVisible(b, w.Token, true)
	assertVisible(a, w.Token, false)
	w.Token = "admin-created"
	_, err = r.api.CreateAlarmSuppressionWindow(r.operator, &w)
	require.NoError(t, err)
	assertVisible(b, w.Token, true)
	assertVisible(a, w.Token, false)
}

func TestSuppressionPostgresScopedMutationSingleConnection(t *testing.T) {
	r := newScopeAlarmRig(t)
	require.NoError(t, schema.NewAlarmSuppressionSchema().Migrate(r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))))
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{"DEVICE", "acme-pump"}}
	_, err := r.api.CreateAlarmSuppressionWindow(r.member("customer:acme"), &w)
	require.NoError(t, err)
	db, err := r.api.RDB.Database.DB()
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.SetMaxOpenConns(0)
	ctx, cancel := context.WithTimeout(r.member("customer:acme"), 3*time.Second)
	defer cancel()
	reason := "scoped edit"
	_, err = r.api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Reason: dcgraphql.OptionalStringOf(reason)})
	require.NoError(t, err)
}

func TestSuppressionPostgresEvaluatorQueries(t *testing.T) {
	r := newScopeAlarmRig(t)
	require.NoError(t, schema.NewAlarmSuppressionSchema().Migrate(r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))))
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-2*time.Hour), now.Add(time.Hour)
	w := suppressionTestWindow()
	w.StartTime, w.EndTime = &start, &end
	w.Targets = []AlarmSuppressionTarget{{"DEVICE", "acme-pump"}}
	w.AlarmKeys = []string{"hot"}
	_, err := r.api.CreateAlarmSuppressionWindow(r.operator, &w)
	require.NoError(t, err)
	recurring := suppressionRecurringWindow()
	recurring.Token = "recurring"
	recurring.Targets = w.Targets
	recurring.AlarmKeys = []string{"recurring-key"}
	recurring.Timezone = sql.NullString{String: "UTC", Valid: true}
	recurring.Slots = datatypes.JSON(fmt.Sprintf(`[{"day":%q,"start":"00:00","durationMinutes":1440}]`, strings.ToUpper(now.Weekday().String())))
	_, err = r.api.CreateAlarmSuppressionWindow(r.operator, &recurring)
	require.NoError(t, err)
	ended := w
	ended.Token, ended.AlarmKeys = "ended", []string{"ended-key"}
	created, err := r.api.CreateAlarmSuppressionWindow(r.operator, &ended)
	require.NoError(t, err)
	_, err = r.api.EndAlarmSuppressionWindow(r.operator, ended.Token)
	require.NoError(t, err)
	endedAt := now.Add(-30 * time.Minute)
	require.NoError(t, r.api.RDB.DB(r.operator).Model(created).Update("ended_at", endedAt).Error)
	r.api.invalidateSuppressionWindows(r.operator)
	assertMatch := func(key string, at time.Time, token string) {
		t.Helper()
		match, err := r.api.SuppressionFor(r.operator, r.devices["acme-pump"], key, "MINOR", at)
		require.NoError(t, err)
		if token == "" {
			require.Nil(t, match)
		} else {
			require.NotNil(t, match)
			require.Equal(t, token, match.WindowToken)
		}
	}
	assertMatch("hot", now, w.Token) // signature aggregate and jsonb scan through the cache path
	assertMatch("other-key", now, "")
	assertMatch("recurring-key", now, recurring.Token)
	windows, err := r.api.suppressionWindows(r.operator)
	require.NoError(t, err)
	require.Len(t, windows, 2)
	for _, window := range windows {
		require.Equal(t, w.Targets, window.window.Targets)
		if window.window.Token == w.Token {
			require.Equal(t, w.AlarmKeys, window.window.AlarmKeys)
		} else {
			require.Equal(t, recurring.Token, window.window.Token)
			require.Equal(t, recurring.AlarmKeys, window.window.AlarmKeys)
		}
	}
	assertMatch("ended-key", endedAt.Add(-time.Minute), ended.Token) // historical OR query
	assertMatch("ended-key", endedAt.Add(time.Minute), "")
}
