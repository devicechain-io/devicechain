// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A save that lands in the same clock instant as the read must still move the dashboard's
// version, or an editor holding the version they read is not refused and overwrites it. Every
// row freezes the clock, so the version can only move because the write moves it. Every row
// fails on a write that takes its version from the clock alone.

// frozenAt is an instant with sub-microsecond digits. It is in the past on purpose.
var frozenAt = time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC)

const (
	defOriginal = `{"schemaVersion":1,"widgets":[]}`
	defWriter   = `{"schemaVersion":1,"widgets":[{"id":"writer"}]}`
	defLate     = `{"schemaVersion":1,"widgets":[{"id":"late"}]}`
)

// setClock replaces the clock every later call of api sees. Each api.* call takes a fresh
// copy of the database config, so the clock in effect is the one set before that call.
func setClock(api *Api, now *time.Time) {
	api.RDB.Database.Config.NowFunc = func() time.Time { return *now }
}

func versionStringOf(t *testing.T, api *Api, ctx context.Context) (string, *Dashboard) {
	t.Helper()
	found, err := api.DashboardsByToken(ctx, []string{"fleet"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return *util.FormatTime(found[0].UpdatedAt), found[0]
}

func TestEveryDashboardWriteMovesTheVersion(t *testing.T) {
	rows := []struct {
		name    string
		setup   func(t *testing.T, api *Api, ctx context.Context)
		write   func(t *testing.T, api *Api, ctx context.Context, read string)
		content string
	}{
		{"guarded update", nil, func(t *testing.T, api *Api, ctx context.Context, read string) {
			_, err := api.UpdateDashboard(ctx, "fleet", &DashboardUpdateRequest{
				Definition: util.OptionalStringOf(defWriter)}, &read)
			require.NoError(t, err)
		}, defWriter},
		{"update without a precondition", nil, func(t *testing.T, api *Api, ctx context.Context, _ string) {
			_, err := api.UpdateDashboard(ctx, "fleet", &DashboardUpdateRequest{
				Definition: util.OptionalStringOf(defWriter)}, nil)
			require.NoError(t, err)
		}, defWriter},
		{"rollback", func(t *testing.T, api *Api, ctx context.Context) {
			_, err := api.PublishDashboard(ctx, "fleet", nil, nil, "tester", nil)
			require.NoError(t, err)
			_, err = api.UpdateDashboard(ctx, "fleet", &DashboardUpdateRequest{
				Definition: util.OptionalStringOf(defWriter)}, nil)
			require.NoError(t, err)
		}, func(t *testing.T, api *Api, ctx context.Context, _ string) {
			_, err := api.RollbackDashboard(ctx, "fleet", 1)
			require.NoError(t, err)
		}, defOriginal},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			api := newTestApi(t)
			now := frozenAt
			setClock(api, &now)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
				Token: "fleet", Name: strp("Fleet"), Definition: defOriginal})
			require.NoError(t, err)
			if row.setup != nil {
				row.setup(t, api, ctx)
			}
			v0, read := versionStringOf(t, api, ctx)

			row.write(t, api, ctx, v0)

			_, stored := versionStringOf(t, api, ctx)
			assert.False(t, stored.UpdatedAt.Equal(read.UpdatedAt), "the write left the version where it was read: %v", stored.UpdatedAt)
			assert.JSONEq(t, row.content, string(stored.Definition))

			// The editor who read the version before the write is refused and changes nothing.
			_, err = api.UpdateDashboard(ctx, "fleet", &DashboardUpdateRequest{
				Definition: util.OptionalStringOf(defLate)}, &v0)
			assert.ErrorIs(t, err, ErrConflict)
			_, after := versionStringOf(t, api, ctx)
			assert.JSONEq(t, row.content, string(after.Definition))
		})
	}
}

// The clock moving on is what is stored: the floor never invents a version when time has moved.
func TestDashboardWriteStoresTheClockWhenItHasMoved(t *testing.T) {
	api := newTestApi(t)
	now := frozenAt
	setClock(api, &now)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token: "fleet", Name: strp("Fleet"), Definition: defOriginal})
	require.NoError(t, err)

	now = frozenAt.Add(time.Hour)
	_, err = api.UpdateDashboard(ctx, "fleet", &DashboardUpdateRequest{
		Definition: util.OptionalStringOf(defWriter)}, nil)
	require.NoError(t, err)
	_, stored := versionStringOf(t, api, ctx)
	assert.True(t, stored.UpdatedAt.Equal(now), "stored %v, want the clock %v", stored.UpdatedAt, now)
}

// A rollback hands back the version the database stored, which is what the console sends
// as its next precondition. The fixture keeps updated_at to the microsecond, as PostgreSQL
// does, so a response carrying the value that was sent is refused by the very next save.
func TestRollbackReturnsTheStoredVersion(t *testing.T) {
	api := newTestApi(t)
	now := frozenAt
	setClock(api, &now)
	const hook = "test:store_microseconds"
	require.NoError(t, api.RDB.Database.Callback().Update().After("gorm:update").Register(hook, func(tx *gorm.DB) {
		d, ok := tx.Statement.Model.(*Dashboard)
		if !ok || tx.Error != nil || tx.Statement.RowsAffected == 0 {
			return
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).
			Exec("UPDATE dashboards SET updated_at = ? WHERE id = ?", d.UpdatedAt.Truncate(time.Microsecond), d.ID).Error; err != nil {
			tx.AddError(err)
		}
	}))
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
		Token: "fleet", Name: strp("Fleet"), Definition: defOriginal})
	require.NoError(t, err)
	_, err = api.PublishDashboard(ctx, "fleet", nil, nil, "tester", nil)
	require.NoError(t, err)
	now = frozenAt.Add(time.Hour) // past the floor, so the clock itself (with its nanoseconds) is sent

	rolled, err := api.RollbackDashboard(ctx, "fleet", 1)
	require.NoError(t, err)

	_, stored := versionStringOf(t, api, ctx)
	assert.True(t, rolled.UpdatedAt.Equal(stored.UpdatedAt), "response %v, stored %v", rolled.UpdatedAt, stored.UpdatedAt)
	next := util.FormatTime(rolled.UpdatedAt)
	_, err = api.UpdateDashboard(ctx, "fleet", &DashboardUpdateRequest{
		Definition: util.OptionalStringOf(defWriter)}, next)
	assert.NoError(t, err)
}
