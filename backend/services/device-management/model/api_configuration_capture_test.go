// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// T-cap-1 and T-cap-2: capture must preserve every accepted state transition,
// while retaining the first occurrence of each verdict at a revision.
func TestConfigurationCaptureFirstWins(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	for _, report := range []struct {
		status ConfigurationStatus
		errors []string
	}{
		{ConfigurationApplied, []string{"first applied"}},
		{ConfigurationRejected, []string{"first rejected"}},
		{ConfigurationRejected, []string{"changed rejection"}},
		{ConfigurationApplied, []string{"later applied"}},
	} {
		if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, report.status, report.errors); err != nil {
			t.Fatalf("accepted sequence refused: %v", err)
		}
		if got := reportedState(t, api, ctx); got.ReportedStatus.String != string(report.status) || got.ReportedErrorList()[0] != report.errors[0] {
			t.Fatalf("state did not accept report: %+v", got)
		}
	}
	var rows []struct{ Status, Errors string }
	if err := api.RDB.DB(ctx).Model(&DeviceConfigurationReport{}).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Status != "APPLIED" || rows[0].Errors != `["first applied"]` || rows[1].Status != "REJECTED" || rows[1].Errors != `["first rejected"]` {
		t.Fatalf("first verdicts = %+v", rows)
	}
}

// T-cap-3: an old writer can hide an intermediate rejection. Only its final
// pair can be backfilled; the interval preceding the next capture is degraded.
func TestConfigurationCaptureGapAndIdenticalReturn(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
	require.True(t, reportedState(t, api, ctx).CaptureCompleteSince.Valid, "first capture must establish coverage")
	old := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationState{}).Where("device_id = ?", dev.ID).Update("capture_complete_since", old).Error)
	rev.ID, rev.Revision = 0, 5
	require.NoError(t, api.RDB.DB(ctx).Create(rev).Error)
	for _, status := range []ConfigurationStatus{ConfigurationRejected, ConfigurationApplied} {
		require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationState{}).Where("device_id = ?", dev.ID).Updates(map[string]any{
			"reported_revision": 5, "reported_status": status, "reported_digest": rev.Digest, "reported_errors": nil,
		}).Error)
	}
	// Identical report: capture and backfill must run even though reported_at stays.
	before := reportedState(t, api, ctx)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", 5, rev.Digest, ConfigurationApplied, nil))
	got := reportedState(t, api, ctx)
	require.Equal(t, sql.NullInt64{Int64: 5, Valid: true}, got.CaptureGapThrough)
	require.True(t, got.CaptureCompleteSince.Valid && got.CaptureCompleteSince.Time.After(old))
	require.Equal(t, before.ReportedAt, got.ReportedAt)
	require.Equal(t, got.ReportedRevision, got.CapturedRevision)
	require.Equal(t, got.ReportedStatus, got.CapturedStatus)
	var rows []DeviceConfigurationReport
	require.NoError(t, api.RDB.DB(ctx).Where("revision = ?", 5).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "STATE_BACKFILL", rows[0].Source)
	// A RECEIVED report is tracked in the pair but is never evidence.
	rev.ID, rev.Revision = 0, 6
	require.NoError(t, api.RDB.DB(ctx).Create(rev).Error)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", 6, rev.Digest, ConfigurationReceived, nil))
	got = reportedState(t, api, ctx)
	require.Equal(t, got.ReportedRevision, got.CapturedRevision)
	require.Equal(t, got.ReportedStatus, got.CapturedStatus)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationReport{}).Where("revision = ?", 6).Count(&count).Error)
	require.Zero(t, count)
}

// Capture must roll back with a failed state write. A separate capture transaction
// would leave evidence of a report the state did not accept.
func TestConfigurationCaptureRollsBackWithState(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationReceived, nil))
	const callback = "capture_state_failure"
	require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "device_configuration_states" {
			db.AddError(errors.New("injected state write failure"))
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Update().Remove(callback) })
	require.Error(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationReport{}).Count(&count).Error)
	require.Zero(t, count)
	require.Equal(t, "RECEIVED", reportedState(t, api, ctx).ReportedStatus.String)
}

// T-cap-5: old-pod deletes leave reports; the pass repairs only orphans and
// expired history, in bounded statements, across tenants.
func TestConfigurationReportMaintenance(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	now := time.Now().UTC()
	for _, tenant := range []string{"acme", "other"} {
		tenantCtx := core.WithTenant(context.Background(), tenant)
		// Device IDs are global. Keep the other tenant's live row distinct.
		live := dev.ID
		if tenant == "other" {
			other := &Device{}
			other.TenantId, other.Token = tenant, "other"
			require.NoError(t, api.RDB.DB(tenantCtx).Create(other).Error)
			live = other.ID
		}
		for i := range 1005 {
			require.NoError(t, api.RDB.DB(tenantCtx).Create(&DeviceConfigurationReport{
				DeviceId: live + 100000, Revision: int64(i), Status: "APPLIED", Digest: "sha256:x", Source: "REPORT", CreatedAt: now,
			}).Error)
		}
		for i, age := range []time.Duration{201 * 24 * time.Hour, 199 * 24 * time.Hour} {
			require.NoError(t, api.RDB.DB(tenantCtx).Create(&DeviceConfigurationReport{
				DeviceId: live, Revision: int64(i), Status: "APPLIED", Digest: "sha256:x", Source: "REPORT", CreatedAt: now.Add(-age),
			}).Error)
		}
	}
	// A genuine raw-delete orphan, in addition to the missing-parent fixtures.
	doomed := &Device{}
	doomed.Token = "doomed"
	require.NoError(t, api.RDB.DB(ctx).Create(doomed).Error)
	require.NoError(t, api.RDB.DB(ctx).Create(&DeviceConfigurationReport{DeviceId: doomed.ID, Revision: 1, Status: "APPLIED", Source: "REPORT", Digest: "sha256:x"}).Error)
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(doomed).Error)
	orphans := testutil.ToFloat64(configurationReportsPruned.WithLabelValues("orphan"))
	retained := testutil.ToFloat64(configurationReportsPruned.WithLabelValues("retention"))
	const callback = "configuration_prune_batch_bound"
	require.NoError(t, api.RDB.Database.Callback().Delete().After("gorm:delete").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "device_configuration_reports" {
			if db.RowsAffected > 1000 {
				t.Errorf("prune statement removed %d rows; limit is 1000", db.RowsAffected)
			}
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Delete().Remove(callback) })
	require.NoError(t, api.MaintainDeviceConfigurationReports(context.Background()))
	require.Equal(t, orphans+2011, testutil.ToFloat64(configurationReportsPruned.WithLabelValues("orphan")))
	require.Equal(t, retained+2, testutil.ToFloat64(configurationReportsPruned.WithLabelValues("retention")))
	for _, tenant := range []string{"acme", "other"} {
		var rows []DeviceConfigurationReport
		require.NoError(t, api.RDB.DB(core.WithTenant(context.Background(), tenant)).Find(&rows).Error)
		require.Len(t, rows, 1)
		require.Equal(t, int64(1), rows[0].Revision)
	}
}

func TestConfigurationCaptureDeviceDelete(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
	deleted, err := api.DeleteDevice(ctx, "dev")
	require.NoError(t, err)
	require.True(t, deleted)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationReport{}).Where("device_id = ?", dev.ID).Count(&count).Error)
	require.Zero(t, count)
}

// The review's case: a capture of APPLIED r7, then a previous-release pod accepts REJECTED r7 and
// APPLIED r7, restoring the very pair the capture left. Only reported_at shows that another
// writer was there, and the next capture (here an identical report) must mark the gap. A
// status-only change at the captured revision must be caught the same way.
func TestConfigurationCaptureSeesARestoredPair(t *testing.T) {
	for _, restore := range []bool{true, false} {
		api, ctx := configTestApi(t)
		dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
		rev := mintOne(t, api, ctx)
		require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
		old := time.Now().UTC().Add(-time.Hour)
		require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationState{}).Where("device_id = ?", dev.ID).Update("capture_complete_since", old).Error)
		statuses := []ConfigurationStatus{ConfigurationRejected}
		if restore {
			statuses = append(statuses, ConfigurationApplied)
		}
		for i, status := range statuses {
			// A previous-release pod: it writes the reported_* columns and reported_at, and knows
			// nothing of the capture columns.
			require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationState{}).Where("device_id = ?", dev.ID).Updates(map[string]any{
				"reported_status": status, "reported_at": time.Now().UTC().Add(time.Duration(i+1) * time.Second),
			}).Error)
		}
		report := ConfigurationApplied
		if !restore {
			report = ConfigurationRejected
		}
		require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, report, nil))
		got := reportedState(t, api, ctx)
		require.True(t, got.CaptureCompleteSince.Time.After(old),
			"restore=%v: a write by a non-capturing pod must move capture_complete_since", restore)
		require.Equal(t, got.ReportedAt.Valid, got.CapturedReportedAt.Valid)
		require.True(t, got.ReportedAt.Time.Equal(got.CapturedReportedAt.Time), "the capture must record the reported_at it leaves")
	}
}

// Defence in depth: a write that changes the pair but not reported_at (no release does this, but
// the comparison must not rest on one column) is still a gap.
func TestConfigurationCaptureSeesAPairChangeWithoutATimestamp(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
	old := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationState{}).Where("device_id = ?", dev.ID).Updates(map[string]any{
		"capture_complete_since": old, "reported_status": ConfigurationRejected,
	}).Error)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationRejected, nil))
	require.True(t, reportedState(t, api, ctx).CaptureCompleteSince.Time.After(old), "a changed pair must be a gap")
}
