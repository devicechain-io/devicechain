// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The SHAPE of a scoped anchor-filtered read, pinned for the one reader who cannot see it
// fail in a result. The filter subquery carries the tenant's predicate ONLY (it selects event
// keys and nothing it returns is shown), so what keeps a scoped caller inside its own events
// is the OUTER read's rule, in the same statement. The two halves are asserted together:
// a subquery that quietly grew a scope predicate again would reintroduce the planner hazard
// it was dropped for (see EventAnchorKey), and an outer read that lost its rule would leak,
// with the subquery alone never able to say so.
func TestAScopedAnchorFilteredReadIsTheTenantFilterInsideTheOuterScopeRule(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Event{}, &EventAnchor{}, &MeasurementEvent{}))
	api := NewApi(&rdb.RdbManager{Database: db})

	tenant := core.WithTenant(context.Background(), "tenant1")
	scoped := auth.WithClaims(tenant, &auth.Claims{TokenType: auth.TokenTypeAccess,
		ScopeAnchors: []string{"customer:acme"}})
	unscoped := auth.WithClaims(tenant, &auth.Claims{TokenType: auth.TokenTypeAccess})

	read := func(ctx context.Context) string {
		return db.ToSQL(func(tx *gorm.DB) *gorm.DB {
			var out []MeasurementEvent
			return api.RDB.DB(ctx).Session(&gorm.Session{DryRun: true}).Model(&MeasurementEvent{}).
				Where("event_id IN (?)", api.anchorKeySubquery(ctx, "area", "floor-shared")).Find(&out)
		})
	}

	got := read(scoped)
	// the subquery: the requested anchor under the tenant, and no scope predicate of its own
	assert.Contains(t, got, "event_id IN (SELECT `event_id` FROM `event_anchors` WHERE (anchor_type = \"area\" AND anchor_token = \"floor-shared\") AND `event_anchors`.`tenant_id` = \"tenant1\")",
		"the filter subquery is the requested anchor under the tenant, and nothing else")
	// the outer read: tenant plus the caller's scope, as an EXISTS over the anchors of the event
	assert.Contains(t, got, "`measurement_events`.`tenant_id` = \"tenant1\"")
	assert.Contains(t, got, "EXISTS (SELECT 1 FROM `event_anchors` AS `dc_scope_set` WHERE `dc_scope_set`.`tenant_id` = \"tenant1\" AND `dc_scope_set`.`event_id` = `measurement_events`.`event_id` AND ((`dc_scope_set`.`anchor_type` = \"customer\" AND `dc_scope_set`.`anchor_token` = \"acme\")))",
		"the outer read keeps its own scope rule")

	// control: an unscoped caller's statement has the filter and the tenant, and no scope rule
	plain := read(unscoped)
	assert.Contains(t, plain, "`event_anchors`.`tenant_id` = \"tenant1\"")
	assert.NotContains(t, plain, "dc_scope_set", "unscoped reads are unchanged")
}

// The base event table's filter is a CORRELATED EXISTS on the event's full key for a scoped
// caller and the key subquery, as before, for an unscoped one. Same rows either way; the
// shape is pinned because the difference is a plan: see eventAnchorFilter.
func TestTheBaseEventFilterIsCorrelatedForAScopedCallerAndUnchangedForAnUnscopedOne(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Event{}, &EventAnchor{}))
	api := NewApi(&rdb.RdbManager{Database: db})
	area, floor := "area", "floor-shared"
	criteria := EventSearchCriteria{AnchorType: &area, AnchorToken: &floor}

	tenant := core.WithTenant(context.Background(), "tenant1")
	scoped := auth.WithClaims(tenant, &auth.Claims{TokenType: auth.TokenTypeAccess,
		ScopeAnchors: []string{"customer:acme"}})
	unscoped := auth.WithClaims(tenant, &auth.Claims{TokenType: auth.TokenTypeAccess})
	read := func(ctx context.Context) string {
		return db.ToSQL(func(tx *gorm.DB) *gorm.DB {
			var out []Event
			q := api.RDB.DB(ctx).Session(&gorm.Session{DryRun: true}).Model(&Event{})
			return api.eventAnchorFilter(ctx, criteria, q).Find(&out)
		})
	}

	got := read(scoped)
	assert.Contains(t, got, "EXISTS (SELECT 1 FROM `event_anchors` WHERE (anchor_type = \"area\" AND anchor_token = \"floor-shared\") AND (event_anchors.occurred_time = events.occurred_time AND event_anchors.event_id = events.event_id) AND `event_anchors`.`tenant_id` = \"tenant1\")",
		"scoped: a correlated probe on the event's full key, under the tenant")
	assert.NotContains(t, got, "(occurred_time, event_id) IN", "scoped: not the key subquery")
	assert.Contains(t, got, "dc_scope_set", "scoped: the outer scope rule is still in the statement")

	plain := read(unscoped)
	assert.Contains(t, plain, "(occurred_time, event_id) IN (SELECT occurred_time, event_id FROM `event_anchors` WHERE (anchor_type = \"area\" AND anchor_token = \"floor-shared\") AND `event_anchors`.`tenant_id` = \"tenant1\")",
		"control: unscoped keeps the key subquery")
}
