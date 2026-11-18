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

func deviceStaleFixture(t *testing.T) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	family := deviceFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func TestUpdateDeviceStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, statements := deviceStaleFixture(t)
	fired := false
	const callback = "test:device_stale_other_writer"
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "devices" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has captured the old description. Guard before
		// writing so neither nested queries nor the final reload can fire again.
		fired = true
		fresh := api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true})
		tx.AddError(fresh.Model(&Device{}).Where("token = ?", "d-1").
			Update("description", "other writer").Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateDevice(ctx, "d-1", &DeviceUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.DevicesByToken(ctx, []string{"d-1"})
	stored := requireOne(t, "device", rows, err)
	for _, row := range []*Device{stored, updated} {
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
	for _, column := range []string{"id", "created_at", "deleted_at", "external_id", "description", "metadata", "device_type_id", "expected_since", "disabled_at", "gateway_id", "expected_provisioning_profile_id", "token", "tenant_id"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}

func TestUpdateDeviceEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, statements := deviceStaleFixture(t)
	var before Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "d-1").First(&before).Error)
	rows, err := api.DevicesByToken(ctx, []string{"d-1"})
	loaded := requireOne(t, "device", rows, err)

	updated, err := api.UpdateDevice(ctx, "d-1", &DeviceUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements), "an empty request must issue no writes")
	var stored Device
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored, "the complete stored row, including updated_at, must stay unchanged")
	require.Equal(t, loaded, updated)
}

func TestUpdateDeviceUnknownDeviceTypeWritesNothing(t *testing.T) {
	api, ctx, statements := deviceStaleFixture(t)
	var before Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "d-1").First(&before).Error)

	_, err := api.UpdateDevice(ctx, "d-1", &DeviceUpdateRequest{
		Name:            dcgraphql.OptionalStringOf("Refused"),
		DeviceTypeToken: dcgraphql.OptionalStringOf("no-such-type"),
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Empty(t, customerPartialWrites(statements), "a refused request must issue no writes")
	var stored Device
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored, "a refusal must preserve the complete row, including updated_at")
}

func TestUpdateDeviceEnabledWritesDisabledAtAndExpectedSince(t *testing.T) {
	api, ctx, statements := deviceStaleFixture(t)
	updated, err := api.UpdateDevice(ctx, "d-1", &DeviceUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(false),
	})
	require.NoError(t, err)
	require.True(t, updated.DisabledAt.Valid)
	require.True(t, updated.ExpectedSince.Valid)
	var updates []string
	for _, sql := range customerPartialWrites(statements) {
		if strings.HasPrefix(sql, "UPDATE `devices` ") {
			updates = append(updates, sql)
		}
	}
	require.Len(t, updates, 1)
	for _, column := range []string{"disabled_at", "expected_since", "updated_at"} {
		require.Contains(t, updates[0], "`"+column+"`=")
	}
	for _, column := range []string{"name", "description", "metadata", "external_id", "device_type_id", "gateway_id", "expected_provisioning_profile_id"} {
		require.NotContains(t, updates[0], "`"+column+"`=")
	}
	rows, err := api.DevicesById(ctx, []uint{updated.ID})
	stored := requireOne(t, "device", rows, err)
	require.Equal(t, stored, updated)
}

func TestUpdateDeviceAliasesOnlyDoesNotUpdateDevice(t *testing.T) {
	api, ctx, statements := deviceStaleFixture(t)
	var before Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "d-1").First(&before).Error)
	updated, err := api.UpdateDevice(ctx, "d-1", &DeviceUpdateRequest{
		Aliases: OptionalDeviceAliasListOf([]DeviceAliasInput{{Scheme: "custom.acme", Value: "replacement"}}),
	})
	require.NoError(t, err)
	for _, sql := range customerPartialWrites(statements) {
		require.False(t, strings.HasPrefix(sql, "UPDATE `devices` "), "aliases alone must not update the device: %s", sql)
	}
	var stored Device
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored)
	aliases, err := api.DeviceAliases(ctx, updated.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	require.Equal(t, "replacement", aliases[0].Value)
}
