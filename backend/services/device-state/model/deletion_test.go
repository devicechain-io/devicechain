// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

func deletionUpdate(tenant, token string, at time.Time) ProjectionUpdate {
	return ProjectionUpdate{Tenant: tenant, DeviceToken: token, OccurredAt: at,
		Identity:     DeviceIdentity{Owners: Owners{Anchors: []string{"customer:old-owner"}, At: at}},
		Measurements: []LatestMeasurementInput{{Name: "temperature", Value: sql.NullFloat64{Float64: 17, Valid: true}, OccurredTime: at}},
		Locations:    []LatestLocationInput{{Latitude: sql.NullFloat64{Float64: 42, Valid: true}, OccurredTime: at}},
		Dropped:      []DroppedMeasurementInput{{Name: "text", Reason: resolvedevent.DroppedReasons[0], Value: "private", OccurredTime: at}}}
}

func requireLiveCounts(t *testing.T, api *Api, tenant, token string, want int64) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	for _, table := range []any{&DeviceState{}, &LatestMeasurement{}, &LatestLocation{}, &DroppedMeasurement{}} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(table).Where("device_token = ?", token).Count(&n).Error)
		require.Equal(t, want, n, "%T tenant=%s token=%s", table, tenant, token)
	}
}

func TestADeletedDevicesLiveStateIsGoneAndAReCreatedDeviceStartsEmpty(t *testing.T) {
	api := newSQLiteProjectionApi(t, "delete")
	ctx := core.WithTenant(context.Background(), "a")
	at := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{deletionUpdate("a", "truck-7", at), deletionUpdate("a", "other", at), deletionUpdate("b", "truck-7", at)}))
	requireLiveCounts(t, api, "a", "truck-7", 1)
	// Include a soft-deleted parent: deletion is always unscoped and hard.
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", "truck-7").Delete(&DeviceState{}).Error)
	applied, err := api.ApplyDeviceDeleted(ctx, "truck-7", 17, time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, applied)
	requireLiveCounts(t, api, "a", "truck-7", 0)
	requireLiveCounts(t, api, "a", "other", 1)
	requireLiveCounts(t, api, "b", "truck-7", 1)
	applied, err = api.ApplyDeviceDeleted(ctx, "truck-7", 17, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, applied)
	row, err := api.MergeDeviceState(ctx, "truck-7", at.Add(time.Hour), nil, DeviceIdentity{})
	require.NoError(t, err)
	require.False(t, row.Incarnation.Valid)
	require.Empty(t, row.ScopeAnchors)
	for _, table := range []any{&LatestMeasurement{}, &LatestLocation{}, &DroppedMeasurement{}} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(table).Where("device_token = ?", "truck-7").Count(&n).Error)
		require.Zero(t, n)
	}
}

func TestDeviceDeletionCannotEraseANewerIncarnation(t *testing.T) {
	for _, stamped := range []bool{false, true} {
		t.Run(map[bool]string{false: "new unstamped row", true: "new incarnation"}[stamped], func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "newer")
			ctx := core.WithTenant(context.Background(), "a")
			cutoff := time.Now().UTC().Add(-time.Hour)
			require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{deletionUpdate("a", "truck-7", cutoff)}))
			if stamped {
				require.NoError(t, api.RDB.DB(ctx).Model(&DeviceState{}).Where("device_token = ?", "truck-7").UpdateColumn("incarnation", 23).Error)
			}
			applied, err := api.ApplyDeviceDeleted(ctx, "truck-7", 17, cutoff)
			require.NoError(t, err)
			require.False(t, applied)
			requireLiveCounts(t, api, "a", "truck-7", 1)
		})
	}
}

func TestParentlessDeletionBoundsEveryChildByItsWriteTime(t *testing.T) {
	api := newSQLiteProjectionApi(t, "children")
	ctx := core.WithTenant(context.Background(), "a")
	cutoff := time.Now().UTC()
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{deletionUpdate("a", "truck-7", cutoff)}))
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Where("device_token = ?", "truck-7").Delete(&DeviceState{}).Error)
	for _, table := range []any{&LatestMeasurement{}, &LatestLocation{}, &DroppedMeasurement{}} {
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(table).Where("device_token = ?", "truck-7").UpdateColumn("updated_at", cutoff.Add(time.Hour)).Error)
	}
	applied, err := api.ApplyDeviceDeleted(ctx, "truck-7", 17, cutoff)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = api.ApplyDeviceDeleted(ctx, "truck-7", 17, cutoff.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, applied)
	requireLiveCounts(t, api, "a", "truck-7", 0)
}

func TestProjectionBatchNeverSetsOrClearsIncarnation(t *testing.T) {
	api := newSQLiteProjectionApi(t, "stamp")
	ctx := core.WithTenant(context.Background(), "a")
	at := time.Now().UTC()
	for _, pt := range []*PresenceTransition{nil, {Claim: presence.ClaimConnected, SessionId: 1, OccurredAt: at}} {
		u := deletionUpdate("a", "truck-7", at)
		u.Presence = pt
		require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{u}))
		var row DeviceState
		require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
		if pt == nil {
			require.False(t, row.Incarnation.Valid)
		} else {
			require.Equal(t, sql.NullInt64{Int64: 23, Valid: true}, row.Incarnation)
		}
		require.NoError(t, api.RDB.DB(ctx).Model(&DeviceState{}).Where("id = ?", row.ID).UpdateColumn("incarnation", 23).Error)
	}
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{deletionUpdate("a", "truck-7", at.Add(time.Minute))}))
	var row DeviceState
	require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
	require.Equal(t, int64(23), row.Incarnation.Int64)
	require.True(t, row.Incarnation.Valid)
	cols, err := foldColumns(api.RDB.DB(ctx))
	require.NoError(t, err)
	require.NotContains(t, cols, "incarnation")
}

func TestOrphanReconcileRechecksRowIdentity(t *testing.T) {
	api := newSQLiteProjectionApi(t, "race")
	ctx := core.WithTenant(context.Background(), "a")
	row, err := api.MergeDeviceState(ctx, "truck-7", time.Now(), nil, DeviceIdentity{})
	require.NoError(t, err)
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(row).Error)
	replacement, err := api.MergeDeviceState(ctx, "truck-7", time.Now(), nil, DeviceIdentity{})
	require.NoError(t, err)
	d, st, err := api.ReconcileDeviceState(ctx, *row, 0, time.Time{})
	require.NoError(t, err)
	require.False(t, d)
	require.False(t, st)
	var found DeviceState
	require.NoError(t, api.RDB.DB(ctx).First(&found).Error)
	require.Equal(t, replacement.ID, found.ID)
}

func TestIncarnationMigrationIsNullableAndRerunnable(t *testing.T) {
	api := newSQLiteProjectionApi(t, "migration")
	db := api.RDB.Database
	require.NoError(t, db.Migrator().DropColumn(&DeviceState{}, "Incarnation"))
	m := NewDeviceStateIncarnationSchema()
	require.NoError(t, m.Migrate(db))
	require.NoError(t, m.Migrate(db))
	ctx := core.WithTenant(context.Background(), "a")
	require.NoError(t, api.RDB.DB(ctx).Create(&DeviceState{DeviceToken: "truck-7"}).Error)
	var row DeviceState
	require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
	require.False(t, row.Incarnation.Valid)
}

func TestDeviceDeletionUsesStampedIncarnationInsteadOfTimestamp(t *testing.T) {
	for _, id := range []int64{16, 17} {
		api := newSQLiteProjectionApi(t, "stamped")
		ctx := core.WithTenant(context.Background(), "a")
		cutoff := time.Now().UTC().Add(-time.Hour)
		require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{deletionUpdate("a", "truck-7", cutoff)}))
		require.NoError(t, api.RDB.DB(ctx).Model(&DeviceState{}).Where("device_token = ?", "truck-7").UpdateColumn("incarnation", id).Error)
		applied, err := api.ApplyDeviceDeleted(ctx, "truck-7", 17, cutoff)
		require.NoError(t, err)
		require.True(t, applied)
		requireLiveCounts(t, api, "a", "truck-7", 0)
	}
}
