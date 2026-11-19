// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSuppressionEndAuthorityAndVisibility(t *testing.T) {
	for _, broad := range []bool{false, true} {
		t.Run(map[bool]string{false: "narrow", true: "broad"}[broad], func(t *testing.T) {
			api, ctx, _ := newSuppressionTestApi(t)
			w := suppressionTestWindow()
			if broad {
				w.Targets[0].Type = "DEVICE_TYPE"
			}
			created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
			require.NoError(t, err)
			noWrite := auth.WithClaims(ctx, &auth.Claims{Tenant: "acme", TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.AlarmRead)}})
			_, err = api.EndAlarmSuppressionWindow(noWrite, created.Token)
			require.ErrorIs(t, err, auth.ErrForbidden)
			ended, err := api.EndAlarmSuppressionWindow(suppressionCaller(ctx, false), created.Token)
			require.NoError(t, err)
			again, err := api.EndAlarmSuppressionWindow(suppressionCaller(ctx, false), created.Token)
			require.NoError(t, err)
			require.Equal(t, ended.EndedAt, again.EndedAt)
			_, err = api.DeleteAlarmSuppressionWindow(suppressionCaller(ctx, false), created.Token)
			if broad {
				require.ErrorIs(t, err, auth.ErrForbidden)
			} else {
				require.NoError(t, err)
			}
		})
	}
	api, ctx, id := newSuppressionTestApi(t)
	suppressionReadSQLiteOverlap(t, api.RDB.Database)
	rt := seedRelType(t, api, ctx, "acme", "owns", true)
	seedEdge(t, api, ctx, "acme", "owner", id, rt, "customer", "A")
	w := suppressionTestWindow()
	w.Targets = append(w.Targets, AlarmSuppressionTarget{Type: "CUSTOMER", Token: "B"})
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	// Deliberately permissive storage stamp: the whole-target check must refuse End.
	require.NoError(t, api.RDB.DB(ctx).Model(created).Update("scope_anchors", rdb.Anchors{"customer:A"}).Error)
	_, err = api.EndAlarmSuppressionWindow(suppressionCaller(ctx, false, "customer:A"), created.Token)
	require.ErrorIs(t, err, auth.ErrForbidden)
}

func suppressionOwnershipRig(t *testing.T) (*Api, context.Context, *captureOwnership) {
	t.Helper()
	api, ctx, pub := ownershipFixture(t)
	pub.api = nil
	suppressionReadSQLiteOverlap(t, api.RDB.Database)
	assignDozer(t, api, ctx, "acme-mining")
	return api, ctx, pub
}

func TestSuppressionOwnershipDrainMovesVisibility(t *testing.T) {
	api, ctx, _ := suppressionOwnershipRig(t)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"customer:acme-mining"}, created.ScopeAnchors)
	// Prime the tenant cache before the ownership write.
	_, err = api.SuppressionFor(ctx, deviceIdByToken(t, api, ctx, "dozer-01"), "hot", "MINOR", *w.StartTime)
	require.NoError(t, err)
	_, err = api.RemoveEntityRelationship(ctx, "e-acme-mining")
	require.NoError(t, err)
	assignDozer(t, api, ctx, "globex-mining")
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(ctx)).AutoMigrate(&rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	for _, tc := range []struct {
		anchor  string
		visible bool
	}{{"customer:globex-mining", true}, {"customer:acme-mining", false}} {
		caller := suppressionCaller(ctx, false, tc.anchor)
		rows, err := api.AlarmSuppressionWindows(caller)
		require.NoError(t, err)
		if tc.visible {
			require.Len(t, rows, 1)
		} else {
			require.Empty(t, rows)
		}
		_, err = api.AlarmSuppressionWindowByToken(caller, created.Token)
		if tc.visible {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		}
	}
	refreshed, err := api.AlarmSuppressionWindowByToken(ctx, created.Token)
	require.NoError(t, err)
	require.Equal(t, created.Version+1, refreshed.Version)
	require.Empty(t, queuedOwnership(t, api))
	var journal []rdb.AuditEvent
	require.NoError(t, api.RDB.DB(ctx).Where("table_name = ? AND operation = ?", "alarm_suppression_windows", "update").Find(&journal).Error)
	require.Len(t, journal, 1)
	require.EqualValues(t, 1, journal[0].RowsAffected)
	api.suppressionCache.Lock()
	_, cached := api.suppressionCache.tenants["acme"]
	api.suppressionCache.Unlock()
	require.False(t, cached, "committed refresh evicts the tenant cache")
}

func TestSuppressionOwnershipIntersectionAndHistory(t *testing.T) {
	for _, kind := range []string{"multi", "creator", "ended", "expired", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			api, ctx, _ := suppressionOwnershipRig(t)
			w := suppressionTestWindow()
			w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
			caller := ctx
			if kind == "multi" {
				w.Targets = append(w.Targets, AlarmSuppressionTarget{Type: "CUSTOMER", Token: "acme-mining"})
			}
			if kind == "creator" {
				caller = suppressionCaller(ctx, false, "customer:acme-mining")
			}
			created, err := api.CreateAlarmSuppressionWindow(caller, &w)
			require.NoError(t, err)
			if kind == "ended" {
				_, err = api.EndAlarmSuppressionWindow(ctx, created.Token)
				require.NoError(t, err)
			}
			if kind == "expired" {
				require.NoError(t, api.RDB.DB(ctx).Model(created).Update("end_time", w.StartTime).Error)
			}
			if kind == "deleted" {
				_, err = api.DeleteAlarmSuppressionWindow(ctx, created.Token)
				require.NoError(t, err)
			}
			_, err = api.RemoveEntityRelationship(ctx, "e-acme-mining")
			require.NoError(t, err)
			assignDozer(t, api, ctx, "globex-mining")
			var before AlarmSuppressionWindow
			require.NoError(t, api.RDB.DB(ctx).Unscoped().First(&before, created.ID).Error)
			_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
			require.NoError(t, err)
			var after AlarmSuppressionWindow
			require.NoError(t, api.RDB.DB(ctx).Unscoped().First(&after, created.ID).Error)
			switch kind {
			case "multi", "creator":
				require.Empty(t, after.ScopeAnchors)
			case "deleted":
				require.Equal(t, before.Version, after.Version)
				require.Equal(t, before.ScopeAnchors, after.ScopeAnchors)
			default:
				require.Equal(t, rdb.Anchors{"customer:globex-mining"}, after.ScopeAnchors)
			}
		})
	}
}

func TestSuppressionOwnershipCASRecomputesConcurrentEdit(t *testing.T) {
	api, ctx, _ := suppressionOwnershipRig(t)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	_, err = api.RemoveEntityRelationship(ctx, "e-acme-mining")
	require.NoError(t, err)
	assignDozer(t, api, ctx, "globex-mining")
	raced := false
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("test:suppression-race", func(tx *gorm.DB) {
		if raced || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "AlarmSuppressionWindow" {
			return
		}
		raced = true
		// A real competing user edit replaces the device target with customer A.
		_, err := api.UpdateAlarmSuppressionWindow(ctx, created.Token, &AlarmSuppressionUpdateRequest{Targets: OptionalAlarmSuppressionTargets{Set: true, Value: []AlarmSuppressionTarget{{Type: "CUSTOMER", Token: "acme-mining"}}}})
		require.NoError(t, err)
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Query().Remove("test:suppression-race") })
	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.True(t, raced)
	after, err := api.AlarmSuppressionWindowByToken(ctx, created.Token)
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"customer:acme-mining"}, after.ScopeAnchors)
	require.Equal(t, created.Version+1, after.Version, "refresh must not overwrite the winning edit's stamp or version")
	require.Equal(t, []AlarmSuppressionTarget{{Type: "CUSTOMER", Token: "acme-mining"}}, []AlarmSuppressionTarget(after.Targets))
}

func TestSuppressionOwnershipFailureRetainsOutbox(t *testing.T) {
	api, ctx, pub := suppressionOwnershipRig(t)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Model(created).Update("scope_anchors", rdb.Anchors{}).Error)
	boom := errors.New("refresh refused")
	require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register("test:refresh-fail", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AlarmSuppressionWindow" {
			tx.AddError(boom)
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Update().Remove("test:refresh-fail") })
	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.ErrorContains(t, err, boom.Error())
	require.Len(t, queuedOwnership(t, api), 1)
	require.Zero(t, pub.attempts)
}

func TestSuppressionOwnershipNoMatchingWindowsIsReadOnly(t *testing.T) {
	api, ctx, _ := suppressionOwnershipRig(t)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "CUSTOMER", Token: "acme-mining"}}
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	queries, writes := 0, 0
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:refresh-query", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AlarmSuppressionWindow" {
			queries++
		}
	}))
	require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register("test:refresh-write", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AlarmSuppressionWindow" {
			writes++
		}
	}))
	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, 1, queries)
	require.Zero(t, writes)
}

func TestSuppressionOwnershipPersistentCASConflictKeepsPage(t *testing.T) {
	api, ctx, pub := suppressionOwnershipRig(t)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	_, err = api.RemoveEntityRelationship(ctx, "e-acme-mining")
	require.NoError(t, err)
	assignDozer(t, api, ctx, "globex-mining")
	races := 0
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("test:refresh-conflicts", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "AlarmSuppressionWindow" {
			return
		}
		races++
		// Advance the version after every read, as continuously competing editors do.
		require.NoError(t, api.RDB.DB(ctx).Model(&AlarmSuppressionWindow{}).Where("id = ?", created.ID).UpdateColumn("version", gorm.Expr("version + 1")).Error)
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Query().Remove("test:refresh-conflicts") })
	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.ErrorContains(t, err, "changed concurrently")
	require.Equal(t, 3, races, "one initial read and two fresh reads, then leave the page queued")
	require.Len(t, queuedOwnership(t, api), 1)
	require.Zero(t, pub.attempts)
}

func TestSuppressionOwnershipAccessGroupRefresh(t *testing.T) {
	api, ctx, pub := accessFixture(t)
	ctx = suppressionCaller(ctx, true)
	pub.api = nil
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "d-arid"}}
	created, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	for _, enabled := range []bool{true, false} {
		_, err = api.SetEntityGroupAccessScope(ctx, "arid", enabled)
		require.NoError(t, err)
		_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
		require.NoError(t, err)
		after, err := api.AlarmSuppressionWindowByToken(ctx, created.Token)
		require.NoError(t, err)
		if enabled {
			require.Equal(t, rdb.Anchors{"group:arid"}, after.ScopeAnchors)
		} else {
			require.Empty(t, after.ScopeAnchors)
		}
	}
}
