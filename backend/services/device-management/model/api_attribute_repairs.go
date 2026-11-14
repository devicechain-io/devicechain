// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PendingAttributeRepair survives a restart between migration dedupe and the
// membership/configuration repair. It is removed only with a successful repair.
type PendingAttributeRepair struct {
	ID         uint `gorm:"primaryKey"`
	TenantId   string
	EntityType string
	EntityId   uint
	Scope      string
	AttrKey    string
	CreatedAt  time.Time
}

func (api *Api) DrainAttributeRepairs(ctx context.Context) error {
	var tenants []string
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingAttributeRepair{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return err
	}
	var failures []error
	for _, tenant := range tenants {
		tenantCtx := core.WithTenant(ctx, tenant)
		var rows []PendingAttributeRepair
		if err := api.RDB.DB(tenantCtx).Order("id").Limit(rdb.MaxLookupKeys).Find(&rows).Error; err != nil {
			failures = append(failures, err)
			continue
		}
		for _, row := range rows {
			if err := api.repairAttribute(tenantCtx, row); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (api *Api) repairAttribute(ctx context.Context, row PendingAttributeRepair) error {
	var change membershipChange
	var minted *DeviceConfigurationRevision
	var attr EntityAttribute
	var device *Device
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize drainers; another replica may already have completed this row.
		var pending PendingAttributeRepair
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&pending, row.ID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		local := NewApi(&rdb.RdbManager{Database: tx})
		owner, err := local.LoadEntity(ctx, row.EntityType, row.EntityId)
		if err != nil {
			return err
		}
		if owner == nil {
			return tx.Delete(&pending).Error
		}
		device, _ = owner.(*Device)
		// Match the writer's lock order: attribute, family, configuration.
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
			row.EntityType, row.EntityId, row.Scope, row.AttrKey).First(&attr).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if membershipScopeEligible(row.EntityType, row.Scope) {
			change, err = api.recomputeMembershipForAttr(ctx, tx, row.EntityType, row.EntityId, row.AttrKey)
			if err != nil {
				return err
			}
		}
		minted, err = api.reconcileDeviceConfigurationOnTx(ctx, tx, row.EntityType, row.EntityId, row.Scope, row.AttrKey, false)
		if err != nil {
			return err
		}
		if err := queueAccessChangeOnTx(tx, row.EntityType, row.EntityId, change); err != nil {
			return err
		}
		return tx.Delete(&pending).Error
	})
	if err != nil {
		return err
	}
	api.publishMintedConfiguration(ctx, row.EntityId, minted)
	api.afterMembershipRecompute(ctx, row.EntityType, row.EntityId, change)
	if device != nil && attr.ID != 0 {
		var value *string
		if attr.Value.Valid {
			value = &attr.Value.String
		}
		api.emitDeviceAttributeForSet(ctx, &EntityAttributeSetRequest{EntityType: row.EntityType,
			Entity: device.Token, Scope: row.Scope, AttrKey: row.AttrKey, ValueType: attr.ValueType, Value: value}, attr.LastUpdated)
	}
	return nil
}
