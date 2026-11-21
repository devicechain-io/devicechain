// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestHolderAbsenceBackoffProgressionAndCap(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	require.NoError(t, api.RDB.DB(ctx).Create(&EntityDeletionWatermark{
		EntityType: "device", EntityToken: "gone", DeletedTime: now.Add(-time.Hour),
	}).Error)
	for _, hours := range []int{1, 2, 4, 8, 16, 32, 64, 128, 168, 168, 168} {
		page, err := api.UnheldDeletedDeviceTokensAfter(ctx, "", now)
		require.NoError(t, err)
		require.Len(t, page, 1, "absence must never age out permanently")
		require.NoError(t, api.RecordDeviceTokenAbsent(ctx, page[0], now))
		// Another replica answering the same snapshot cannot advance its schedule.
		require.NoError(t, api.RecordDeviceTokenAbsent(ctx, page[0], now.Add(time.Minute)))
		var w EntityDeletionWatermark
		require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
		require.NotNil(t, w.HolderCheckAfter)
		due := now.Add(time.Duration(hours) * time.Hour)
		require.True(t, due.Equal(*w.HolderCheckAfter), "got %v; want %v", w.HolderCheckAfter, due)
		require.Equal(t, hours*3600, w.HolderCheckDelaySeconds)
		// A new process reads the durable schedule and excludes not-yet-due tokens
		// before applying the page limit.
		api = NewApi(api.RDB)
		page, err = api.UnheldDeletedDeviceTokensAfter(ctx, "", due.Add(-time.Nanosecond))
		require.NoError(t, err)
		require.Empty(t, page)
		now = due
	}
}

func TestHolderAbsenceScheduleIsScopedToTenantAndDeletion(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, tenant := range []context.Context{ctx, other} {
		require.NoError(t, api.RDB.DB(tenant).Create(&EntityDeletionWatermark{
			EntityType: "device", EntityToken: "same", DeletedTime: now.Add(-time.Hour),
		}).Error)
	}
	page, err := api.UnheldDeletedDeviceTokensAfter(ctx, "", now)
	require.NoError(t, err)
	require.Len(t, page, 1)
	first := page[0]
	require.NoError(t, api.RecordDeviceTokenAbsent(ctx, first, now))
	page, err = api.UnheldDeletedDeviceTokensAfter(other, "", now)
	require.NoError(t, err)
	require.Len(t, page, 1, "another tenant's same token keeps its own schedule")
	require.Nil(t, page[0].CheckAfter)
	require.NoError(t, api.RecordDeviceTokenHeld(other, "same", now))
	page, err = api.UnheldDeletedDeviceTokensAfter(ctx, "", now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, page, 1, "another tenant's holder does not hide this token")
	stale := page[0]
	// Simulate an old pod recording a newer deletion without knowing the new columns.
	newDeletion := now.Add(30 * time.Minute)
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityDeletionWatermark{}).
		Where("entity_token = ?", "same").Update("deleted_time", newDeletion).Error)
	require.NoError(t, api.RecordDeviceTokenAbsent(ctx, stale, now.Add(time.Hour)))
	var w EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
	require.True(t, w.HolderCheckedDeletion.Equal(first.DeletedTime), "stale answer must not update a newer deletion")
	require.True(t, w.HolderCheckAfter.Equal(now.Add(time.Hour)), "stale answer must leave the schedule untouched")
	page, err = api.UnheldDeletedDeviceTokensAfter(ctx, "", now.Add(31*time.Minute))
	require.NoError(t, err)
	require.Len(t, page, 1, "new deletion bypasses the previous deletion's backoff")
	require.Zero(t, page[0].CheckDelaySeconds, "new deletion starts at one hour again")
	require.NoError(t, api.RecordDeviceTokenAbsent(ctx, page[0], now.Add(31*time.Minute)))
	require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
	require.Equal(t, 3600, w.HolderCheckDelaySeconds)
	require.True(t, w.HolderCheckedDeletion.Equal(newDeletion))
	require.Error(t, api.RecordDeviceTokenAbsent(context.Background(), first, now), "missing tenant must fail closed")
}

func TestHolderCheckBackoffMigrationOwnsSnapshotAndAppends(t *testing.T) {
	require.Equal(t, []string{
		`ALTER TABLE "event-management".entity_deletion_watermarks ADD COLUMN IF NOT EXISTS holder_checked_deletion timestamptz NULL`,
		`ALTER TABLE "event-management".entity_deletion_watermarks ADD COLUMN IF NOT EXISTS holder_check_after timestamptz NULL`,
		`ALTER TABLE "event-management".entity_deletion_watermarks ADD COLUMN IF NOT EXISTS holder_check_delay_seconds integer NOT NULL DEFAULT 0`,
	}, holderCheckBackoffDDL)
	m := NewHolderCheckBackoffSchema()
	require.Equal(t, "20261120010000", m.ID)
	require.Equal(t, migrationPosition(t, NewDeviceTokenHoldersSchema().ID)+1, migrationPosition(t, m.ID))
}
