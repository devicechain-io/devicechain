// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"testing"

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

func TestDashboardCapCountsOnlyLiveTenantDashboards(t *testing.T) {
	api := newTestApi(t)
	assert.Equal(t, int64(maxDashboardsPerTenant), api.dashboardCreateLimit())
	api.dashboardLimit = 2
	ctx := core.WithTenant(context.Background(), "acme")
	first, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "first", Definition: defA})
	require.NoError(t, err)
	duplicate, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "first", Definition: defA})
	require.Error(t, err, "duplicate tokens must still fail below capacity")
	assert.NotErrorIs(t, err, ErrDashboardLimit)
	assert.Nil(t, duplicate)
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "second", Definition: defA})
	require.NoError(t, err)
	created, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "third", Definition: defA})
	require.ErrorIs(t, err, ErrDashboardLimit)
	assert.Nil(t, created)
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "second", Definition: defA})
	require.Error(t, err, "a duplicate token at capacity keeps its normal outcome")
	assert.NotErrorIs(t, err, ErrDashboardLimit)
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "third", Definition: defA})
	require.ErrorIs(t, err, ErrDashboardLimit)
	var typed interface{ Extensions() map[string]any }
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "DASHBOARD_LIMIT", typed.Extensions()["code"])
	assert.Contains(t, err.Error(), "10000")
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(&Dashboard{}).Count(&count).Error)
	assert.Equal(t, int64(2), count, "refusal must not insert even a deleted row")

	other := core.WithTenant(context.Background(), "other")
	_, err = api.CreateDashboard(other, &DashboardCreateRequest{Token: "first", Definition: defA})
	require.NoError(t, err, "another tenant has its own capacity")

	// Soft-deleted rows are excluded, as are rows removed by the public delete API.
	require.NoError(t, api.RDB.DB(ctx).Delete(first).Error)
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "third", Definition: defA})
	require.NoError(t, err)
	deleted, err := api.DeleteDashboard(ctx, "second")
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "fourth", Definition: defA})
	require.NoError(t, err)

	created, err = api.CreateDashboard(context.Background(), &DashboardCreateRequest{Token: "no-tenant", Definition: defA})
	require.ErrorIs(t, err, core.ErrNoTenant)
	assert.Nil(t, created)
	require.NoError(t, api.RDB.DB(ctx).Model(&Dashboard{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

func TestDashboardCapKeepsOverLimitTenantUsable(t *testing.T) {
	api := newTestApi(t)
	api.dashboardLimit = 2
	ctx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < 3; i++ {
		row := &Dashboard{TokenReference: rdb.TokenReference{Token: fmt.Sprintf("existing-%d", i)}, Definition: datatypes.JSON(defA)}
		require.NoError(t, api.RDB.DB(ctx).Create(row).Error)
	}
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "extra", Definition: defA})
	require.ErrorIs(t, err, ErrDashboardLimit)
	found, err := api.DashboardsByToken(ctx, []string{"existing-0"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	updated, err := api.UpdateDashboard(ctx, "existing-0", &DashboardUpdateRequest{Definition: util.OptionalStringOf(defB)}, nil)
	require.NoError(t, err)
	assert.JSONEq(t, defB, string(updated.Definition))
	v, _, err := api.PublishDashboard(ctx, "existing-0", nil, nil, "alice", nil)
	require.NoError(t, err)
	assert.JSONEq(t, defB, string(v.Definition))
	_, served, err := api.PublishedDashboard(ctx, "existing-0")
	require.NoError(t, err)
	assert.Equal(t, v.Version, served.Version)
	deleted, err := api.DeleteDashboard(ctx, "existing-0")
	require.NoError(t, err)
	assert.True(t, deleted)
}

func TestDashboardCapValidationBeforeDatabase(t *testing.T) {
	api := &Api{} // Any database access would panic.
	_, err := api.CreateDashboard(context.Background(), &DashboardCreateRequest{Definition: "invalid"})
	require.ErrorIs(t, err, ErrInvalidDefinition)
	api.TenantDeleted = func(string) bool { return true }
	_, err = api.CreateDashboard(core.WithTenant(context.Background(), "gone"), &DashboardCreateRequest{Definition: defA})
	var deleted *TenantDeletedError
	require.ErrorAs(t, err, &deleted)
}

// Ordered expectations exercise lock -> count -> insert, including failures that
// must roll back without an insert. The integration test exercises the actual lock.
func TestDashboardCapPostgresOrdering(t *testing.T) {
	for _, scenario := range []string{"last-slot", "full", "lock-error", "count-error", "insert-error", "missing-tenant"} {
		t.Run(scenario, func(t *testing.T) {
			sqldb, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqldb.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqldb}), &gorm.Config{DisableAutomaticPing: true})
			require.NoError(t, err)
			require.NoError(t, rdb.RegisterTenantScoping(db))
			api := NewApi(&rdb.RdbManager{Database: db})
			api.dashboardLimit = 2
			ctx := core.WithTenant(context.Background(), "acme")
			var wantErr error
			mock.ExpectBegin()
			if scenario == "missing-tenant" {
				ctx = context.Background()
			} else {
				hash := fnv.New64a()
				_, _ = hash.Write([]byte("dashboard-create:acme"))
				lock := mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(int64(hash.Sum64()))
				if scenario == "lock-error" {
					wantErr = errors.New("lock unavailable")
					lock.WillReturnError(wantErr)
				} else {
					lock.WillReturnResult(sqlmock.NewResult(0, 1))
					count := mock.ExpectQuery(`SELECT count\(\*\) FROM "dashboards" WHERE .*`).WithArgs("acme")
					switch scenario {
					case "full":
						count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
						mock.ExpectQuery(`SELECT count\(\*\) FROM "dashboards" WHERE token = .*`).
							WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
						wantErr = ErrDashboardLimit
					case "count-error":
						wantErr = errors.New("count unavailable")
						count.WillReturnError(wantErr)
					default:
						count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
						insert := mock.ExpectQuery(`INSERT INTO "dashboards" .*RETURNING "id"`)
						if scenario == "insert-error" {
							wantErr = errors.New("duplicate token")
							insert.WillReturnError(wantErr)
						} else {
							insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
						}
					}
				}
			}
			if wantErr != nil || scenario == "missing-tenant" {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			dash, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: "new", Definition: defA})
			if scenario == "missing-tenant" {
				require.ErrorIs(t, err, core.ErrNoTenant)
				assert.Nil(t, dash)
			} else if wantErr != nil {
				require.ErrorIs(t, err, wantErr)
				assert.Nil(t, dash)
			} else {
				require.NoError(t, err)
				assert.Equal(t, uint(7), dash.ID)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
