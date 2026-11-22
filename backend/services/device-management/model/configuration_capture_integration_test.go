// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// T-cap-4 uses the real report and delete entry points in both schedules. Outer
// transactions retain the locks after their nested API transaction returns.
func TestConfigurationCaptureDeleteOnPostgres(t *testing.T) {
	for _, first := range []string{"report", "delete"} {
		t.Run(first, func(t *testing.T) {
			api := NewApi(newPostgresRdbManager(t))
			pool, err := api.RDB.Database.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(4) // holder, waiter, lock observer, and spare
			ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("cap%d", time.Now().UnixNano())), 20*time.Second)
			defer cancel()
			dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
			rev := mintOne(t, api, ctx)
			held := api.RDB.DB(ctx).Begin()
			require.NoError(t, held.Error)
			defer held.Rollback()
			var holder int
			require.NoError(t, held.Raw("SELECT pg_backend_pid()").Scan(&holder).Error)
			local := NewApi(&rdb.RdbManager{Database: held})
			if first == "report" {
				require.NoError(t, local.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
			} else {
				deleted, err := local.DeleteDevice(ctx, "dev")
				require.NoError(t, err)
				require.True(t, deleted)
			}
			pid := make(chan int, 1)
			done := make(chan error, 1)
			go func() {
				tx := api.RDB.DB(ctx).Begin()
				if tx.Error != nil {
					done <- tx.Error
					return
				}
				defer tx.Rollback()
				var waiter int
				if err := tx.Raw("SELECT pg_backend_pid()").Scan(&waiter).Error; err != nil {
					done <- err
					return
				}
				pid <- waiter
				other := NewApi(&rdb.RdbManager{Database: tx})
				var err error
				if first == "report" {
					_, err = other.DeleteDevice(ctx, "dev")
				} else {
					err = other.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil)
					if refusalReason(err) == ReportRejectedUnknownDevice {
						err = nil
					}
				}
				if err == nil {
					err = tx.Commit().Error
				}
				done <- err
			}()
			var waiter int
			select {
			case waiter = <-pid:
			case err := <-done:
				t.Fatalf("waiter failed: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			waitForConfigurationDeviceLock(t, ctx, api.RDB.DB(ctx), holder, waiter, first, done)
			require.NoError(t, held.Commit().Error)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var count int64
			require.NoError(t, api.RDB.DB(ctx).Model(&DeviceConfigurationReport{}).Where("device_id = ?", dev.ID).Count(&count).Error)
			require.Zero(t, count, "report survived deleted parent")
		})
	}
}

func waitForConfigurationDeviceLock(t *testing.T, ctx context.Context, db *gorm.DB, holder, waiter int, first string, done <-chan error) {
	t.Helper()
	want := "FOR KEY SHARE"
	if first == "report" {
		want = "FOR UPDATE"
	}
	for {
		select {
		case err := <-done:
			t.Fatalf("operation finished without waiting on parent: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		var rows []struct{ Query string }
		err := db.Raw(`SELECT a.query FROM pg_stat_activity a
JOIN pg_locks l ON l.pid = a.pid
WHERE a.pid = ? AND NOT l.granted AND ? = ANY(pg_blocking_pids(a.pid))`, waiter, holder).Scan(&rows).Error
		require.NoError(t, err)
		for _, row := range rows {
			if strings.Contains(row.Query, "devices") && strings.Contains(row.Query, want) {
				return
			}
		}
		runtime.Gosched()
	}
}

func TestConfigurationReportSchemaOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4) // holder, waiter, lock observer, and spare
	db := api.RDB.DB(core.WithSystemContext(context.Background()))
	for range 2 {
		require.NoError(t, schema.NewDeviceConfigurationReportsSchema().Migrate(db))
	}
	var checks int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint WHERE conrelid='"device-management".device_configuration_reports'::regclass AND conname='chk_configuration_reports_status' AND contype='c'`).Scan(&checks).Error)
	require.Equal(t, int64(1), checks)
	var foreignKeys int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint WHERE conrelid='"device-management".device_configuration_reports'::regclass AND contype='f'`).Scan(&foreignKeys).Error)
	require.Zero(t, foreignKeys)
}

// S3: the maintenance pass on the real catalog. An orphan (its device row hard-deleted the way a
// previous-release pod deletes it) and an expired row are removed; the expired row that is the
// device's CURRENT captured verdict is kept; a live device's recent row is untouched; and two
// replicas running the pass at once remove each row once, without errors.
func TestConfigurationReportMaintenanceOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), fmt.Sprintf("crm%d", time.Now().UnixNano())), 60*time.Second)
	defer cancel()
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	require.NoError(t, api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil))
	db := api.RDB.DB(ctx)
	old := time.Now().UTC().Add(-201 * 24 * time.Hour)
	// The device's current verdict, aged past retention: it must survive.
	require.NoError(t, db.Model(&DeviceConfigurationReport{}).Where("device_id = ? AND revision = ?", dev.ID, rev.Revision).
		Update("created_at", old).Error)
	// An expired verdict for an older revision of the same live device: it must go.
	require.NoError(t, db.Create(&DeviceConfigurationReport{DeviceId: dev.ID, Revision: rev.Revision - 1, Digest: rev.Digest,
		Status: string(ConfigurationRejected), Source: "REPORT", CreatedAt: old}).Error)
	// A recent verdict of the live device: it must stay.
	require.NoError(t, db.Create(&DeviceConfigurationReport{DeviceId: dev.ID, Revision: rev.Revision - 2, Digest: rev.Digest,
		Status: string(ConfigurationRejected), Source: "REPORT", CreatedAt: time.Now().UTC()}).Error)
	// Orphans: rows of a device id that has no device row.
	for i := int64(0); i < 3; i++ {
		require.NoError(t, db.Create(&DeviceConfigurationReport{DeviceId: dev.ID + 100000, Revision: i + 1, Digest: rev.Digest,
			Status: string(ConfigurationApplied), Source: "REPORT", CreatedAt: time.Now().UTC()}).Error)
	}
	before := map[string]float64{}
	for _, reason := range []string{"orphan", "retention"} {
		before[reason] = testutil.ToFloat64(configurationReportsPruned.WithLabelValues(reason))
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- api.MaintainDeviceConfigurationReports(ctx) }()
	}
	for range 2 {
		require.NoError(t, <-errs)
	}
	var left []DeviceConfigurationReport
	require.NoError(t, db.Order("revision").Find(&left).Error)
	revisions := []int64{}
	for _, r := range left {
		require.Equal(t, dev.ID, r.DeviceId, "an orphan survived")
		revisions = append(revisions, r.Revision)
	}
	require.Equal(t, []int64{rev.Revision - 2, rev.Revision}, revisions,
		"the recent row and the current verdict stay; the expired older verdict goes")
	// The database is shared with other tests' tenants, whose leftovers the pass may also remove,
	// so the counters only bound this tenant's work from below.
	require.GreaterOrEqual(t, testutil.ToFloat64(configurationReportsPruned.WithLabelValues("orphan"))-before["orphan"], 3.0)
	require.GreaterOrEqual(t, testutil.ToFloat64(configurationReportsPruned.WithLabelValues("retention"))-before["retention"], 1.0)
}
