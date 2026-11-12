// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func assetTypePartialFixture(t *testing.T) (*Api, context.Context, *AssetType, *rdbtest.StatementCounter) {
	t.Helper()
	family := assetTypeFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	rows, err := api.AssetTypesByToken(ctx, []string{family.Token})
	original := requireOne(t, "AssetType", rows, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, original, statements
}

func TestUpdateAssetTypeStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, original, _ := assetTypePartialFixture(t)
	fired := false
	const callback = "test:asset_type_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "asset_types" {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&AssetType{}).Where("id = ?", original.ID).
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateAssetType(ctx, original.Token, &AssetTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.AssetTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "AssetType", rows, err)
	for _, row := range []*AssetType{updated, stored} {
		require.Equal(t, "Renamed", nullStr(row.Name))
		require.Equal(t, "other writer", nullStr(row.Description), "the return value must be reloaded too")
	}
	require.True(t, updated.UpdatedAt.After(original.UpdatedAt))
}

func TestUpdateAssetTypeEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := assetTypePartialFixture(t)
	updated, err := api.UpdateAssetType(ctx, original.Token, &AssetTypeUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.AssetTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "AssetType", rows, err)
	require.Equal(t, original, stored, "the entire row, including updated_at, must be unchanged")
	require.Equal(t, original, updated)
}

func TestUpdateAssetTypeRefusedRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := assetTypePartialFixture(t)
	_, err := api.UpdateAssetType(ctx, original.Token, &AssetTypeUpdateRequest{
		Name:           dcgraphql.OptionalStringOf("Refused"),
		PropertySchema: dcgraphql.OptionalStringOf("not JSON"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.AssetTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "AssetType", rows, err)
	require.Equal(t, original, stored, "refusal must leave the entire row unchanged")
}
