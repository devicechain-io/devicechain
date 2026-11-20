// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
		_, windowRead := tx.Statement.Dest.(*AlarmSuppressionWindow)
		if raced || !windowRead {
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

func TestSuppressionOwnershipFailureDoesNotBlockPublication(t *testing.T) {
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
	failed := testutil.ToFloat64(suppressionRefresh.WithLabelValues("failed"))
	res, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, 1, res.Published)
	require.Empty(t, queuedOwnership(t, api))
	require.Equal(t, 1, pub.attempts)
	require.Equal(t, failed+1, testutil.ToFloat64(suppressionRefresh.WithLabelValues("failed")))
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

func TestSuppressionOwnershipPersistentCASConflictPublishesPage(t *testing.T) {
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
		if _, windowRead := tx.Statement.Dest.(*AlarmSuppressionWindow); !windowRead {
			return
		}
		races++
		// Advance the version after every read, as continuously competing editors do.
		require.NoError(t, api.RDB.DB(ctx).Model(&AlarmSuppressionWindow{}).Where("id = ?", created.ID).UpdateColumn("version", gorm.Expr("version + 1")).Error)
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Query().Remove("test:refresh-conflicts") })
	failed := testutil.ToFloat64(suppressionRefresh.WithLabelValues("failed"))
	res, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, 3, races, "one initial read and two fresh reads, then publish despite the conflict")
	require.Equal(t, 1, res.Published)
	require.Empty(t, queuedOwnership(t, api))
	require.Equal(t, 1, pub.attempts)
	require.Equal(t, []string{"customer:globex-mining"}, pub.last().Anchors)
	require.Equal(t, failed+1, testutil.ToFloat64(suppressionRefresh.WithLabelValues("failed")))
	require.NoError(t, api.RDB.Database.Callback().Query().Remove("test:refresh-conflicts"))
	after, err := api.AlarmSuppressionWindowByToken(ctx, created.Token)
	require.NoError(t, err)
	require.Equal(t, created.ScopeAnchors, after.ScopeAnchors, "a failed CAS keeps the old visibility stamp")
	for _, anchor := range []string{"customer:acme-mining", "customer:globex-mining"} {
		rows, err := api.AlarmSuppressionWindows(suppressionCaller(ctx, false, anchor))
		require.NoError(t, err)
		require.Empty(t, rows, "stale stamp never exposes the window to the former owner and hides it from the new owner")
		_, err = api.AlarmSuppressionWindowByToken(suppressionCaller(ctx, false, anchor), created.Token)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
}

func TestSuppressionOwnershipMalformedWindowDoesNotBlockBacklog(t *testing.T) {
	for _, tc := range []struct {
		name, column, value string
		failures            float64
	}{
		{"alarm_keys", "alarm_keys", `{}`, 1},
		{"targets_element", "targets", `[{"type":"DEVICE","token":"dozer-01"},{"type":"DEVICE","token":7}]`, 1},
		{"targets_object", "targets", `{"type":"DEVICE","token":"dozer-01"}`, 0},
		{"targets_scalar", "targets", `"dozer-01"`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, pub := suppressionOwnershipRig(t)
			w := suppressionTestWindow()
			w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
			bad, err := api.CreateAlarmSuppressionWindow(ctx, &w)
			require.NoError(t, err)
			// Corrupt a row before the good candidate, using valid JSON of the wrong shape.
			require.NoError(t, api.RDB.DB(ctx).Model(bad).UpdateColumn(tc.column, gorm.Expr("?", tc.value)).Error)
			w.Token = "good"
			good, err := api.CreateAlarmSuppressionWindow(ctx, &w)
			require.NoError(t, err)
			_, err = api.RemoveEntityRelationship(ctx, "e-acme-mining")
			require.NoError(t, err)
			assignDozer(t, api, ctx, "globex-mining")
			ids := []uint{deviceIdByToken(t, api, ctx, "dozer-01")}
			for i := 0; i < 4; i++ {
				d := Device{TokenReference: rdb.TokenReference{Token: fmt.Sprintf("backlog-%d", i)}}
				require.NoError(t, api.RDB.DB(ctx).Create(&d).Error)
				ids = append(ids, d.ID)
			}
			require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
				_, err := queueOwnershipOnTx(tx, ids)
				return err
			}))
			failed := testutil.ToFloat64(suppressionRefresh.WithLabelValues("failed"))
			published, removed := 0, 0
			// Five devices exceed the two-row page: corruption on page one must
			// neither retain that page nor starve the remainder of the backlog.
			for pass := 0; pass < 3; pass++ {
				res, err := api.DrainOwnershipFacts(context.Background(), 2)
				require.NoError(t, err)
				published, removed = published+res.Published, removed+res.Removed
			}
			require.Equal(t, 5, published)
			require.Equal(t, 5, removed)
			require.Empty(t, queuedOwnership(t, api))
			require.Len(t, pub.facts, 5)
			seen := map[uint64]bool{}
			for _, fact := range pub.facts {
				require.False(t, seen[fact.DeviceIncarnation])
				seen[fact.DeviceIncarnation] = true
				if fact.DeviceToken == "dozer-01" {
					require.Equal(t, []string{"customer:globex-mining"}, fact.Anchors)
				}
			}
			after, err := api.AlarmSuppressionWindowByToken(ctx, good.Token)
			require.NoError(t, err)
			require.Equal(t, rdb.Anchors{"customer:globex-mining"}, after.ScopeAnchors)
			require.Equal(t, good.Version+1, after.Version)
			require.Equal(t, failed+tc.failures, testutil.ToFloat64(suppressionRefresh.WithLabelValues("failed")))
		})
	}
}

func TestSuppressionOwnershipAnchorQueryCountIsBatched(t *testing.T) {
	api, ctx, _ := suppressionOwnershipRig(t)
	w := suppressionTestWindow()
	w.Targets = []AlarmSuppressionTarget{{Type: "DEVICE", Token: "dozer-01"}}
	for i := 1; i < 10; i++ {
		d := Device{TokenReference: rdb.TokenReference{Token: fmt.Sprintf("shared-%d", i)}}
		require.NoError(t, api.RDB.DB(ctx).Create(&d).Error)
		w.Targets = append(w.Targets, AlarmSuppressionTarget{Type: "DEVICE", Token: d.Token})
	}
	_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
	require.NoError(t, err)
	statements, windowReads := 0, 0
	count := func(tx *gorm.DB) {
		// Per-window decoding is deliberately isolated. Only ownership/anchor
		// reads must be constant; the required window loads are linear.
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "AlarmSuppressionWindow" {
			windowReads++
		} else {
			statements++
		}
	}
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:anchor-queries", count))
	require.NoError(t, api.RDB.Database.Callback().Row().Before("gorm:row").Register("test:anchor-rows", count))
	t.Cleanup(func() {
		_ = api.RDB.Database.Callback().Query().Remove("test:anchor-queries")
		_ = api.RDB.Database.Callback().Row().Remove("test:anchor-rows")
	})
	facts := []*DeviceOwnershipChangedEvent{{DeviceToken: "dozer-01"}}
	require.NoError(t, api.refreshSuppressionOwnership(ctx, facts))
	one := statements
	require.Positive(t, one)
	require.Equal(t, 2, windowReads, "one candidate query and one isolated window load")
	for i := 1; i < 50; i++ {
		w.Token = fmt.Sprintf("window-%d", i)
		_, err := api.CreateAlarmSuppressionWindow(ctx, &w)
		require.NoError(t, err)
	}
	statements, windowReads = 0, 0
	require.NoError(t, api.refreshSuppressionOwnership(ctx, facts))
	require.Equal(t, one, statements, "50 windows sharing 10 DEVICE targets must read anchors once for the batch")
	require.Equal(t, 51, windowReads)
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
