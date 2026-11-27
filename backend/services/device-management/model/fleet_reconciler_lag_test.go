// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// freshBulkMetrics gives the test a metric set of its own, as a newly started pod has.
func freshBulkMetrics(t *testing.T) {
	t.Helper()
	previous := bulkMetrics
	bulkMetrics = NewBulkChangeMetrics(unregisteredBulkChangeMetrics{})
	t.Cleanup(func() { bulkMetrics = previous })
}

func reconcilerLag() float64 { return testutil.ToFloat64(bulkMetrics.reconcilerLag) }

// lagLine is one applied line of tenant's own DISPATCHED operation, awaiting evidence at revision 5,
// targeting a synthetic device (the journal has no device foreign key).
func lagLine(t *testing.T, api *Api, tenant string, device int64, protocol int32) *FleetOperationLine {
	t.Helper()
	db := api.RDB.DB(core.WithTenant(context.Background(), tenant))
	op := &FleetOperation{Token: "lag-" + tenant, Kind: FleetKindConfigSet, Name: "lag", Dispatch: FleetDispatchDispatched, DriverProtocol: protocol,
		RequestDigest: "x", Intent: `{"key":"k"}`, TargetKind: "DEVICE_LIST", TargetCount: 1, MaxPostsPerSecond: 1, EffPostsPerSecond: 1,
		SettleDeadlineS: 60, Initiator: "test"}
	require.NoError(t, db.Create(op).Error)
	line := &FleetOperationLine{OperationId: int64(op.ID), Ordinal: 1, TargetId: sql.NullInt64{Int64: device, Valid: true}, Posting: FleetPostingPosted,
		PostedRevision: sql.NullInt64{Int64: 5, Valid: true}, PostedValue: sql.NullString{String: `{"present":true,"valueType":"STRING","value":"v"}`, Valid: true}}
	require.NoError(t, db.Create(line).Error)
	return line
}

func lagReport(t *testing.T, api *Api, tenant string, device, revision int64, ago time.Duration) {
	t.Helper()
	db := api.RDB.DB(core.WithTenant(context.Background(), tenant))
	require.NoError(t, db.Create(&DeviceConfigurationReport{DeviceId: uint(device), Revision: revision, Status: "APPLIED", Source: "REPORT",
		Digest: "x", CreatedAt: time.Now().Add(-ago)}).Error)
}

// failReads makes every read on api's database fail (or, with hang, wait for its context to end
// and then fail), until the returned function is called.
func failReads(t *testing.T, api *Api, hang bool) (restore func()) {
	t.Helper()
	boom := errors.New("the database is unreachable")
	fail := func(tx *gorm.DB) {
		if hang {
			<-tx.Statement.Context.Done()
			_ = tx.AddError(tx.Statement.Context.Err())
			return
		}
		_ = tx.AddError(boom)
	}
	db := api.RDB.Database
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:fail-row", fail))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:fail-query", fail))
	restore = func() {
		_ = db.Callback().Row().Remove("test:fail-row")
		_ = db.Callback().Query().Remove("test:fail-query")
	}
	t.Cleanup(restore)
	return restore
}

// A pod that has measured nothing reads NaN, never the healthy 0.
func TestReconcilerLagIsNaNUntilMeasured(t *testing.T) {
	freshBulkMetrics(t)
	require.True(t, math.IsNaN(reconcilerLag()), "born NaN, got %v", reconcilerLag())
}

// The happy path, and the reason there is no per-tenant state to forget: one measurement reads
// every tenant's waiting reports, whichever tenant's context asks, so the oldest is found however
// many tenants there are and wherever any settlement rotation stands.
func TestReconcilerLagIsTheOldestWaitingReportAcrossEveryTenant(t *testing.T) {
	freshBulkMetrics(t)
	api, ctx := fleetFixture(t, 1)
	tenants := []string{"t-a", "t-b", "t-c"}
	ages := []time.Duration{20 * time.Second, 300 * time.Second, 45 * time.Second}
	lines := map[string]*FleetOperationLine{}
	for i, tenant := range tenants {
		device := int64(9_000_000 + i)
		lines[tenant] = lagLine(t, api, tenant, device, 1)
		lagReport(t, api, tenant, device, 5, ages[i])
	}
	// Not waiting: a report before the posted revision; a line already decided; a line an operation of a
	// newer release drives; a redacted line.
	lagReport(t, api, "t-a", 9_000_000, 4, time.Hour)
	// Nor a later revision of a device whose line already has its decider (t-a's revision 5): the
	// decider is the first report at or after the posted revision, as the settlement read takes it,
	// however old the reports after it are.
	lagReport(t, api, "t-a", 9_000_000, 7, time.Hour)
	decided := lagLine(t, api, "t-d", 9_000_010, 1)
	require.NoError(t, api.RDB.DB(core.WithTenant(ctx, "t-d")).Model(decided).Update("evidence_revision", 5).Error)
	lagReport(t, api, "t-d", 9_000_010, 5, time.Hour)
	lagLine(t, api, "t-e", 9_000_011, 99)
	lagReport(t, api, "t-e", 9_000_011, 5, time.Hour)
	redacted := lagLine(t, api, "t-f", 9_000_012, 1)
	require.NoError(t, api.RDB.DB(core.WithTenant(ctx, "t-f")).Model(redacted).Update("redacted_at", time.Now()).Error)
	lagReport(t, api, "t-f", 9_000_012, 5, time.Hour)

	// Asked from an unrelated tenant's context: the answer is the instance's, not the caller's.
	require.NoError(t, api.MeasureFleetReconcilerLag(core.WithTenant(ctx, "t-a")))
	require.InDelta(t, 300, reconcilerLag(), 2)

	// t-b's report is decided: the next oldest is t-c's.
	require.NoError(t, api.RDB.DB(core.WithTenant(ctx, "t-b")).Model(lines["t-b"]).Update("evidence_revision", 5).Error)
	require.NoError(t, api.MeasureFleetReconcilerLag(ctx))
	require.InDelta(t, 45, reconcilerLag(), 2)

	// Everything decided: a measured 0, not NaN.
	for _, tenant := range []string{"t-a", "t-c"} {
		require.NoError(t, api.RDB.DB(core.WithTenant(ctx, tenant)).Model(lines[tenant]).Update("evidence_revision", 5).Error)
	}
	require.NoError(t, api.MeasureFleetReconcilerLag(ctx))
	require.Equal(t, 0.0, reconcilerLag())
}

// A measurement that fails replaces whatever was measured before: a lag that was high stays
// unknown, not frozen at its old value, and a lag that was 0 does not keep reading healthy.
func TestReconcilerLagFailedMeasurementReadsNaNNotTheLastValue(t *testing.T) {
	freshBulkMetrics(t)
	api, ctx := fleetFixture(t, 1)
	line := lagLine(t, api, "t-a", 9_100_000, 1)
	lagReport(t, api, "t-a", 9_100_000, 5, 600*time.Second)
	require.NoError(t, api.MeasureFleetReconcilerLag(ctx))
	require.InDelta(t, 600, reconcilerLag(), 2)

	restore := failReads(t, api, false)
	require.Error(t, api.MeasureFleetReconcilerLag(ctx))
	require.True(t, math.IsNaN(reconcilerLag()), "a failed measurement after a high one reads NaN, got %v", reconcilerLag())
	restore()

	require.NoError(t, api.RDB.DB(core.WithTenant(ctx, "t-a")).Model(line).Update("evidence_revision", 5).Error)
	require.NoError(t, api.MeasureFleetReconcilerLag(ctx))
	require.Equal(t, 0.0, reconcilerLag())
	failReads(t, api, false)
	require.Error(t, api.MeasureFleetReconcilerLag(ctx))
	require.True(t, math.IsNaN(reconcilerLag()), "a failed measurement after a healthy one reads NaN, got %v", reconcilerLag())
}

// A pod on which every measurement fails never reads healthy: it reads NaN from its first moment
// to its last.
func TestReconcilerLagUniversalFailureNeverReadsHealthy(t *testing.T) {
	freshBulkMetrics(t)
	api, ctx := fleetFixture(t, 1)
	failReads(t, api, false)
	for range 3 {
		require.Error(t, api.MeasureFleetReconcilerLag(ctx))
		require.True(t, math.IsNaN(reconcilerLag()), "got %v", reconcilerLag())
	}
}

// A measurement that hangs ends as a failure within its bound, so it cannot hold an old value up
// while it waits, or hold up the passes after it.
func TestReconcilerLagHungMeasurementEndsAsNaN(t *testing.T) {
	freshBulkMetrics(t)
	api, ctx := fleetFixture(t, 1)
	lagLine(t, api, "t-a", 9_200_000, 1)
	lagReport(t, api, "t-a", 9_200_000, 5, time.Second)
	require.NoError(t, api.MeasureFleetReconcilerLag(ctx))
	require.False(t, math.IsNaN(reconcilerLag()))

	previous := fleetReconcilerLagTimeout
	fleetReconcilerLagTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fleetReconcilerLagTimeout = previous })
	failReads(t, api, true)
	done := make(chan error, 1)
	go func() { done <- api.MeasureFleetReconcilerLag(ctx) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("a hung measurement was not bounded")
	}
	require.True(t, math.IsNaN(reconcilerLag()), "got %v", reconcilerLag())
}

type everyTenantAtHome struct{}

func (everyTenantAtHome) ShardGroup(string) (int, error) { return 0, nil }

// With tenant sharding on, the home database no longer holds every tenant, so the instance-wide
// read refuses rather than answering for the tenants it can see.
func TestReconcilerLagRefusesUnderSharding(t *testing.T) {
	freshBulkMetrics(t)
	api, ctx := fleetFixture(t, 1)
	lagLine(t, api, "t-a", 9_300_000, 1)
	lagReport(t, api, "t-a", 9_300_000, 5, time.Second)
	require.NoError(t, api.MeasureFleetReconcilerLag(ctx))
	require.False(t, math.IsNaN(reconcilerLag()))
	require.NoError(t, api.RDB.EnableSharding(everyTenantAtHome{}, map[int]*gorm.DB{1: sqlitetest.OpenDSN(t, sqlitetest.MemoryDSN(t), nil)}))
	require.ErrorIs(t, api.MeasureFleetReconcilerLag(ctx), errFleetReconcilerLagSharded)
	require.True(t, math.IsNaN(reconcilerLag()), "got %v", reconcilerLag())
}
