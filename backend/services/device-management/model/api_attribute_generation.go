// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AttributeGeneration survives unset, including an unset of an absent key.
// Previous-release pods do not bump these heads. A head is evidence only when
// read in a transaction that passed the future activation check.
// The surrogate ID follows PendingAttributeRepair; the tenant-bearing unique
// key also follows the area's attribute upsert convention. There is no device FK:
// previous-release device deletes must remain possible during a rolling upgrade.
type AttributeGeneration struct {
	ID         uint   `gorm:"primaryKey"`
	TenantId   string `gorm:"not null;size:128;uniqueIndex:uix_attribute_generation_key,priority:1"`
	DeviceId   uint   `gorm:"type:bigint;not null;uniqueIndex:uix_attribute_generation_key,priority:2"`
	Scope      string `gorm:"not null;size:32;uniqueIndex:uix_attribute_generation_key,priority:3"`
	AttrKey    string `gorm:"not null;size:256;uniqueIndex:uix_attribute_generation_key,priority:4"`
	Generation int64  `gorm:"not null"`
	UpdatedAt  time.Time
}

// TableName sorts the table beside entity_attributes and after devices, so tenant purge (which
// orders tables with no foreign key alphabetically) deletes it after a tenant's devices, in the
// same order a device delete takes.
func (AttributeGeneration) TableName() string { return "entity_attribute_generations" }

func attributeGeneration(tx *gorm.DB, deviceID uint, scope, key string) (int64, bool, error) {
	var head AttributeGeneration
	err := tx.Where("device_id = ? AND scope = ? AND attr_key = ?", deviceID, scope, key).First(&head).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	return head.Generation, err == nil, err
}

func bumpAttributeGenerationTx(tx *gorm.DB, deviceID uint, scope, key string) (int64, error) {
	var next int64
	updates := map[string]any{"updated_at": time.Now()}
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Raw(`SELECT nextval('"device-management".entity_attribute_generation_seq')`).Scan(&next).Error; err != nil {
			return 0, err
		}
		updates["generation"] = gorm.Expr(`nextval('"device-management".entity_attribute_generation_seq')`)
	} else {
		// SQLite tests are single-writer; allocate inside the owning transaction. The tenant's own
		// maximum is enough there: a head is compared only with heads of the same device.
		if err := tx.Model(&AttributeGeneration{}).
			Select("COALESCE(MAX(generation), 0) + 1").Scan(&next).Error; err != nil {
			return 0, err
		}
		updates["generation"] = next
	}
	head := AttributeGeneration{DeviceId: deviceID, Scope: scope, AttrKey: key, Generation: next}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_id"}, {Name: "scope"}, {Name: "attr_key"}},
		DoUpdates: clause.Assignments(updates),
	}, clause.Returning{}).Create(&head).Error
	return head.Generation, err
}
