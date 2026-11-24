// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Run with the existing DC_IT_PG* TimescaleDB harness and -tags integration.
// mcp-gate's fixed, untagged gates cannot execute these Postgres checks.
func TestIntegrationAnalyticsGenerationConcurrentReconcilers(t *testing.T) {
	mgr := newPostgresManager(t, "dcanalyticsgenerationraceit")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := mgr.DB(core.WithSystemContext(ctx))
	for _, firstGeneration := range []int{2, 1} {
		t.Run(fmt.Sprintf("generation_%d_first", firstGeneration), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			require.NoError(t, db.Exec(`UPDATE "event-management".analytics_surface_generation SET generation = 0 WHERE id = 1`).Error)
			require.NoError(t, db.Exec(`DROP VIEW IF EXISTS analytics.generation_probe`).Error)
			entered, release := make(chan struct{}), make(chan struct{})
			writeSurface := func(tx *gorm.DB, generation int) error {
				// Both versions can replace this view. The view and function must
				// both finish at generation 2 even when the older version runs last.
				if err := tx.Exec(fmt.Sprintf(`CREATE OR REPLACE VIEW analytics.generation_probe AS SELECT %d AS generation`, generation)).Error; err != nil {
					return err
				}
				return tx.Exec(fmt.Sprintf(`CREATE OR REPLACE FUNCTION analytics.generation_probe() RETURNS integer LANGUAGE sql AS 'SELECT %d'`, generation)).Error
			}
			first, second := make(chan error, 1), make(chan error, 1)
			firstDone, secondDone := make(chan struct{}), make(chan struct{})
			defer func() {
				cancel()
				select {
				case <-release:
				default:
					close(release)
				}
				<-firstDone
				<-secondDone
			}()
			go func() {
				defer close(firstDone)
				first <- withAnalyticsGeneration(ctx, mgr, firstGeneration, newTestAnalyticsMetrics(), func(tx *gorm.DB) error {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					return writeSurface(tx, firstGeneration)
				})
			}()
			metrics := newTestAnalyticsMetrics()
			go func() {
				defer close(secondDone)
				// The first callback must enter before the second even tries.
				select {
				case <-entered:
				case <-ctx.Done():
					second <- ctx.Err()
					return
				}
				second <- withAnalyticsGeneration(ctx, mgr, 3-firstGeneration, metrics, func(tx *gorm.DB) error {
					return writeSurface(tx, 3-firstGeneration)
				})
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Observe real contention, rather than assuming that starting a
			// goroutine means it reached the lock before the first was released.
			key := uint64(rdb.AdvisoryLockKey(analyticsLockName))
			require.Eventually(t, func() bool {
				var waiting int64
				err := db.Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
					AND classid = ? AND objid = ? AND objsubid = 1 AND NOT granted`,
					int64(key>>32), int64(key&0xffffffff)).Scan(&waiting).Error
				return err == nil && waiting > 0
			}, 5*time.Second, 10*time.Millisecond, "the competing reconciler must wait on the production lock")
			close(release)
			require.NoError(t, <-first)
			require.NoError(t, <-second)
			var stored, view, function int
			require.NoError(t, db.Raw(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).Scan(&stored).Error)
			require.NoError(t, db.Raw(`SELECT generation FROM analytics.generation_probe`).Scan(&view).Error)
			require.NoError(t, db.Raw(`SELECT analytics.generation_probe()`).Scan(&function).Error)
			require.Equal(t, 2, stored)
			require.Equal(t, 2, view)
			require.Equal(t, 2, function)
			wantSkips := float64(0)
			if firstGeneration == 2 {
				wantSkips = 1
			}
			require.Equal(t, wantSkips, testutil.ToFloat64(metrics.NewerSurfaceSkips))
		})
	}
}

func TestIntegrationAnalyticsNewerGenerationPreservesActualSurface(t *testing.T) {
	mgr := newPostgresManager(t, "dcanalyticsgenerationfutureit")
	ctx := context.Background()
	db := mgr.DB(core.WithSystemContext(ctx))
	t.Cleanup(func() {
		// Restore the original projection so the fixed-name harness can rerun.
		require.NoError(t, db.Exec(`DROP VIEW IF EXISTS analytics.events`).Error)
		require.NoError(t, execAnalyticsSurface(db))
		require.NoError(t, db.Exec(`UPDATE "event-management".analytics_surface_generation SET generation = 0 WHERE id = 1`).Error)
	})
	require.NoError(t, db.Exec(`UPDATE "event-management".analytics_surface_generation SET generation = 0 WHERE id = 1`).Error)
	require.NoError(t, ReconcileAnalyticsSurface(ctx, mgr, newTestAnalyticsMetrics()))
	// Simulate a later release widening events, without introducing text_value.
	view := analyticsViews[0]
	view.columns = append(append([]string{}, view.columns...), "future_column")
	stmt := view.createViewStmt()
	// The future column need not exist in the event store for this fixture.
	stmt = strings.Replace(stmt, `"future_column"`, `'future'::text AS "future_column"`, 1)
	require.NoError(t, db.Exec(stmt).Error)
	require.NoError(t, db.Exec(`CREATE OR REPLACE FUNCTION analytics.reader_tenant() RETURNS text LANGUAGE sql STABLE AS 'SELECT ''future''::text'`).Error)
	require.NoError(t, storeAnalyticsGeneration(db, AnalyticsSurfaceGeneration+1))
	var beforeView, beforeFunction string
	require.NoError(t, db.Raw(`SELECT pg_get_viewdef('analytics.events'::regclass)`).Scan(&beforeView).Error)
	require.NoError(t, db.Raw(`SELECT pg_get_functiondef('analytics.reader_tenant()'::regprocedure)`).Scan(&beforeFunction).Error)
	metrics := newTestAnalyticsMetrics()
	require.NoError(t, ReconcileAnalyticsSurface(ctx, mgr, metrics))
	var afterView, afterFunction string
	require.NoError(t, db.Raw(`SELECT pg_get_viewdef('analytics.events'::regclass)`).Scan(&afterView).Error)
	require.NoError(t, db.Raw(`SELECT pg_get_functiondef('analytics.reader_tenant()'::regprocedure)`).Scan(&afterFunction).Error)
	require.Equal(t, beforeView, afterView)
	require.Equal(t, beforeFunction, afterFunction)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.NewerSurfaceSkips))
}

func TestIntegrationAnalyticsEqualLowerGenerationRepairsAndStores(t *testing.T) {
	mgr := newPostgresManager(t, "dcanalyticsgenerationrepairit")
	ctx := context.Background()
	db := mgr.DB(core.WithSystemContext(ctx))
	for _, stored := range []int{0, AnalyticsSurfaceGeneration} {
		require.NoError(t, db.Exec(`UPDATE "event-management".analytics_surface_generation SET generation = ? WHERE id = 1`, stored).Error)
		require.NoError(t, db.Exec(`ALTER VIEW analytics.events RESET (security_barrier)`).Error)
		require.NoError(t, db.Exec(`CREATE OR REPLACE FUNCTION analytics.reader_tenant() RETURNS text LANGUAGE sql STABLE AS 'SELECT ''drift''::text'`).Error)
		metrics := newTestAnalyticsMetrics()
		require.NoError(t, ReconcileAnalyticsSurface(ctx, mgr, metrics))
		intact, reason, err := analyticsSurfaceIntact(db)
		require.NoError(t, err)
		require.True(t, intact, reason)
		var generation int
		require.NoError(t, db.Raw(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).Scan(&generation).Error)
		require.Equal(t, AnalyticsSurfaceGeneration, generation)
		require.Zero(t, testutil.ToFloat64(metrics.NewerSurfaceSkips))
	}
}
