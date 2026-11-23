// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testAttributeOrphanSweep(t *testing.T, api *Api, ctx context.Context, dev *Device) {
	t.Helper()
	live, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "live", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	for _, id := range []uint{dev.ID, live.ID} {
		require.NoError(t, api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
			value := "kept"
			_, err := api.applyAttributePatchTx(ctx, tx, "device", id, AttributePatch{{Scope: "SERVER", Key: "orphan", ValueType: "STRING", Value: &value, LastUpdated: time.Now()}})
			return err
		}))
	}
	// An unrelated entity family's row is not a device orphan, even if its ID
	// has no devices row. Both the readiness count and deletion must exclude it.
	nonDevice := EntityAttribute{EntityType: "asset", EntityId: dev.ID + 200000000, Scope: "SERVER", AttrKey: "not-device", ValueType: "STRING", LastUpdated: time.Now()}
	require.NoError(t, api.RDB.DB(ctx).Create(&nonDevice).Error)
	other := core.WithTenant(ctx, "orphan-other-tenant")
	otherRow := EntityAttribute{EntityType: "device", EntityId: dev.ID + 100000000, Scope: "SERVER", AttrKey: "other", ValueType: "STRING", LastUpdated: time.Now()}
	require.NoError(t, api.RDB.DB(other).Create(&otherRow).Error)
	otherHead := AttributeGeneration{DeviceId: otherRow.EntityId, Scope: "SERVER", AttrKey: "other", Generation: 1}
	require.NoError(t, api.RDB.DB(other).Create(&otherHead).Error)
	// The previous-release delete had no generation cascade and could orphan attributes.
	require.NoError(t, api.RDB.DB(ctx).Exec("DELETE FROM devices WHERE id = ?", dev.ID).Error)
	n, err := api.CountOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), n, "one orphaned attribute row and its orphaned generation head")
	beforeAttrs := testutil.ToFloat64(deviceAttributeOrphansRemoved.WithLabelValues("entity_attributes"))
	beforeHeads := testutil.ToFloat64(deviceAttributeOrphansRemoved.WithLabelValues("entity_attribute_generations"))
	require.NoError(t, api.SweepDeviceAttributeOrphans(ctx))
	n, err = api.CountOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, beforeAttrs+1, testutil.ToFloat64(deviceAttributeOrphansRemoved.WithLabelValues("entity_attributes")))
	require.Equal(t, beforeHeads+1, testutil.ToFloat64(deviceAttributeOrphansRemoved.WithLabelValues("entity_attribute_generations")))
	for _, tc := range []struct {
		ctx context.Context
		id  uint
	}{{ctx, live.ID}, {other, otherRow.EntityId}} {
		var count int64
		require.NoError(t, api.RDB.DB(tc.ctx).Unscoped().Model(&EntityAttribute{}).Where("entity_id = ?", tc.id).Count(&count).Error)
		require.Equal(t, int64(1), count)
		_, found, err := attributeGeneration(api.RDB.DB(tc.ctx), tc.id, "SERVER", map[bool]string{true: "other", false: "orphan"}[tc.id == otherRow.EntityId])
		require.NoError(t, err)
		require.True(t, found)
	}
	var nonDeviceCount int64
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(&EntityAttribute{}).Where("id = ?", nonDevice.ID).Count(&nonDeviceCount).Error)
	require.Equal(t, int64(1), nonDeviceCount, "another entity family's attributes must be untouched")
	_, found, err := attributeGeneration(api.RDB.DB(ctx), dev.ID, "SERVER", "orphan")
	require.NoError(t, err)
	require.False(t, found)
}

func TestAttributeOrphanSweepSQLite(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, nil)
	testAttributeOrphanSweep(t, api, ctx, dev)
}

func TestAttributeOrphanSweepBoundsBatches(t *testing.T) {
	api, ctx := configTestApi(t)
	var rows []EntityAttribute
	for i := range 2001 {
		rows = append(rows, EntityAttribute{EntityType: "device", EntityId: uint(i + 1), Scope: "SERVER", AttrKey: "orphan", ValueType: "STRING", LastUpdated: time.Now()})
	}
	require.NoError(t, api.RDB.DB(ctx).CreateInBatches(rows, 100).Error)
	var batches []int64
	require.NoError(t, api.RDB.Database.Callback().Delete().After("gorm:delete").Register("orphan_batch_size", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "EntityAttribute" {
			batches = append(batches, tx.RowsAffected)
		}
	}))
	require.NoError(t, api.SweepDeviceAttributeOrphans(ctx))
	require.GreaterOrEqual(t, len(batches), 3)
	for _, n := range batches {
		require.LessOrEqual(t, n, int64(1000))
	}
	n, err := api.CountOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestAttributeOrphanSweepFindsHeadOnlyTenants(t *testing.T) {
	api, ctx := configTestApi(t)
	row := AttributeGeneration{DeviceId: 999999, Scope: "SERVER", AttrKey: "only-head", Generation: 1}
	require.NoError(t, api.RDB.DB(core.WithTenant(ctx, "head-only")).Create(&row).Error)
	require.NoError(t, api.SweepAllDeviceAttributeOrphans(context.Background()))
	_, found, err := attributeGeneration(api.RDB.DB(core.WithTenant(ctx, "head-only")), row.DeviceId, row.Scope, row.AttrKey)
	require.NoError(t, err)
	require.False(t, found)
}
