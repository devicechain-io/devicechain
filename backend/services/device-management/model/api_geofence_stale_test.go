// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func geoFencePartialFixture(t *testing.T) (*Api, context.Context, *GeoFence, *rdbtest.StatementCounter) {
	t.Helper()
	family := geoFenceFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	rows, err := api.GeoFencesByToken(ctx, []string{family.Token})
	original := requireOne(t, "GeoFence", rows, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, original, statements
}

func TestUpdateGeoFenceStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, original, statements := geoFencePartialFixture(t)
	fired := false
	const callback = "test:geo_fence_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "geo_fences" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&GeoFence{}).Where("id = ?", original.ID).
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateGeoFence(ctx, original.Token, &GeoFenceUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.GeoFencesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "GeoFence", rows, err)
	for _, row := range []*GeoFence{updated, stored} {
		require.Equal(t, "Renamed", nullStr(row.Name))
		require.Equal(t, "other writer", nullStr(row.Description), "the return value must be reloaded too")
	}
	require.True(t, updated.UpdatedAt.After(original.UpdatedAt))

	var updates []string
	for _, sql := range customerPartialWrites(statements) {
		if strings.HasPrefix(sql, "UPDATE ") {
			updates = append(updates, sql)
		} else {
			require.True(t, strings.HasPrefix(sql, "INSERT INTO `audit_events` "), "unexpected write: %s", sql)
		}
	}
	require.Len(t, updates, 2)
	require.Contains(t, updates[1], "`name`=")
	require.Contains(t, updates[1], "`updated_at`=")
	for _, column := range []string{"id", "tenant_id", "token", "created_at", "deleted_at", "description", "metadata", "geometry"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}

func TestUpdateGeoFenceEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := geoFencePartialFixture(t)
	updated, err := api.UpdateGeoFence(ctx, original.Token, &GeoFenceUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.GeoFencesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "GeoFence", rows, err)
	require.Equal(t, original, stored, "the entire row, including updated_at, must be unchanged")
	require.Equal(t, original, updated)
}

func TestUpdateGeoFenceRefusedRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := geoFencePartialFixture(t)
	_, err := api.UpdateGeoFence(ctx, original.Token, &GeoFenceUpdateRequest{
		Name:     dcgraphql.OptionalStringOf("Refused"),
		Metadata: dcgraphql.OptionalStringOf("not JSON"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.GeoFencesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "GeoFence", rows, err)
	require.Equal(t, original, stored, "refusal must leave the entire row unchanged")
}
