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

func entityRelationshipTypePartialFixture(t *testing.T) (*Api, context.Context, *EntityRelationshipType, *rdbtest.StatementCounter) {
	t.Helper()
	family := entityRelationshipTypeFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	rows, err := api.EntityRelationshipTypesByToken(ctx, []string{family.Token})
	original := requireOne(t, "EntityRelationshipType", rows, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, original, statements
}

func TestUpdateEntityRelationshipTypeStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, original, statements := entityRelationshipTypePartialFixture(t)
	fired := false
	const callback = "test:entity_relationship_type_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "entity_relationship_types" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&EntityRelationshipType{}).Where("id = ?", original.ID).
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateEntityRelationshipType(ctx, original.Token, &EntityRelationshipTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.EntityRelationshipTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "EntityRelationshipType", rows, err)
	for _, row := range []*EntityRelationshipType{updated, stored} {
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
	for _, column := range []string{"id", "tenant_id", "token", "created_at", "deleted_at", "description", "metadata", "tracked", "semantic_predicate", "semantic_inverse"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}

func TestUpdateEntityRelationshipTypeEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := entityRelationshipTypePartialFixture(t)
	updated, err := api.UpdateEntityRelationshipType(ctx, original.Token, &EntityRelationshipTypeUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.EntityRelationshipTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "EntityRelationshipType", rows, err)
	require.Equal(t, original, stored, "the entire row, including updated_at, must be unchanged")
	require.Equal(t, original, updated)
}

func TestUpdateEntityRelationshipTypeRefusedRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := entityRelationshipTypePartialFixture(t)
	_, err := api.UpdateEntityRelationshipType(ctx, original.Token, &EntityRelationshipTypeUpdateRequest{
		Name:     dcgraphql.OptionalStringOf("Refused"),
		Metadata: dcgraphql.OptionalStringOf("not JSON"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.EntityRelationshipTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "EntityRelationshipType", rows, err)
	require.Equal(t, original, stored, "refusal must leave the entire row unchanged")
}

func TestUpdateEntityRelationshipTypeOmittedTrackedDoesNotRevertConcurrentFlip(t *testing.T) {
	api, ctx, original, statements := entityRelationshipTypePartialFixture(t)
	fired := false
	const callback = "test:entity_relationship_type_other_tracked_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "entity_relationship_types" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&EntityRelationshipType{}).Where("id = ?", original.ID).
			Update("tracked", false).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateEntityRelationshipType(ctx, original.Token, &EntityRelationshipTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.EntityRelationshipTypesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "EntityRelationshipType", rows, err)
	for _, row := range []*EntityRelationshipType{updated, stored} {
		require.Equal(t, "Renamed", nullStr(row.Name))
		require.False(t, row.Tracked, "the return value must preserve the concurrent tracked flip too")
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
	for _, column := range []string{"id", "tenant_id", "token", "created_at", "deleted_at", "description", "metadata", "tracked", "semantic_predicate", "semantic_inverse"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}
