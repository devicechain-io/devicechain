// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A refused gateway delete must never enter shared cleanup: creation already holds
// the gateway while it enrolls the child, and an attribute writer can hold the
// family while waiting for the gateway's membership row.
func TestGatewayDeleteChecksBeforeSharedCleanup(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	seedDevice(t, api, ctx, "gateway")
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "child", DeviceTypeToken: "sensor", GatewayToken: strp("gateway")})
	require.NoError(t, err)
	cleanup := errors.New("shared cleanup reached before gateway refusal")
	require.NoError(t, api.RDB.Database.Callback().Delete().Before("gorm:delete").Register("test:refuse-cleanup", func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_attributes" || tx.Statement.Table == "entity_group_memberships" {
			tx.AddError(cleanup)
		}
	}))
	deleted, err := api.DeleteDevice(ctx, "gateway")
	require.ErrorIs(t, err, ErrGatewayHasChildren)
	require.False(t, deleted)
}

// Observe the actual delete requests on reversed insertion order, including two
// scopes. A bulk delete gives the engine freedom to take b before a; each request
// must instead identify one row in the order restore/copy writes keys.
func TestEntityDeleteAcquiresAttributesInKeyOrder(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	id := seedDevice(t, api, ctx, "victim")
	for _, row := range []EntityAttribute{
		{EntityType: "device", EntityId: id, Scope: "SHARED", AttrKey: "b", ValueType: "STRING"},
		{EntityType: "device", EntityId: id, Scope: "SHARED", AttrKey: "a", ValueType: "STRING"},
		{EntityType: "device", EntityId: id, Scope: "CLIENT", AttrKey: "a", ValueType: "STRING"},
	} {
		require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
	}
	var keys []string
	require.NoError(t, api.RDB.Database.Callback().Delete().Before("gorm:delete").Register("test:attribute-order", func(tx *gorm.DB) {
		if tx.Statement.Table != "entity_attributes" {
			return
		}
		row, ok := tx.Statement.Dest.(*EntityAttribute)
		if !ok || row.ID == 0 {
			tx.AddError(errors.New("unordered multi-row attribute delete"))
			return
		}
		keys = append(keys, row.AttrKey+"/"+row.Scope)
	}))
	deleted, err := api.DeleteDevice(ctx, "victim")
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, []string{"a/CLIENT", "a/SHARED", "b/SHARED"}, keys)
	var remaining int64
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(&EntityAttribute{}).Where("entity_id = ?", id).Count(&remaining).Error)
	require.Zero(t, remaining)
}
