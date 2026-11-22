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
	// Cross-tenant by necessity: which tenants have queued repairs. No row is read here;
	// each tenant's rows are read and repaired under core.WithTenant below.
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
	err := api.runAttributeEntityTx(ctx, row.EntityType, "repair", func(tx *gorm.DB) error {
		change, minted, attr, device = membershipChange{}, nil, EntityAttribute{}, nil
		// The device's attribute lock and row come BEFORE the queue row. Tenant purge
		// deletes devices before the repair queue, so a repair that held its queue row
		// while waiting on a purged device would close a cycle with the purge.
		if err := lockAttributeOwnerTx(ctx, tx, row.EntityType, row.EntityId); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Where("id = ?", row.ID).Delete(&PendingAttributeRepair{}).Error
			}
			return err
		}
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
		// Match the owner: advisory, device row, NOWAIT attribute, family, configuration.
		read := tx
		if row.EntityType == "device" && tx.Dialector.Name() == "postgres" {
			read = read.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"})
		} else if row.EntityType != "device" {
			read = read.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err = read.Where(
			"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
			row.EntityType, row.EntityId, row.Scope, row.AttrKey).First(&attr).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return attributeRowError(err)
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
