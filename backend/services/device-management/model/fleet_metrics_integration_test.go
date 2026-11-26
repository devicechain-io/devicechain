// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func pgExecutor(t *testing.T, api *Api, p *postPG) string {
	t.Helper()
	state, err := api.FleetExecutor(p.ctx, p.op)
	require.NoError(t, err)
	return state
}

func pgRuntime(t *testing.T, p *postPG, changes map[string]any) {
	t.Helper()
	require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationRuntime{}).Where("operation_id = ?", p.op.ID).Updates(changes).Error)
}

// I-15 on PostgreSQL. Stop every driver: UNKNOWN after 60 s. Block the standing client: HELD with
// the code. Two replicas read one stored state. A permanent retryable error: STALLED at 5 minutes,
// paused at 24 h. Time passing is the stored stamps moving back on the database clock; this process's
// clock is skewed by a day and must never be read.
func TestBulkChangeExecutorI15OnTheDatabaseClock(t *testing.T) {
	p := newPostPG(t, "v1")
	standing := &fakeStanding{answer: map[string]bool{alice.Name: true}}
	p.api.PrincipalStanding = standing
	replica := NewApi(p.api.RDB)
	replica.PrincipalStanding = standing
	previous := fleetClock
	fleetClock = func() time.Time {
		t.Error("PostgreSQL executor and gauge decisions must never read the process clock")
		return time.Now().Add(24 * time.Hour)
	}
	t.Cleanup(func() { fleetClock = previous })
	db := p.api.RDB.DB(p.ctx)

	// Started, never driven: nobody is looking at it.
	require.Equal(t, "UNKNOWN", pgExecutor(t, p.api, p))

	// The standing client is down: the pass holds with the code, on both replicas' reading.
	standing.err = errors.New("user-management is unavailable")
	r, err := p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, FleetHoldAuthorityUnavailable, r.Hold)
	for i := 0; i < 5; i++ {
		require.Equal(t, "HELD", pgExecutor(t, p.api, p))
		require.Equal(t, "HELD", pgExecutor(t, replica, p))
	}
	rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
	require.NoError(t, err)
	require.Equal(t, FleetHoldAuthorityUnavailable, rt.HoldCode)

	// Every driver stops: the heartbeat ages past 60 s and the stale hold no longer speaks for it.
	pgRuntime(t, p, map[string]any{"driver_seen_at": gorm.Expr("clock_timestamp() - interval '59 seconds'")})
	require.Equal(t, "HELD", pgExecutor(t, replica, p))
	pgRuntime(t, p, map[string]any{"driver_seen_at": gorm.Expr("clock_timestamp() - interval '61 seconds'")})
	require.Equal(t, "UNKNOWN", pgExecutor(t, p.api, p))
	require.Equal(t, "UNKNOWN", pgExecutor(t, replica, p))

	// A permanent retryable error: every attempt fails undecided.
	standing.err = nil
	attributeBeforeCommitHook = func(*gorm.DB) error { return errors.New("the dependency is down") }
	t.Cleanup(func() { attributeBeforeCommitHook = nil })
	retryable := testutil.ToFloat64(bulkMetrics.postAttempts.WithLabelValues(FleetKindConfigSet, "retryable"))
	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.Error(t, err)
	require.Equal(t, FleetHoldPostError, r.Hold)
	require.Zero(t, r.Posted)
	require.Equal(t, retryable+2, testutil.ToFloat64(bulkMetrics.postAttempts.WithLabelValues(FleetKindConfigSet, "retryable")))
	// It was just started, so it is still within its five minutes.
	require.Equal(t, "WORKING", pgExecutor(t, p.api, p))
	sample := pgSample(t, p)
	require.Equal(t, 1.0, sample.holds[FleetHoldPostError])
	require.Less(t, sample.oldestRunnable, 300.0)

	// Five minutes without an applied line: STALLED, not HELD, and the not-advancing age says so.
	require.NoError(t, db.Model(&FleetOperation{}).Where("id = ?", p.op.ID).
		Update("state_changed_at", gorm.Expr("clock_timestamp() - interval '301 seconds'")).Error)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", p.op.ID).
		Update("next_attempt_at", gorm.Expr("clock_timestamp() - interval '1 second'")).Error)
	_, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.Error(t, err)
	require.Equal(t, "STALLED", pgExecutor(t, p.api, p))
	require.Equal(t, "STALLED", pgExecutor(t, replica, p))
	require.GreaterOrEqual(t, pgSample(t, p).oldestRunnable, 300.0)

	// 24 hours: the driver pauses it DEPENDENCY_UNAVAILABLE, and an operation that is not running is IDLE.
	paused := testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseDependency))
	pgRuntime(t, p, map[string]any{"hold_since": gorm.Expr("clock_timestamp() - interval '24 hours'")})
	r, err = p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, FleetPauseDependency, r.Paused)
	require.Equal(t, paused+1, testutil.ToFloat64(bulkMetrics.transitions.WithLabelValues(FleetDispatchPaused, FleetPauseDependency)))
	require.Equal(t, "IDLE", pgExecutor(t, p.api, p))
	sample = pgSample(t, p)
	require.Zero(t, sample.oldestRunnable)
	require.Equal(t, 1.0, sample.operations[FleetDispatchPaused])
	// The shared test database holds other tests' tenants, so the instance-wide refresh is judged only
	// on what is instance-wide by nature.
	require.NoError(t, p.api.refreshFleetMetrics(p.ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(bulkMetrics.activated), "the migrated database enforces the writer protocol")
}

// pgSample is this test tenant's contribution to the gauges, computed on PostgreSQL.
func pgSample(t *testing.T, p *postPG) fleetGaugeSample {
	t.Helper()
	sample := fleetGaugeSample{operations: map[string]float64{}, holds: map[string]float64{}}
	require.NoError(t, p.api.sampleFleetTenant(p.ctx, &sample))
	return sample
}

// A device another writer holds is ordinary contention: counted busy, no hold. A row held by a
// previous-release writer counts toward the dependency hold: counted retryable, POST_ERROR.
func TestBulkChangeBusyDeviceAndBusyRowAreCountedApart(t *testing.T) {
	for _, tc := range []struct{ code, result, hold string }{
		{"DEVICE_BUSY", "busy", ""},
		{"ATTRIBUTE_ROW_BUSY", "retryable", FleetHoldPostError},
	} {
		t.Run(tc.code, func(t *testing.T) {
			p := newPostPG(t, "v1")
			p.api.PrincipalStanding = &fakeStanding{answer: map[string]bool{alice.Name: true}}
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperation{}).Where("id = ?", p.op.ID).Update("eff_posts_per_second", 1).Error)
			if tc.code == "ATTRIBUTE_ROW_BUSY" {
				value := "before"
				_, err := p.api.SetEntityAttribute(p.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: p.devs[0].Token,
					Scope: "SHARED", AttrKey: "k", ValueType: "STRING", Value: &value})
				require.NoError(t, err)
			}
			holder := p.api.RDB.DB(p.ctx).Begin()
			require.NoError(t, holder.Error)
			defer holder.Rollback()
			if tc.code == "DEVICE_BUSY" {
				require.NoError(t, holder.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, p.devs[0].ID).Error)
			} else {
				require.NoError(t, holder.Exec(`SELECT id FROM entity_attributes
					WHERE entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k' FOR UPDATE`, p.devs[0].ID).Error)
			}
			before := testutil.ToFloat64(bulkMetrics.postAttempts.WithLabelValues(FleetKindConfigSet, tc.result))
			r, err := p.api.DriveFleetOperation(p.ctx, p.op.Token, DefaultFleetCaps)
			require.NoError(t, err)
			require.Zero(t, r.Posted)
			require.Equal(t, before+1, testutil.ToFloat64(bulkMetrics.postAttempts.WithLabelValues(FleetKindConfigSet, tc.result)))
			rt, err := p.api.FleetOperationRuntimeOf(p.ctx, p.op)
			require.NoError(t, err)
			require.Equal(t, tc.hold, rt.HoldCode)
			// Neither is a blocking hold: the operation is still advancing as far as the executor can tell.
			require.Equal(t, "WORKING", pgExecutor(t, p.api, p))
		})
	}
}

// A control request that cannot drain in-flight posting is counted by its action.
func TestBulkChangeFencePendingIsCounted(t *testing.T) {
	p := newPostPG(t, "v1")
	poster := p.api.RDB.DB(p.ctx).Begin()
	require.NoError(t, poster.Error)
	defer poster.Rollback()
	require.NoError(t, fleetLock(poster, fleetOpKey(p.tenant, p.op.ID), true))
	restore := ShortenFleetFenceWaitForTest("100ms")
	t.Cleanup(restore)
	pending := testutil.ToFloat64(bulkMetrics.fencePending.WithLabelValues(fleetEventPause))
	waits := histogramCount(t, bulkMetrics.fenceWait.WithLabelValues(fleetEventPause))
	_, err := p.api.PauseFleetOperation(p.ctx, p.op.Token, alice.Name)
	requireFleetCode(t, err, FleetCodeFencePending)
	require.Equal(t, pending+1, testutil.ToFloat64(bulkMetrics.fencePending.WithLabelValues(fleetEventPause)))
	require.Equal(t, waits+1, histogramCount(t, bulkMetrics.fenceWait.WithLabelValues(fleetEventPause)))
}
