// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlRecorder is a gorm logger that keeps every statement the database was asked to run.
type sqlRecorder struct {
	logger.Interface
	mu   sync.Mutex
	sqls []string
}

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

func (r *sqlRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sqls
	r.sqls = nil
	return out
}

// find returns the recorded statements that read table and contain every fragment.
func find(sqls []string, table string, fragments ...string) []string {
	var out []string
nextSQL:
	for _, s := range sqls {
		if !strings.Contains(s, "FROM `"+table+"`") {
			continue
		}
		for _, f := range fragments {
			if !strings.Contains(s, f) {
				continue nextSQL
			}
		}
		out = append(out, s)
	}
	return out
}

func shapeFixture(t *testing.T) (*Api, *sqlRecorder, func(...string) context.Context) {
	t.Helper()
	rec := &sqlRecorder{Interface: logger.Discard}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Event{}, &EventAnchor{}, &MeasurementEvent{}, &LocationEvent{}, &AlertEvent{}))
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := func(scope ...string) context.Context {
		c := core.WithTenant(context.Background(), "tenant1")
		claims := &auth.Claims{TokenType: auth.TokenTypeAccess}
		if len(scope) > 0 {
			claims.ScopeAnchors = scope
		}
		return auth.WithClaims(c, claims)
	}
	rec.take()
	return api, rec, ctx
}

const (
	filterSubquery = "event_id IN (SELECT `event_id` FROM `event_anchors` WHERE (anchor_type = \"area\" AND anchor_token = \"floor-shared\") AND `event_anchors`.`tenant_id` = \"tenant1\")"
	outerScopeRule = "EXISTS (SELECT 1 FROM `event_anchors` AS `dc_scope_set` WHERE `dc_scope_set`.`tenant_id` = \"tenant1\" AND `dc_scope_set`.`event_id` = `measurement_events`.`event_id` AND ((`dc_scope_set`.`anchor_type` = \"customer\" AND `dc_scope_set`.`anchor_token` = \"acme\")))"
)

// The statements the REAL read methods emit for a scoped, area-anchored read, captured from
// the database logger rather than composed by hand: the filter subquery carries the tenant's
// predicate alone (it selects event keys, nothing it returns is shown), and the OUTER read
// keeps its own scope rule in the same statement. The unscoped control has the filter and no
// scope rule. A subquery that quietly grew a scope predicate would bring back the planner
// hazard it was dropped for; an outer read that lost its rule would leak, with the subquery
// alone never able to say so.
func TestTheRealAnchorFilteredReadsAreTheTenantFilterInsideTheOuterScopeRule(t *testing.T) {
	api, rec, ctx := shapeFixture(t)
	area, floor := "area", "floor-shared"
	crit := EventSearchCriteria{AnchorType: &area, AnchorToken: &floor,
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}

	_, err := api.MeasurementEvents(ctx("customer:acme"), crit)
	require.NoError(t, err)
	sqls := rec.take()
	reads := find(sqls, "measurement_events", filterSubquery, "`measurement_events`.`tenant_id` = \"tenant1\"", outerScopeRule)
	assert.NotEmpty(t, reads, "scoped MeasurementEvents: tenant filter subquery inside the outer rule; got %v", sqls)
	// the count and the page are the same filtered statement
	assert.Len(t, find(sqls, "measurement_events", "count(*)", outerScopeRule), 1, "the count carries the rule too")

	_, err = api.MeasurementEvents(ctx(), crit)
	require.NoError(t, err)
	plain := rec.take()
	assert.NotEmpty(t, find(plain, "measurement_events", filterSubquery), "control: unscoped keeps the filter")
	for _, s := range plain {
		assert.NotContains(t, s, "dc_scope_set", "control: unscoped reads carry no scope rule")
	}

	// the aggregation's scope relation, the other payload read
	raw := ctx("customer:acme")
	var out []MeasurementEvent
	require.NoError(t, api.rawMeasurementScope(raw, MeasurementAggregationCriteria{
		AnchorType: &area, AnchorToken: &floor}).Find(&out).Error)
	got := strings.Join(rec.take(), " ; ")
	assert.Contains(t, got, filterSubquery, "rawMeasurementScope: the tenant filter subquery")
	assert.Contains(t, got, outerScopeRule, "rawMeasurementScope: the outer scope rule")
}

// The base events read, for a scoped caller, is a correlated EXISTS on the event's full key
// (same rows as the key subquery; a plan difference, see eventAnchorFilter) and keeps its
// outer rule; an unscoped caller keeps the key subquery.
func TestTheRealBaseEventReadIsCorrelatedForAScopedCallerAndUnchangedForAnUnscopedOne(t *testing.T) {
	api, rec, ctx := shapeFixture(t)
	area, floor := "area", "floor-shared"
	crit := EventSearchCriteria{AnchorType: &area, AnchorToken: &floor,
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}

	_, err := api.Events(ctx("customer:acme"), crit)
	require.NoError(t, err)
	sqls := rec.take()
	assert.NotEmpty(t, find(sqls, "events",
		"EXISTS (SELECT 1 FROM `event_anchors` WHERE (anchor_type = \"area\" AND anchor_token = \"floor-shared\") AND (event_anchors.occurred_time = events.occurred_time AND event_anchors.event_id = events.event_id) AND `event_anchors`.`tenant_id` = \"tenant1\")",
		"dc_scope_set"), "scoped Events: correlated probe under the tenant, inside the outer rule; got %v", sqls)
	for _, s := range sqls {
		assert.NotContains(t, s, "(occurred_time, event_id) IN", "scoped: not the key subquery")
	}

	_, err = api.Events(ctx(), crit)
	require.NoError(t, err)
	assert.NotEmpty(t, find(rec.take(), "events",
		"(occurred_time, event_id) IN (SELECT occurred_time, event_id FROM `event_anchors` WHERE (anchor_type = \"area\" AND anchor_token = \"floor-shared\") AND `event_anchors`.`tenant_id` = \"tenant1\")"),
		"control: unscoped keeps the key subquery")
}

// The filter model holds device-derived rows and is only ever the inside of a key subquery:
// a scoped caller reading it directly is refused, not handed every customer's anchors.
func TestAScopedCallerCannotReadTheAnchorFilterModelDirectly(t *testing.T) {
	api, _, ctx := shapeFixture(t)
	var rows []eventAnchorKey
	err := api.RDB.DB(ctx("customer:acme")).Find(&rows).Error
	assert.ErrorIs(t, err, auth.ErrScopeUnsupported)
	assert.Empty(t, rows)
	var n int64
	assert.ErrorIs(t, api.RDB.DB(ctx("customer:acme")).Model(&eventAnchorKey{}).Count(&n).Error, auth.ErrScopeUnsupported)
	// control: the same model inside a subquery is what every anchor-filtered read uses
	// (the value tests), and an unscoped caller is not refused.
	assert.NoError(t, api.RDB.DB(ctx()).Find(&rows).Error)
}

// A scoped caller's anchor-filtered read is bounded: past the deadline it ends with a clean
// error naming the cause rather than running on. The deadline is shrunk so the test needs
// no slow query. The controls: the same read without an anchor, and the same anchored
// read by an unscoped caller, are not bounded (their cost is the caller's own to ask for).
func TestAScopedAnchorFilteredReadPastItsDeadlineEndsInACleanError(t *testing.T) {
	api, _, ctx := shapeFixture(t)
	old := scopedAnchorReadTimeout
	scopedAnchorReadTimeout = 20 * time.Millisecond
	t.Cleanup(func() { scopedAnchorReadTimeout = old })
	// sqlite ignores a context, so stand in for a slow Postgres query that pgx would cancel:
	// a statement that carries a deadline waits for it and fails with the context's error,
	// and one that carries none runs normally.
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:wait_for_deadline",
		func(tx *gorm.DB) {
			if _, ok := tx.Statement.Context.Deadline(); ok {
				<-tx.Statement.Context.Done()
				_ = tx.AddError(tx.Statement.Context.Err())
			}
		}))
	area, floor := "area", "floor-shared"
	anchored := EventSearchCriteria{AnchorType: &area, AnchorToken: &floor,
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}
	plain := EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}
	start, end := time.Now().Add(-time.Hour), time.Now()
	agg := MeasurementAggregationCriteria{StartTime: &start, EndTime: &end, IntervalSeconds: 60,
		AnchorType: &area, AnchorToken: &floor}

	scoped := ctx("customer:acme")
	_, err := api.Events(scoped, anchored)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "did not finish within")
	_, err = api.MeasurementEvents(scoped, anchored)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = api.LocationEvents(scoped, anchored)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = api.AlertEvents(scoped, anchored)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = api.BucketedMeasurements(scoped, agg)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = api.Events(scoped, plain)
	assert.NoError(t, err, "control: a scoped read with no anchor is not bounded here")
	_, err = api.Events(ctx(), anchored)
	assert.NoError(t, err, "control: an unscoped anchored read is not bounded")
}
