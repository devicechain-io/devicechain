// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func seedVersionHistory(t *testing.T, api *Api, ctx context.Context, count int, step int32) *Dashboard {
	t.Helper()
	dash, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "full", Definition: defA})
	require.NoError(t, err)
	rows := make([]DashboardVersion, count)
	for i := range rows {
		rows[i] = DashboardVersion{DashboardID: dash.ID, Version: int32(i+1) * step, Definition: datatypes.JSON(defA)}
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(&rows, 50).Error)
	pointer := int32(count) * step
	require.NoError(t, api.RDB.DB(ctx).Model(dash).UpdateColumn("published_version", pointer).Error)
	stored, err := api.DashboardsByToken(ctx, []string{dash.Token})
	require.NoError(t, err)
	return stored[0]
}

func requireVersionLimitUnchanged(t *testing.T, api *Api, ctx context.Context, dash *Dashboard, count int64) {
	t.Helper()
	before := *util.FormatTime(dash.UpdatedAt)
	version, published, err := api.PublishDashboard(ctx, dash.Token, nil, nil, "alice", &before)
	require.ErrorIs(t, err, ErrVersionLimit)
	assert.Nil(t, version)
	assert.Nil(t, published)
	var typed interface{ Extensions() map[string]any }
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "DASHBOARD_VERSION_LIMIT", typed.Extensions()["code"])
	assert.Contains(t, err.Error(), "1000")
	var actual int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DashboardVersion{}).Where("dashboard_id = ?", dash.ID).Count(&actual).Error)
	assert.Equal(t, count, actual)
	stored, err := api.DashboardsByToken(ctx, []string{dash.Token})
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, dash.PublishedVersion, stored[0].PublishedVersion)
	assert.Equal(t, before, *util.FormatTime(stored[0].UpdatedAt))
	assert.JSONEq(t, string(dash.Definition), string(stored[0].Definition))
}

func TestPublishVersionCapRefusesAt1000WithoutWrites(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	dash := seedVersionHistory(t, api, ctx, 1000, 1)
	require.Equal(t, int32(1000), *dash.PublishedVersion)
	requireVersionLimitUnchanged(t, api, ctx, dash, 1000)

	// Capacity belongs to this dashboard, not to the tenant's combined history.
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "other", Definition: defB})
	require.NoError(t, err)
	v, _, err := api.PublishDashboard(ctx, "other", nil, nil, "alice", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), v.Version)
}

func TestPublishVersionCapCountsRowsSeparatelyFromNumbering(t *testing.T) {
	for _, step := range []int32{1, 3} {
		t.Run(fmt.Sprintf("step-%d", step), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			dash := seedVersionHistory(t, api, ctx, 999, step)
			v, published, err := api.PublishDashboard(ctx, dash.Token, nil, nil, "alice", nil)
			require.NoError(t, err)
			assert.Equal(t, int32(999)*step+1, v.Version)
			assert.Equal(t, dash.UpdatedAt, published.UpdatedAt)
			requireVersionLimitUnchanged(t, api, ctx, published, 1000)
		})
	}
}

func TestPublishVersionLimitKeepsOverLimitDashboardsUsable(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	dash := seedVersionHistory(t, api, ctx, 1001, 1)
	requireVersionLimitUnchanged(t, api, ctx, dash, 1001)
	_, served, err := api.PublishedDashboard(ctx, dash.Token)
	require.NoError(t, err)
	assert.Equal(t, int32(1001), served.Version)
	assert.JSONEq(t, defA, string(served.Definition))
	history, err := api.DashboardVersions(ctx, dash.Token, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	assert.Equal(t, int32(1001), history[0].Version)
	_, err = api.UpdateDashboard(ctx, dash.Token, &DashboardUpdateRequest{Definition: util.OptionalStringOf(defB)}, nil)
	require.NoError(t, err)
	_, err = api.ActivateDashboardVersion(ctx, dash.Token, 1)
	require.NoError(t, err)
	_, served, err = api.PublishedDashboard(ctx, dash.Token)
	require.NoError(t, err)
	assert.Equal(t, int32(1), served.Version)
	_, err = api.RollbackDashboard(ctx, dash.Token, 1, nil)
	require.NoError(t, err)
	deleted, err := api.DeleteDashboard(ctx, dash.Token)
	require.NoError(t, err)
	assert.True(t, deleted)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DashboardVersion{}).Where("dashboard_id = ?", dash.ID).Count(&count).Error)
	assert.Zero(t, count)
}

// Ordered PostgreSQL expectations pin FOR UPDATE -> COUNT -> MAX -> INSERT ->
// pointer move. SQLite tests above exercise stored values, not cross-process locks.
func TestPublishVersionCapPostgresOrdering(t *testing.T) {
	for _, scenario := range []string{"last-slot", "full", "count-error", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			sqldb, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqldb.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqldb}), &gorm.Config{DisableAutomaticPing: true})
			require.NoError(t, err)
			require.NoError(t, rdb.RegisterTenantScoping(db))
			api := NewApi(&rdb.RdbManager{Database: db})
			ctx := core.WithTenant(context.Background(), "acme")
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT .*FROM "dashboards" .*FOR UPDATE`).
				WillReturnRows(sqlmock.NewRows([]string{"id", "token", "tenant_id", "updated_at", "definition", "published_version"}).
					AddRow(7, "full", "acme", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), []byte(defA), 999))
			var expected *string
			var wantErr error
			if scenario == "stale" {
				expected = strp("2000-01-01T00:00:00Z")
				wantErr = ErrConflict
			} else {
				count := mock.ExpectQuery(`SELECT count\(\*\) FROM "dashboard_versions" WHERE dashboard_id = \$1`).WithArgs(7, "acme")
				switch scenario {
				case "last-slot":
					count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(999))
				case "full":
					count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1000))
					wantErr = ErrVersionLimit
				case "count-error":
					wantErr = errors.New("count unavailable")
					count.WillReturnError(wantErr)
				}
			}
			if wantErr != nil {
				mock.ExpectRollback()
			} else {
				mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM "dashboard_versions"`).
					WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(999))
				mock.ExpectQuery(`INSERT INTO "dashboard_versions" .*RETURNING "id"`).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1000))
				mock.ExpectExec(`UPDATE "dashboards" SET "published_version"=\$1`).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			v, dash, err := api.PublishDashboard(ctx, "full", nil, nil, "alice", expected)
			if wantErr != nil {
				require.ErrorIs(t, err, wantErr)
				assert.Nil(t, v)
				assert.Nil(t, dash)
			} else {
				require.NoError(t, err)
				assert.Equal(t, int32(1000), v.Version)
				assert.Equal(t, int32(1000), *dash.PublishedVersion)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
