// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func newAreaBoundaryTestApi(t *testing.T, linked bool) (*Api, context.Context, *Area) {
	t.Helper()
	api := newGeoFenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&AreaType{}))
	_, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "floor"})
	require.NoError(t, err)
	_, err = api.CreateGeoFence(ctx, &GeoFenceCreateRequest{Token: "f", Geometry: yardGeometry})
	require.NoError(t, err)
	name, description, metadata := "Original", "Keep description", `{"keep":true}`
	request := &AreaCreateRequest{
		Token: "a", AreaTypeToken: "floor", Name: &name, Description: &description, Metadata: &metadata,
	}
	if linked {
		fence := "f"
		request.BoundaryFenceToken = &fence
	}
	area, err := api.CreateArea(ctx, request)
	require.NoError(t, err)
	return api, ctx, area
}

func TestUpdateAreaNameOnlyStaleDoesNotRestoreRemovedBoundary(t *testing.T) {
	api, ctx, area := newAreaBoundaryTestApi(t, true)
	db := api.RDB.DB(ctx)
	fired := 0
	const callback = "test:area_stale_unlink"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if fired != 0 || tx.Statement.Table != "areas" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The SELECT has populated the old boundary. Mutate through the same DB,
		// with a clean statement, before UpdateArea can build its write.
		fired++ // also guards against re-entry from any nested query
		fresh := tx.Session(&gorm.Session{NewDB: true})
		require.NoError(t, fresh.Model(&Area{}).Where("id = ?", area.ID).
			Update("boundary_fence_token", nil).Error)
		require.NoError(t, fresh.Where("token = ?", "f").Delete(&GeoFence{}).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)
	require.Equal(t, 1, fired, "the stale-read interleaving must actually execute once")
	require.Equal(t, "Renamed", updated.Name.String)
	require.False(t, updated.BoundaryFenceToken.Valid, "the return value must be reloaded")
	require.NotNil(t, updated.AreaType)
	require.Equal(t, "floor", updated.AreaType.Token)
	var stored Area
	require.NoError(t, db.First(&stored, area.ID).Error)
	require.Equal(t, "Renamed", stored.Name.String)
	require.False(t, stored.BoundaryFenceToken.Valid)
	var count int64
	require.NoError(t, db.Model(&GeoFence{}).Where("token = ?", "f").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&Area{}).Where("boundary_fence_token = ?", "f").Count(&count).Error)
	require.Zero(t, count, "no area may retain a dangling reference")
}

func TestUpdateAreaBoundaryPartialWrites(t *testing.T) {
	t.Run("existing fence is locked in the write transaction", func(t *testing.T) {
		api, ctx, _ := newAreaBoundaryTestApi(t, false)
		db := api.RDB.DB(ctx)
		locks, writes := 0, 0
		var lockConnection gorm.ConnPool
		const queryCallback = "test:area_boundary_lock"
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
			if tx.Statement.Table != "geo_fences" {
				return
			}
			locking, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
			require.True(t, ok, "the fence lookup must take the lock path even on sqlite")
			require.Equal(t, "UPDATE", locking.Strength)
			_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
			require.True(t, inTransaction)
			locks++
			lockConnection = tx.Statement.ConnPool
		}))
		t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(queryCallback)) })
		const updateCallback = "test:area_boundary_write"
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
			require.Equal(t, "areas", tx.Statement.Table)
			require.Equal(t, 1, locks, "lock must precede write")
			require.Same(t, lockConnection, tx.Statement.ConnPool, "lock and write must share the transaction")
			writes++
		}))
		t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(updateCallback)) })
		updated, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{BoundaryFenceToken: dcgraphql.OptionalStringOf("f")})
		require.NoError(t, err)
		require.Equal(t, 1, locks)
		require.Equal(t, 1, writes)
		require.True(t, updated.BoundaryFenceToken.Valid)
		require.Equal(t, "f", updated.BoundaryFenceToken.String)
	})

	t.Run("unknown fence refuses the entire write", func(t *testing.T) {
		api, ctx, original := newAreaBoundaryTestApi(t, false)
		writes := captureAreaBoundaryUpdates(t, api)
		_, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{
			Name: dcgraphql.OptionalStringOf("Refused"), BoundaryFenceToken: dcgraphql.OptionalStringOf("missing"),
		})
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		require.Empty(t, *writes)
		rows, err := api.AreasByToken(ctx, []string{"a"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, original.Name, rows[0].Name)
		require.False(t, rows[0].BoundaryFenceToken.Valid)
		require.True(t, original.UpdatedAt.Equal(rows[0].UpdatedAt))
	})

	t.Run("explicit null unlinks", func(t *testing.T) {
		api, ctx, _ := newAreaBoundaryTestApi(t, true)
		updated, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{BoundaryFenceToken: dcgraphql.OptionalString{Set: true}})
		require.NoError(t, err)
		require.False(t, updated.BoundaryFenceToken.Valid)
		var stored Area
		require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "a").First(&stored).Error)
		require.False(t, stored.BoundaryFenceToken.Valid)
	})

	t.Run("empty request does not write or advance updated_at", func(t *testing.T) {
		api, ctx, original := newAreaBoundaryTestApi(t, true)
		writes := captureAreaBoundaryUpdates(t, api)
		updated, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{})
		require.NoError(t, err)
		require.Empty(t, *writes)
		require.True(t, original.UpdatedAt.Equal(updated.UpdatedAt))
		var stored Area
		require.NoError(t, api.RDB.DB(ctx).First(&stored, original.ID).Error)
		require.True(t, original.UpdatedAt.Equal(stored.UpdatedAt))
	})

	t.Run("rename SQL omits all unrequested fields", func(t *testing.T) {
		api, ctx, original := newAreaBoundaryTestApi(t, true)
		writes := captureAreaBoundaryUpdates(t, api)
		updated, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
		require.NoError(t, err)
		require.Len(t, *writes, 1, "associations must not be updated")
		sql := (*writes)[0]
		require.Contains(t, sql, "UPDATE `areas`")
		require.Contains(t, sql, "`name`=")
		require.Contains(t, sql, "`updated_at`=")
		for _, omitted := range []string{"boundary_fence_token", "description", "metadata", "area_type_id"} {
			require.NotContains(t, sql, omitted)
		}
		require.Equal(t, original.Description, updated.Description)
		require.Equal(t, original.Metadata, updated.Metadata)
		require.Equal(t, original.BoundaryFenceToken, updated.BoundaryFenceToken)
		require.True(t, updated.UpdatedAt.After(original.UpdatedAt))
	})
}

// Capture all UPDATE statements, including any accidental association writes.
func captureAreaBoundaryUpdates(t *testing.T, api *Api) *[]string {
	t.Helper()
	writes := []string{}
	const callback = "test:area_boundary_capture_updates"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		writes = append(writes, tx.Statement.SQL.String())
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Update().Remove(callback)) })
	return &writes
}

func TestUpdateAreaStaleUnknownTypeRefusesWithoutWrites(t *testing.T) {
	api, ctx, original := newAreaBoundaryTestApi(t, true)
	writes := captureAreaBoundaryUpdates(t, api)
	_, err := api.UpdateArea(ctx, "a", &AreaUpdateRequest{
		AreaTypeToken: dcgraphql.OptionalStringOf("missing"), Name: dcgraphql.OptionalStringOf("Refused"),
		BoundaryFenceToken: dcgraphql.OptionalString{Set: true},
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Empty(t, *writes)
	rows, err := api.AreasByToken(ctx, []string{"a"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, original.Name, rows[0].Name)
	require.Equal(t, original.AreaTypeId, rows[0].AreaTypeId)
	require.Equal(t, original.BoundaryFenceToken, rows[0].BoundaryFenceToken)
	require.True(t, original.UpdatedAt.Equal(rows[0].UpdatedAt))
}
