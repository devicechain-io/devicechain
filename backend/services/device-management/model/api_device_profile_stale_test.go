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

func deviceProfilePartialFixture(t *testing.T) (*Api, context.Context, *DeviceProfile, *rdbtest.StatementCounter) {
	t.Helper()
	family := deviceProfileFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	rows, err := api.DeviceProfilesByToken(ctx, []string{family.Token})
	original := requireOne(t, "DeviceProfile", rows, err)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, original, statements
}

func TestUpdateDeviceProfileStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, original, _ := deviceProfilePartialFixture(t)
	fired := false
	const callback = "test:device_profile_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "device_profiles" {
			return
		}
		// The first entity read has completed (including preloads). Leave its
		// snapshot stale and change a field the request will omit.
		fired = true
		tx.AddError(api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true}).
			Model(&DeviceProfile{}).Where("id = ?", original.ID).
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateDeviceProfile(ctx, original.Token, &DeviceProfileUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.DeviceProfilesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "DeviceProfile", rows, err)
	for _, row := range []*DeviceProfile{updated, stored} {
		require.Equal(t, "Renamed", nullStr(row.Name))
		require.Equal(t, "other writer", nullStr(row.Description), "the return value must be reloaded too")
	}
	require.True(t, updated.UpdatedAt.After(original.UpdatedAt))
}

func TestUpdateDeviceProfileEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := deviceProfilePartialFixture(t)
	updated, err := api.UpdateDeviceProfile(ctx, original.Token, &DeviceProfileUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.DeviceProfilesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "DeviceProfile", rows, err)
	require.Equal(t, original, stored, "the entire row, including updated_at, must be unchanged")
	require.Equal(t, original, updated)
}

func TestUpdateDeviceProfileRefusedRequestWritesNothing(t *testing.T) {
	api, ctx, original, statements := deviceProfilePartialFixture(t)
	_, err := api.UpdateDeviceProfile(ctx, original.Token, &DeviceProfileUpdateRequest{
		Name:     dcgraphql.OptionalStringOf("Refused"),
		Metadata: dcgraphql.OptionalStringOf("not JSON"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements))
	rows, err := api.DeviceProfilesByToken(ctx, []string{original.Token})
	stored := requireOne(t, "DeviceProfile", rows, err)
	require.Equal(t, original, stored, "refusal must leave the entire row unchanged")
}

func TestUpdateDeviceProfileClearLocationWritesNull(t *testing.T) {
	api, ctx, original, _ := deviceProfilePartialFixture(t)
	require.NotNil(t, original.LocationDeclaration)
	updated, err := api.UpdateDeviceProfile(ctx, original.Token, &DeviceProfileUpdateRequest{
		Location: ClearedLocationDeclaration(),
	})
	require.NoError(t, err)
	require.Nil(t, updated.LocationDeclaration)
	var stored DeviceProfile
	require.NoError(t, api.RDB.DB(ctx).First(&stored, original.ID).Error)
	require.Nil(t, stored.LocationDeclaration)
	require.Equal(t, original.Name, stored.Name)
	require.Equal(t, original.Metadata, stored.Metadata)
}
