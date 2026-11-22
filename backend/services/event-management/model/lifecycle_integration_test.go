// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Read jobs, not SQL intent: stale policies still delete data after an operator
// disables retention. Casting the stored interval also checks the effective window.
func requireRetentionJobs(t *testing.T, db *gorm.DB, policy DataLifecyclePolicy) {
	t.Helper()
	var jobs []struct {
		HypertableName string
		Days           int
	}
	require.NoError(t, db.Raw(`SELECT hypertable_name,
		EXTRACT(EPOCH FROM (config->>'drop_after')::interval)::bigint / 86400 AS days
		FROM timescaledb_information.jobs
		WHERE hypertable_schema = ? AND proc_name = 'policy_retention'`, lifecycleSchema).Scan(&jobs).Error)
	want := map[string]int{}
	for _, table := range LifecycleHypertables {
		// Deliberately independent of retentionDaysFor, which is under test.
		days := policy.RetentionDays
		if table == LocationHypertable && policy.LocationRetentionDays != nil {
			days = *policy.LocationRetentionDays
		}
		if table == FailedEventsHypertable {
			days = policy.FailedEventRetentionDays
		}
		if days > 0 {
			want[table] = days
		}
	}
	// The measurement_rollups aggregate carries a job of its own whenever raw
	// retention is on: one raw chunk interval longer, rounded up to whole days
	// (minimum one). Also deliberately independent of RollupRetentionDays.
	if policy.RetentionDays > 0 {
		margin := (policy.ChunkIntervalHours + 23) / 24
		if margin < 1 {
			margin = 1
		}
		want[RollupAggregate] = policy.RetentionDays + margin
		want[RollupAggregate1h] = policy.RetentionDays + margin
	}
	got := map[string]int{}
	for _, job := range jobs {
		require.NotContains(t, got, job.HypertableName, "duplicate retention job")
		got[job.HypertableName] = job.Days
	}
	require.Equal(t, want, got)
}

func TestIntegrationRetentionLifecycle(t *testing.T) {
	mgr := newPostgresManager(t, "it_retention_lifecycle")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := mgr.DB(core.WithSystemContext(ctx))
	policy := DataLifecyclePolicy{ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7}
	apply := func() {
		require.NoError(t, ApplyDataLifecyclePolicies(ctx, mgr, policy))
		requireRetentionJobs(t, db, policy)
	}
	apply()
	policy.RetentionDays = 0
	apply() // Only the failed-event diagnostic keeps its policy.
	policy.RetentionDays = 30
	apply()
	policy.RetentionDays = 7
	apply() // Replaces, rather than duplicates or leaves the 30-day window.
	policy.RetentionDays = 30
	policy.LocationRetentionDays = intPtr(0)
	apply()
}

func TestIntegrationRetentionDisablePermissionDenied(t *testing.T) {
	mgr := newPostgresManager(t, "it_retention_denied")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := mgr.DB(core.WithSystemContext(ctx))
	policy := DataLifecyclePolicy{ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7}
	require.NoError(t, ApplyDataLifecyclePolicies(ctx, mgr, policy))
	requireRetentionJobs(t, db, policy)

	// A real non-owner role can resolve the hypertable but cannot remove its job.
	// A pinned connection plus RESET ROLE prevents leaking it to pooled sessions.
	role := fmt.Sprintf("dc_retention_%d", time.Now().UnixNano())
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE ROLE %q NOLOGIN`, role)).Error)
	t.Cleanup(func() {
		cleanup := mgr.DB(core.WithSystemContext(context.Background()))
		require.NoError(t, cleanup.Exec(fmt.Sprintf(`DROP OWNED BY %q`, role)).Error)
		require.NoError(t, cleanup.Exec(fmt.Sprintf(`DROP ROLE %q`, role)).Error)
	})
	require.NoError(t, db.Exec(fmt.Sprintf(`GRANT USAGE ON SCHEMA %q TO %q`, lifecycleSchema, role)).Error)
	// Use autocommit statements on a pinned connection: an earlier best-effort
	// failure must not abort a transaction and mask the retention permission error.
	require.NoError(t, db.Connection(func(pinned *gorm.DB) error {
		// Connection hands over a non-session *gorm.DB, whose Error is STICKY: after
		// the probe fails, every later Exec on it is skipped and returns that stale
		// error. applyOne's own removal would then "fail" without being sent, and
		// RESET ROLE would never run, returning the connection to the pool still
		// under the role. A session gives every statement a fresh error.
		conn := pinned.Session(&gorm.Session{})
		require.NoError(t, conn.Exec(fmt.Sprintf(`SET ROLE %q`, role)).Error)
		defer func() { require.NoError(t, conn.Exec("RESET ROLE").Error) }()
		probe := conn.Exec(removeRetentionPolicyStmt("events")).Error
		var pgErr *pgconn.PgError
		require.True(t, errors.As(probe, &pgErr), "expected a Postgres permission error: %v", probe)
		require.Equal(t, "42501", pgErr.Code, "insufficient_privilege")
		err := applyOne(conn, "events", DataLifecyclePolicy{ChunkIntervalHours: 24})
		require.Error(t, err)
		require.ErrorContains(t, err, "events")
		pgErr = nil
		require.True(t, errors.As(err, &pgErr), "retention removal must preserve its cause: %v", err)
		require.Equal(t, "42501", pgErr.Code)
		return nil
	}))
	requireRetentionJobs(t, db, policy) // The forbidden job removal really left it live.
}

func TestIntegrationRetentionConcurrentApplies(t *testing.T) {
	const instance = "it_retention_concurrent"
	first := newPostgresManager(t, instance)
	second := newPostgresManager(t, instance) // Independent connection pools, like two replicas.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	policy := DataLifecyclePolicy{ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7}
	require.NoError(t, ApplyDataLifecyclePolicies(ctx, first, policy))

	removed := make(chan struct{})
	release := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var park sync.Once
	require.NoError(t, first.Database.Callback().Raw().After("gorm:raw").Register("test:park-retention-remove", func(tx *gorm.DB) {
		if tx.Statement.SQL.String() == removeRetentionPolicyStmt("events") {
			park.Do(func() {
				close(removed)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
	}))
	defer first.Database.Callback().Raw().Remove("test:park-retention-remove")
	results := make(chan error, 2)
	go func() { results <- ApplyDataLifecyclePolicies(ctx, first, policy) }()
	select {
	case <-removed:
	case <-ctx.Done():
		t.Fatal("first replica never reached retention removal")
	}
	go func() { results <- ApplyDataLifecyclePolicies(ctx, second, policy) }()
	db := second.DB(core.WithSystemContext(ctx))
	// A start barrier alone can pass on a serial schedule. Observe the second
	// session waiting while the first is inside the remove/add critical section.
	require.Eventually(t, func() bool {
		var count int64
		err := db.Raw(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE a.datname = current_database() AND l.locktype = 'advisory' AND NOT l.granted`).Scan(&count).Error
		return err == nil && count == 1
	}, 5*time.Second, 10*time.Millisecond, "second replica must wait on the lifecycle lock")
	unblock.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("concurrent reconciliation did not finish")
		}
	}
	requireRetentionJobs(t, db, policy)
}
