// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
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
	_, err = r.api.UpdateAlarmSuppressionWindow(member, w.Token, &AlarmSuppressionUpdateRequest{Targets: &targets})
	require.ErrorIs(t, err, auth.ErrForbidden)
	reason := "extended work"
	updated, err := r.api.UpdateAlarmSuppressionWindow(member, w.Token, &AlarmSuppressionUpdateRequest{Reason: &reason})
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
	_, err = r.api.UpdateAlarmSuppressionWindow(ctx, w.Token, &AlarmSuppressionUpdateRequest{Reason: &reason})
	require.NoError(t, err)
}
