// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A nil embedded store makes ANY unexpected secret operation fail immediately.
// The fixture seeds through the real store before replacing it with this guard.
type channelPartialNoSecrets struct{ secrets.SecretStore }

func channelPartialFixture(t *testing.T) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	family := notificationChannelFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	api.Secrets = channelPartialNoSecrets{}
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func channelPartialWrites(statements *rdbtest.StatementCounter) []string {
	var writes []string
	for _, sql := range statements.MarkedSQL() {
		if strings.HasPrefix(sql, "UPDATE ") || strings.HasPrefix(sql, "INSERT ") || strings.HasPrefix(sql, "DELETE ") {
			writes = append(writes, strings.NewReplacer("`", "", "\"", "").Replace(sql))
		}
	}
	return writes
}

func channelPartialUpdates(t *testing.T, statements *rdbtest.StatementCounter) []string {
	t.Helper()
	var updates []string
	for _, sql := range channelPartialWrites(statements) {
		if strings.HasPrefix(sql, "INSERT INTO audit_events ") {
			continue
		}
		require.True(t, strings.HasPrefix(sql, "UPDATE notification_channels SET "), "unexpected write: %s", sql)
		updates = append(updates, sql)
	}
	return updates
}

func TestChannelRenamePartialConcurrentPreservesDisable(t *testing.T) {
	api, ctx, statements := channelPartialFixture(t)
	fired := 0
	const callback = "test:channel_partial_other_writer"
	db := api.RDB.Database
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired != 0 || tx.Statement.Table != "notification_channels" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The SELECT has populated enabled=true. Disable before the rename writes.
		fired++
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&NotificationChannel{}).
			Where("token = ?", channelToken).Update("enabled", false).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })
	updated, err := api.UpdateNotificationChannel(ctx, channelToken, &NotificationChannelUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	})
	require.NoError(t, err)
	require.Equal(t, 1, fired)
	stored := channelRow(t, api, ctx, channelToken)
	for _, row := range []*NotificationChannel{stored, updated} {
		require.Equal(t, "Renamed", row.Name.String)
		require.False(t, row.Enabled, "rename must preserve the concurrent disable and reload it")
		require.Equal(t, channelSeed.ChannelType, row.ChannelType)
		require.JSONEq(t, channelSeed.Config, jsonStr(row.Config))
	}
	writes := channelPartialUpdates(t, statements)
	require.Len(t, writes, 2, "only the injected disable and rename may write")
	renames := 0
	for _, sql := range writes {
		if !strings.Contains(sql, "name=") {
			continue
		}
		renames++
		require.Contains(t, sql, "UPDATE notification_channels SET ")
		require.Contains(t, sql, "updated_at=")
		for _, omitted := range []string{"enabled=", "config=", "channel_type=", "metadata=", "description=", "token=", "tenant_id="} {
			// Inspect assignments only; token and tenant_id belong in the predicate.
			require.NotContains(t, strings.SplitN(sql, " WHERE ", 2)[0], omitted)
		}
	}
	require.Equal(t, 1, renames)
}

func TestChannelPartialConcurrentExplicitFalseAndNull(t *testing.T) {
	api, ctx, statements := channelPartialFixture(t)
	updated, err := api.UpdateNotificationChannel(ctx, channelToken, &NotificationChannelUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(false), Description: dcgraphql.ClearedString(),
	})
	require.NoError(t, err)
	stored := channelRow(t, api, ctx, channelToken)
	for _, row := range []*NotificationChannel{updated, stored} {
		require.False(t, row.Enabled)
		require.False(t, row.Description.Valid)
		require.Equal(t, channelSeed.Name, row.Name.String)
	}
	require.Len(t, channelPartialUpdates(t, statements), 1)
}

func TestChannelPartialEmptyWritesNothing(t *testing.T) {
	api, ctx, statements := channelPartialFixture(t)
	before := channelRow(t, api, ctx, channelToken)
	updated, err := api.UpdateNotificationChannel(ctx, channelToken, &NotificationChannelUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, channelPartialWrites(statements))
	stored := channelRow(t, api, ctx, channelToken)
	require.True(t, before.UpdatedAt.Equal(updated.UpdatedAt))
	require.True(t, before.UpdatedAt.Equal(stored.UpdatedAt))
}

func TestChannelPartialConcurrentInvalidInputWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  NotificationChannelUpdateRequest
	}{
		{"config", NotificationChannelUpdateRequest{Config: dcgraphql.OptionalStringOf(`[]`)}},
		{"credential", NotificationChannelUpdateRequest{
			ChannelType: dcgraphql.OptionalStringOf(ChannelTypeWebhook),
			Config:      dcgraphql.OptionalStringOf(bearerHookConfig), Secret: dcgraphql.ClearedString(),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, statements := channelPartialFixture(t)
			before := channelRow(t, api, ctx, channelToken)
			tc.req.Name = dcgraphql.OptionalStringOf("Refused")
			_, err := api.UpdateNotificationChannel(ctx, channelToken, &tc.req)
			require.Error(t, err)
			require.Empty(t, channelPartialWrites(statements))
			stored := channelRow(t, api, ctx, channelToken)
			require.Equal(t, before.Name, stored.Name)
			require.Equal(t, before.Config, stored.Config)
			require.True(t, before.UpdatedAt.Equal(stored.UpdatedAt))
		})
	}
}
