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

func commandDefinitionStaleFixture(t *testing.T) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	family := commandDefinitionFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func TestUpdateCommandDefinitionStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, statements := commandDefinitionStaleFixture(t)
	fired := false
	const callback = "test:command_definition_stale_other_writer"
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "command_definitions" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has captured the old description. Guard before
		// writing so neither nested queries nor the final reload can fire again.
		fired = true
		fresh := api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true})
		tx.AddError(fresh.Model(&CommandDefinition{}).Where("token = ?", "cd-1").
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateCommandDefinition(ctx, "cd-1", &CommandDefinitionUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.CommandDefinitionsByToken(ctx, []string{"cd-1"})
	stored := requireOne(t, "command_definition", rows, err)
	for _, row := range []*CommandDefinition{stored, updated} {
		require.Equal(t, "Renamed", row.Name.String)
		require.Equal(t, "other writer", row.Description.String, "return value must be reloaded")
	}
	// The other writer and the requested rename each issue exactly one UPDATE;
	// the rename must include updated_at and omit all other entity columns.
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
	for _, column := range []string{"description", "metadata", "command_key", "impact", "encoder", "parameter_schema", "device_profile_id"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}

func TestUpdateCommandDefinitionEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, statements := commandDefinitionStaleFixture(t)
	var before CommandDefinition
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "cd-1").First(&before).Error)
	rows, err := api.CommandDefinitionsByToken(ctx, []string{"cd-1"})
	loaded := requireOne(t, "command_definition", rows, err)

	updated, err := api.UpdateCommandDefinition(ctx, "cd-1", &CommandDefinitionUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements), "an empty request must issue no writes")
	var stored CommandDefinition
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored, "the complete stored row, including updated_at, must stay unchanged")
	require.Equal(t, loaded, updated)
}

func TestUpdateCommandDefinitionUnknownProfileWritesNothing(t *testing.T) {
	api, ctx, statements := commandDefinitionStaleFixture(t)
	var before CommandDefinition
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "cd-1").First(&before).Error)

	_, err := api.UpdateCommandDefinition(ctx, "cd-1", &CommandDefinitionUpdateRequest{
		Name:               dcgraphql.OptionalStringOf("Refused"),
		DeviceProfileToken: dcgraphql.OptionalStringOf("no-such-profile"),
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Empty(t, customerPartialWrites(statements), "a refused request must issue no writes")
	var stored CommandDefinition
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored, "a refusal must preserve the complete row, including updated_at")
}
