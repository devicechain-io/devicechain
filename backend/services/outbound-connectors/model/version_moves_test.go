// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A save that lands in the same clock instant as the read must still move the connector's
// version, or an editor holding the version they read is not refused and overwrites it.
// Every row freezes the clock, so the version can only move because the write moves it.

// frozenAt is an instant with sub-microsecond digits. It is in the past on purpose.
var frozenAt = time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC)

const kafkaConfig = `{"addresses":["k:9092"],"topic":"t"}`

// setClock replaces the clock every later call of api sees. Each api.* call takes a fresh
// copy of the database config, so the clock in effect is the one set before that call.
func setClock(api *Api, now *time.Time) {
	api.RDB.Database.Config.NowFunc = func() time.Time { return *now }
}

func connectorOf(t *testing.T, api *Api, ctx context.Context, token string) (string, *Connector) {
	t.Helper()
	found, err := api.ConnectorsByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return *dcgraphql.FormatTime(found[0].UpdatedAt), found[0]
}

func TestEveryConnectorWriteMovesTheVersion(t *testing.T) {
	describe := func(d string) *ConnectorUpdateRequest {
		return &ConnectorUpdateRequest{Description: dcgraphql.OptionalStringOf(d)}
	}
	rows := []struct {
		name  string
		setup func(t *testing.T, api *Api, ctx context.Context)
		// write returns the token the connector has afterwards.
		write func(t *testing.T, api *Api, ctx context.Context, read string) string
		check func(t *testing.T, c *Connector)
	}{
		{"guarded update", nil, func(t *testing.T, api *Api, ctx context.Context, read string) string {
			_, err := api.UpdateConnector(ctx, "pager", describe("writer"), &read)
			require.NoError(t, err)
			return "pager"
		}, func(t *testing.T, c *Connector) { assert.Equal(t, "writer", c.Description.String) }},
		{"update without a precondition", nil, func(t *testing.T, api *Api, ctx context.Context, _ string) string {
			_, err := api.UpdateConnector(ctx, "pager", describe("writer"), nil)
			require.NoError(t, err)
			return "pager"
		}, func(t *testing.T, c *Connector) { assert.Equal(t, "writer", c.Description.String) }},
		{"rename", nil, func(t *testing.T, api *Api, ctx context.Context, _ string) string {
			_, err := api.RenameConnector(ctx, "pager", "pager-2")
			require.NoError(t, err)
			return "pager-2"
		}, func(t *testing.T, c *Connector) { assert.Equal(t, "pager-2", c.Token) }},
		{"rollback", func(t *testing.T, api *Api, ctx context.Context) {
			_, err := api.PublishConnector(ctx, "pager", nil, nil, "tester", nil)
			require.NoError(t, err)
			_, err = api.UpdateConnector(ctx, "pager", connectorEdit(ConnectorTypeKafka, kafkaConfig), nil)
			require.NoError(t, err)
		}, func(t *testing.T, api *Api, ctx context.Context, _ string) string {
			_, err := api.RollbackConnector(ctx, "pager", 1)
			require.NoError(t, err)
			return "pager"
		}, func(t *testing.T, c *Connector) { assert.Equal(t, "mqtt", c.Type) }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			api := newTestApi(t)
			now := frozenAt
			setClock(api, &now)
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token: "pager", Name: strp("Pager"), Type: string(ConnectorTypeMQTT), Config: mqttConfig})
			require.NoError(t, err)
			if row.setup != nil {
				row.setup(t, api, ctx)
			}
			v0, read := connectorOf(t, api, ctx, "pager")

			token := row.write(t, api, ctx, v0)

			_, stored := connectorOf(t, api, ctx, token)
			assert.True(t, stored.UpdatedAt.After(read.UpdatedAt), "the write did not move the version forward: read %v stored %v", read.UpdatedAt, stored.UpdatedAt)
			row.check(t, stored)

			// The editor who read the version before the write is refused and changes nothing.
			_, err = api.UpdateConnector(ctx, token, describe("late"), &v0)
			assert.ErrorIs(t, err, ErrConflict)
			_, after := connectorOf(t, api, ctx, token)
			assert.NotEqual(t, "late", after.Description.String)
		})
	}
}

// The clock moving on is what is stored: the floor never invents a version when time has moved.
func TestConnectorWriteStoresTheClockWhenItHasMoved(t *testing.T) {
	api := newTestApi(t)
	now := frozenAt
	setClock(api, &now)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "pager", Name: strp("Pager"), Type: string(ConnectorTypeMQTT), Config: mqttConfig})
	require.NoError(t, err)

	now = frozenAt.Add(time.Hour)
	_, err = api.UpdateConnector(ctx, "pager",
		&ConnectorUpdateRequest{Description: dcgraphql.OptionalStringOf("writer")}, nil)
	require.NoError(t, err)
	_, stored := connectorOf(t, api, ctx, "pager")
	assert.True(t, stored.UpdatedAt.Equal(now), "stored %v, want the clock %v", stored.UpdatedAt, now)
}

// A rollback, a rename and a last-write-wins update hand back the version the database
// stored, which is what the console sends as its next precondition. The fixture keeps
// updated_at to the microsecond, as PostgreSQL does, so a response carrying the value that
// was sent is refused by the very next save.
func TestConnectorWritesReturnTheStoredVersion(t *testing.T) {
	rows := map[string]func(t *testing.T, api *Api, ctx context.Context) (string, *Connector){
		"rollback": func(t *testing.T, api *Api, ctx context.Context) (string, *Connector) {
			c, err := api.RollbackConnector(ctx, "pager", 1)
			require.NoError(t, err)
			return "pager", c
		},
		"rename": func(t *testing.T, api *Api, ctx context.Context) (string, *Connector) {
			c, err := api.RenameConnector(ctx, "pager", "pager-2")
			require.NoError(t, err)
			return "pager-2", c
		},
		"update without a precondition": func(t *testing.T, api *Api, ctx context.Context) (string, *Connector) {
			c, err := api.UpdateConnector(ctx, "pager",
				&ConnectorUpdateRequest{Description: dcgraphql.OptionalStringOf("writer")}, nil)
			require.NoError(t, err)
			return "pager", c
		},
	}
	for name, write := range rows {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			now := frozenAt
			setClock(api, &now)
			const hook = "test:store_microseconds"
			require.NoError(t, api.RDB.Database.Callback().Update().After("gorm:update").Register(hook, func(tx *gorm.DB) {
				c, ok := tx.Statement.Model.(*Connector)
				if !ok || tx.Error != nil || tx.Statement.RowsAffected == 0 {
					return
				}
				if err := tx.Session(&gorm.Session{NewDB: true}).
					Exec("UPDATE connectors SET updated_at = ? WHERE id = ?", c.UpdatedAt.Truncate(time.Microsecond), c.ID).Error; err != nil {
					tx.AddError(err)
				}
			}))
			ctx := core.WithTenant(context.Background(), "acme")
			_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token: "pager", Name: strp("Pager"), Type: string(ConnectorTypeMQTT), Config: mqttConfig})
			require.NoError(t, err)
			_, err = api.PublishConnector(ctx, "pager", nil, nil, "tester", nil)
			require.NoError(t, err)
			now = frozenAt.Add(time.Hour) // past the floor, so the clock itself (with its nanoseconds) is sent

			token, got := write(t, api, ctx)

			_, stored := connectorOf(t, api, ctx, token)
			assert.True(t, got.UpdatedAt.Equal(stored.UpdatedAt), "response %v, stored %v", got.UpdatedAt, stored.UpdatedAt)
			_, err = api.UpdateConnector(ctx, token,
				&ConnectorUpdateRequest{Description: dcgraphql.OptionalStringOf("next")}, dcgraphql.FormatTime(got.UpdatedAt))
			assert.NoError(t, err)
		})
	}
}
