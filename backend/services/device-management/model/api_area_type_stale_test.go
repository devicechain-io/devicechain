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

func areaTypePartialFixture(t *testing.T) (*Api, context.Context, *AreaType, *rdbtest.StatementCounter) {
	t.Helper()
	family := areaTypeFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	rows, err := api.AreaTypesByToken(ctx, []string{family.Token})
	original := requireOne(t, "AreaType", rows, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, original, statements
}

func TestUpdateAreaTypeStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, original, statements := areaTypePartialFixture(t)
	fired := false
	const callback = "test:area_type_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "area_types" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&AreaType{}).Where("id = ?", original.ID).
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateAreaType(ctx, original.Token, &AreaTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.AreaTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "AreaType", rows, err)
	for _, row := range []*AreaType{updated, stored} {
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
	for _, column := range []string{"id", "tenant_id", "token", "created_at", "deleted_at", "description", "metadata", "image_url", "icon", "background_color", "foreground_color", "border_color", "semantic_types"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}

func TestUpdateAreaTypeEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := areaTypePartialFixture(t)
	updated, err := api.UpdateAreaType(ctx, original.Token, &AreaTypeUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.AreaTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "AreaType", rows, err)
	require.Equal(t, original, stored, "the entire row, including updated_at, must be unchanged")
	require.Equal(t, original, updated)
}

func TestUpdateAreaTypeRefusedRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := areaTypePartialFixture(t)
	_, err := api.UpdateAreaType(ctx, original.Token, &AreaTypeUpdateRequest{
		Name:     dcgraphql.OptionalStringOf("Refused"),
		Metadata: dcgraphql.OptionalStringOf("not JSON"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.AreaTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "AreaType", rows, err)
	require.Equal(t, original, stored, "refusal must leave the entire row unchanged")
}
