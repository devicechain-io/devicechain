// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func TestPositionCheckRecognizesScopedLocationGrant(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	for _, location := range []bool{false, true} {
		authorities := []string{string(auth.NotificationRead)}
		if location {
			authorities = append(authorities, string(auth.LocationRead))
		}
		caller := auth.WithClaims(ctx, &auth.Claims{Authorities: authorities, ScopeAnchors: []string{"customer:acme"}})
		// Judge only the position check here. The independent tenant-scope callback
		// still refuses these tenant-wide notification tables to scoped members.
		filtered := notificationHistoryRead(api.RDB.DB(caller), false)
		_, withheld := filtered.Statement.Clauses["WHERE"]
		require.Equal(t, !location, withheld)
	}
}

// Exercise the same predicates through the model API as well as GraphQL: the
// mutation harness tests the package owning the mutated implementation file.
func TestNotificationHistoryReadPredicates(t *testing.T) {
	api := newTestApi(t)
	api.PositionOracle = positionOracleFunc(func(context.Context, []string) (map[string]bool, error) { return map[string]bool{"plain": true}, nil })
	ctx := tenantCtx("A")
	yes, no := true, false
	for token, flag := range map[string]*bool{"geo": &yes, "plain": &no, "unknown": nil} {
		require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{AlarmToken: token, AlarmKey: "k", PositionDerived: flag}).Error)
		require.NoError(t, api.RecordNotificationAttempt(ctx, &NotificationAttempt{AlarmToken: token}))
	}
	// A false verdict in another tenant cannot grant access to this tenant's alarm.
	require.NoError(t, api.RDB.DB(tenantCtx("B")).Create(&NotificationState{AlarmToken: "geo", AlarmKey: "k", PositionDerived: &no}).Error)
	require.NoError(t, api.RecordNotificationAttempt(ctx, &NotificationAttempt{AlarmToken: "orphan"}))
	caller := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
	states, err := api.NotificationStatesByAlarmToken(caller, []string{"geo", "plain", "unknown"})
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.Equal(t, "plain", states[0].AlarmToken)
	page := rdb.Pagination{PageNumber: 1, PageSize: 10}
	listed, err := api.NotificationStates(caller, NotificationStateSearchCriteria{Pagination: page})
	require.NoError(t, err)
	require.Len(t, listed.Results, 1)
	require.Equal(t, "plain", listed.Results[0].AlarmToken)
	attempts, err := api.NotificationAttempts(caller, NotificationAttemptSearchCriteria{Pagination: page})
	require.NoError(t, err)
	require.Len(t, attempts.Results, 1)
	require.Equal(t, "plain", attempts.Results[0].AlarmToken)
}

func TestNotificationPositionUpdatesFailClosedAndStayTrue(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	yes, no := true, false
	require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{AlarmToken: "a", AlarmKey: "k", PositionDerived: &no}).Error)
	for _, verdict := range []*bool{nil, &no, &yes} {
		require.NoError(t, api.RecordAlarmPosition(ctx, "a", verdict))
		rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, verdict, rows[0].PositionDerived)
	}
	for _, verdict := range []*bool{&no, nil} {
		require.NoError(t, api.RecordAlarmPosition(ctx, "a", verdict))
		rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a"})
		require.NoError(t, err)
		require.Equal(t, &yes, rows[0].PositionDerived)
	}
}

// The pre-dispatch classification may race a first insert. The locked lifecycle
// fold must also merge the verdict, even on stale or already-stamped events.
func TestOldCycleStillClassifiesNotificationHistory(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		api := newTestApi(t)
		ctx := tenantCtx("A")
		yes, no := true, false
		at := time.Now().UTC()
		require.NoError(t, api.MarkCleared(ctx, "a", "k", "MAJOR", at, at, &no))
		if terminal {
			require.NoError(t, api.MarkAcknowledged(ctx, "a", "k", "MAJOR", at.Add(-time.Hour), at, &yes))
		} else {
			_, err := api.RecordPagedFrom(ctx, "a", "k", "MAJOR", at.Add(-time.Hour), at, nil,
				AlarmSource{PositionObserved: true, PositionDerived: &yes}, at, at)
			require.NoError(t, err)
		}
		rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, &yes, rows[0].PositionDerived)
	}
}
