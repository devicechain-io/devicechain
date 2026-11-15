// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestAttributeUpsertStoredIdentityAndLiveKey(t *testing.T) {
	api, _, ctx := newAttrEmitTestApi(t)
	set := func(value string) *EntityAttribute {
		row, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "d1",
			Scope: "SERVER", AttrKey: "site", ValueType: "STRING", Value: &value})
		require.NoError(t, err)
		return row
	}
	first := set("north")
	// Make the original timestamp distinguishable even on coarse clocks.
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, api.RDB.DB(ctx).Model(first).Update("created_at", past).Error)
	second := set("south")
	require.Equal(t, first.ID, second.ID)
	require.True(t, past.Equal(second.CreatedAt))
	require.Equal(t, "south", second.Value.String)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityAttribute{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	duplicate := &EntityAttribute{EntityType: "device", EntityId: first.EntityId, Scope: "SERVER", AttrKey: "site"}
	require.Error(t, api.RDB.DB(ctx).Create(duplicate).Error)
	require.Error(t, upsertAttributeOnTx(api.RDB.DB(context.Background()), &EntityAttribute{EntityType: "device"}))
	ok, err := api.DeleteEntityAttribute(ctx, "device", "d1", "SERVER", "site")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, first.ID, set("east").ID)
}

func TestAttributeRepairRecomputesStaleMembershipAndRetries(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	db := api.RDB.DB(ctx)
	require.NoError(t, db.AutoMigrate(&PendingAttributeRepair{}))
	id := seedDevice(t, api, ctx, "d1")
	setSharedClimate(t, api, ctx, "d1", "arid")
	g, v := publishAridGroup(t, api, ctx, "arid")
	require.NoError(t, api.RegisterGroupVersionForScoping(ctx, "arid", v))
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	require.NoError(t, db.Where("device_id = ?", id).Delete(&PendingOwnershipFact{}).Error)
	require.True(t, isMemberOf(t, api, ctx, "device", id, g.ID, v))
	// Model the migration retaining the current value while removing a stale match.
	require.NoError(t, db.Model(&EntityAttribute{}).Where("entity_id = ?", id).Update("value", "humid").Error)
	repair := &PendingAttributeRepair{EntityType: "device", EntityId: id, Scope: "SHARED", AttrKey: "climate"}
	require.NoError(t, db.Create(repair).Error)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("break_repair", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "EntityGroupFacetRef" {
			tx.AddError(gorm.ErrInvalidData)
		}
	}))
	require.Error(t, api.DrainAttributeRepairs(ctx))
	var n int64
	require.NoError(t, db.Model(&PendingAttributeRepair{}).Count(&n).Error)
	require.EqualValues(t, 1, n, "failed recompute must keep durable work")
	require.NoError(t, db.Callback().Query().Remove("break_repair"))
	require.NoError(t, api.DrainAttributeRepairs(ctx))
	require.False(t, isMemberOf(t, api, ctx, "device", id, g.ID, v))
	require.NoError(t, db.Model(&PendingOwnershipFact{}).Where("device_id = ?", id).Count(&n).Error)
	require.EqualValues(t, 1, n, "access-scope repair must announce the changed ownership")
	require.NoError(t, db.Model(&PendingAttributeRepair{}).Count(&n).Error)
	require.Zero(t, n)
	// A deleted owner is harmless and its queued repair is discarded.
	require.NoError(t, db.Create(&PendingAttributeRepair{EntityType: "device", EntityId: 9999, Scope: "SHARED", AttrKey: "climate"}).Error)
	require.NoError(t, api.DrainAttributeRepairs(ctx))
	require.NoError(t, db.Model(&PendingAttributeRepair{}).Count(&n).Error)
	require.Zero(t, n)
}

func TestAttributeRepairReconcilesConfiguration(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "site", ValueType: "STRING"}})
	setConfigAttr(t, api, ctx, AttributeScopeShared, "site", AttributeValueString, "north")
	db := api.RDB.DB(ctx)
	require.NoError(t, db.AutoMigrate(&PendingAttributeRepair{}))
	require.EqualValues(t, 1, countRevisions(t, api, ctx))
	require.NoError(t, db.Model(&EntityAttribute{}).Where("entity_id = ?", dev.ID).Update("value", "south").Error)
	require.NoError(t, db.Create(&PendingAttributeRepair{EntityType: "device", EntityId: dev.ID, Scope: "SHARED", AttrKey: "site"}).Error)
	require.NoError(t, api.DrainAttributeRepairs(ctx))
	require.EqualValues(t, 2, countRevisions(t, api, ctx))
	var revision DeviceConfigurationRevision
	require.NoError(t, db.Order("id DESC").First(&revision).Error)
	require.Contains(t, revision.Document, "south")
}

func TestRestoreAttributeCompetingFirstInsert(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "site", ValueType: "STRING"}})
	db := api.RDB.DB(ctx)
	injected := injectCompetingAttributeInsert(t, db)
	require.NoError(t, api.applyConfigurationDocument(ctx, "dev", dev.ID, `{"site":"north"}`))
	require.True(t, injected())
	var rows []EntityAttribute
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "north", rows[0].Value.String)
}

// Insert another writer's winner immediately before the storage statement. This
// deterministically exercises the conflict arm after any application-side read.
func injectCompetingAttributeInsert(t *testing.T, db *gorm.DB) func() bool {
	t.Helper()
	injected := false
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("competing_attribute_insert", func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*EntityAttribute)
		if !ok || injected {
			return
		}
		injected = true
		// Deterministic race: another first insert became visible after the restore's
		// existence read. Use raw SQL so this callback cannot recursively call itself.
		tx.AddError(tx.Exec(`INSERT INTO entity_attributes
   (tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, created_at, updated_at)
   VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			"acme", row.EntityType, row.EntityId, row.Scope, row.AttrKey, "STRING", "stale").Error)
	}))
	t.Cleanup(func() { db.Callback().Create().Remove("competing_attribute_insert") })
	return func() bool { return injected }
}

func TestSetAttributeCompetingFirstInsert(t *testing.T) {
	api, _, ctx := newAttrEmitTestApi(t)
	injected := injectCompetingAttributeInsert(t, api.RDB.DB(ctx))
	value := "north"
	row, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "d1",
		Scope: "SERVER", AttrKey: "site", ValueType: "STRING", Value: &value})
	require.NoError(t, err)
	require.True(t, injected())
	var rows []EntityAttribute
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "north", rows[0].Value.String)
	require.Equal(t, rows[0].ID, row.ID)
}
