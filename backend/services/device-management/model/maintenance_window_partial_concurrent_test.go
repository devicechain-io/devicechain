// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const partialWindowSlots = `[{"day":"FRIDAY","start":"20:00","durationMinutes":60}]`

func maintenanceWindowPartialFixture(t *testing.T) (*Api, context.Context, *MaintenanceWindow, *rdbtest.StatementCounter) {
	t.Helper()
	api := newWindowTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	window, err := api.CreateMaintenanceWindow(ctx, &MaintenanceWindowCreateRequest{
		Token: "partial-window", Name: strp("Original"), Description: strp("Description"),
		Timezone: "Europe/Berlin", Slots: sundayNightSlots, Metadata: strp(`{"original":true}`),
	})
	require.NoError(t, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, window, statements
}

func reloadPartialWindow(t *testing.T, api *Api, ctx context.Context) *MaintenanceWindow {
	t.Helper()
	rows, err := api.MaintenanceWindowsByToken(ctx, []string{"partial-window"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}

// The callback lands exactly one other write after the initial read, without
// goroutines or sleeps. A fresh statement prevents the read's clauses leaking in.
func interleaveWindowSlots(t *testing.T, api *Api, slots string) *int {
	t.Helper()
	fired := 0
	const callback = "test:window_other_writer"
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired != 0 || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "maintenance_windows" {
			return
		}
		fired++
		fresh := tx.Session(&gorm.Session{NewDB: true})
		tx.AddError(fresh.Model(&MaintenanceWindow{}).Where("token = ?", "partial-window").Update("slots", slots).Error)
	}))
	t.Cleanup(func() { require.NoError(t, api.RDB.Database.Callback().Query().Remove(callback)) })
	return &fired
}

func TestMaintenanceWindowConcurrentPartialRenamePreservesSchedule(t *testing.T) {
	api, ctx, before, statements := maintenanceWindowPartialFixture(t)
	fired := interleaveWindowSlots(t, api, partialWindowSlots)
	updated, err := api.UpdateMaintenanceWindow(ctx, before.Token, &MaintenanceWindowUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.Equal(t, 1, *fired)
	for _, row := range []*MaintenanceWindow{updated, reloadPartialWindow(t, api, ctx)} {
		require.Equal(t, "Renamed", row.Name.String)
		require.JSONEq(t, partialWindowSlots, string(row.Slots), "rename must preserve the other writer's schedule")
		require.Equal(t, before.Timezone, row.Timezone)
		require.Equal(t, before.Description, row.Description)
		require.Equal(t, before.Metadata == nil, row.Metadata == nil)
		if before.Metadata != nil {
			require.JSONEq(t, string(*before.Metadata), string(*row.Metadata))
		}
	}
	writes := customerPartialWrites(statements)
	require.Len(t, writes, 2, "one interleaved slots write and one rename: %v", writes)
	require.Contains(t, writes[0], "slots")
	require.NotContains(t, writes[1], "slots")
	require.NotContains(t, writes[1], "timezone")
	require.NotContains(t, writes[1], "metadata")
	require.NotContains(t, writes[1], "description")
}

func TestMaintenanceWindowPartialScheduleWrites(t *testing.T) {
	for _, field := range []string{"slots", "timezone"} {
		t.Run(field, func(t *testing.T) {
			api, ctx, before, statements := maintenanceWindowPartialFixture(t)
			request := &MaintenanceWindowUpdateRequest{}
			wantZone, wantSlots := before.Timezone, partialWindowSlots
			if field == "slots" {
				// Reordered keys and whitespace are normalized when slots are supplied.
				request.Slots = dcgraphql.OptionalStringOf(`[ { "durationMinutes":60, "start":"20:00", "day":"FRIDAY" } ]`)
			} else {
				interleaveWindowSlots(t, api, partialWindowSlots)
				request.Timezone = dcgraphql.OptionalStringOf("  UTC  ")
				wantZone = "UTC"
			}

			locks, writes := 0, 0
			var lockConnection gorm.ConnPool
			const queryCallback = "test:window_schedule_lock"
			require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
				if tx.Statement.Table != "maintenance_windows" {
					return
				}
				locking, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
				if !ok {
					return // Initial lookup and post-write reload do not need a lock.
				}
				require.Equal(t, "UPDATE", locking.Strength)
				_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
				require.True(t, inTransaction)
				locks++
				lockConnection = tx.Statement.ConnPool
			}))
			t.Cleanup(func() { require.NoError(t, api.RDB.Database.Callback().Query().Remove(queryCallback)) })
			const updateCallback = "test:window_schedule_write"
			require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
				assignments, ok := tx.Statement.Dest.(map[string]any)
				if !ok || assignments[field] == nil {
					return
				}
				require.Equal(t, 1, locks, "schedule lock must precede the write")
				require.Same(t, lockConnection, tx.Statement.ConnPool)
				writes++
			}))
			t.Cleanup(func() { require.NoError(t, api.RDB.Database.Callback().Update().Remove(updateCallback)) })

			updated, err := api.UpdateMaintenanceWindow(ctx, before.Token, request)
			require.NoError(t, err)
			require.Equal(t, 1, locks)
			require.Equal(t, 1, writes)
			for _, row := range []*MaintenanceWindow{updated, reloadPartialWindow(t, api, ctx)} {
				require.Equal(t, wantZone, row.Timezone)
				require.Equal(t, wantSlots, string(row.Slots))
				require.Equal(t, before.Name, row.Name)
				require.Equal(t, before.Description, row.Description)
				require.Equal(t, before.Metadata == nil, row.Metadata == nil)
				if before.Metadata != nil {
					require.JSONEq(t, string(*before.Metadata), string(*row.Metadata))
				}
			}
			allWrites := customerPartialWrites(statements)
			require.NotEmpty(t, allWrites)
			last := allWrites[len(allWrites)-1]
			require.Contains(t, last, field)
			other := "timezone"
			if field == "timezone" {
				other = "slots"
			}
			require.NotContains(t, last, other)
			require.NotContains(t, last, "metadata")
			require.NotContains(t, last, "description")
		})
	}
}

func TestMaintenanceWindowConcurrentPartialTimezoneValidatesCurrentSlots(t *testing.T) {
	api, ctx, before, statements := maintenanceWindowPartialFixture(t)
	// A malformed stored schedule simulates damage after the first read. Only
	// validation of the locked current row can detect it on a timezone-only edit.
	fired := interleaveWindowSlots(t, api, `[]`)
	_, err := api.UpdateMaintenanceWindow(ctx, before.Token, &MaintenanceWindowUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Refused"), Timezone: dcgraphql.OptionalStringOf("UTC"),
	})
	require.ErrorIs(t, err, maintwindow.ErrInvalid)
	require.Equal(t, 1, *fired)
	require.Len(t, customerPartialWrites(statements), 1, "only the interleaved writer may write")
	stored := reloadPartialWindow(t, api, ctx)
	require.Equal(t, before.Name, stored.Name)
	require.Equal(t, before.Timezone, stored.Timezone)
	require.Equal(t, `[]`, string(stored.Slots))
}

func TestMaintenanceWindowPartialInvalidInputWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit MaintenanceWindowUpdateRequest
	}{
		{"null timezone", MaintenanceWindowUpdateRequest{Timezone: dcgraphql.ClearedString()}},
		{"null slots", MaintenanceWindowUpdateRequest{Slots: dcgraphql.ClearedString()}},
		{"unknown timezone", MaintenanceWindowUpdateRequest{Timezone: dcgraphql.OptionalStringOf("Unknown/Zone")}},
		{"invalid slots", MaintenanceWindowUpdateRequest{Slots: dcgraphql.OptionalStringOf(`[]`)}},
		{"invalid metadata", MaintenanceWindowUpdateRequest{Metadata: dcgraphql.OptionalStringOf(`{`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, before, statements := maintenanceWindowPartialFixture(t)
			tc.edit.Name = dcgraphql.OptionalStringOf("Refused")
			_, err := api.UpdateMaintenanceWindow(ctx, before.Token, &tc.edit)
			require.Error(t, err)
			require.Empty(t, customerPartialWrites(statements))
			stored := reloadPartialWindow(t, api, ctx)
			require.Equal(t, before.Name, stored.Name)
			require.Equal(t, before.Timezone, stored.Timezone)
			require.Equal(t, string(before.Slots), string(stored.Slots))
			require.True(t, before.UpdatedAt.Equal(stored.UpdatedAt))
		})
	}
}

func TestMaintenanceWindowPartialEmptyInputRetainsTimestamp(t *testing.T) {
	api, ctx, before, statements := maintenanceWindowPartialFixture(t)
	updated, err := api.UpdateMaintenanceWindow(ctx, before.Token, &MaintenanceWindowUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	stored := reloadPartialWindow(t, api, ctx)
	require.True(t, before.UpdatedAt.Equal(updated.UpdatedAt))
	require.True(t, before.UpdatedAt.Equal(stored.UpdatedAt))
	_, err = api.UpdateMaintenanceWindow(ctx, "missing", &MaintenanceWindowUpdateRequest{})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
