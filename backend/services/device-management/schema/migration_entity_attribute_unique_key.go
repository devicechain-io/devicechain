// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Frozen queue snapshot. Attribute columns below are likewise literal snapshots;
// neither the baseline nor a live model participates in this migration.
type pendingAttributeRepair struct {
	ID         uint   `gorm:"primaryKey"`
	TenantId   string `gorm:"not null;size:128;uniqueIndex:uix_pending_attribute_repairs_key,priority:1"`
	EntityType string `gorm:"uniqueIndex:uix_pending_attribute_repairs_key,priority:2"`
	EntityId   uint   `gorm:"uniqueIndex:uix_pending_attribute_repairs_key,priority:3"`
	Scope      string `gorm:"uniqueIndex:uix_pending_attribute_repairs_key,priority:4"`
	AttrKey    string `gorm:"uniqueIndex:uix_pending_attribute_repairs_key,priority:5"`
	CreatedAt  time.Time
}

func NewEntityAttributeUniqueKeySchema() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "20261201090000", Migrate: func(db *gorm.DB) error {
		if err := db.AutoMigrate(&pendingAttributeRepair{}); err != nil {
			return err
		}
		return db.Transaction(func(tx *gorm.DB) error {
			attrs, repairs := "entity_attributes", "pending_attribute_repairs"
			if tx.Dialector.Name() == "postgres" {
				attrs = `"device-management".entity_attributes`
				repairs = `"device-management".pending_attribute_repairs`
				// Exclude old-pod inserts until both dedupe and index build commit.
				if err := tx.Exec(`LOCK TABLE ` + attrs + ` IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
					return err
				}
			}
			return deduplicateAttributeKeys(tx, attrs, repairs)
		})
	}, Rollback: func(tx *gorm.DB) error {
		name := "uix_entity_attributes_key"
		if tx.Dialector.Name() == "postgres" {
			name = `"device-management".` + name
		}
		if err := tx.Exec(`DROP INDEX IF EXISTS ` + name).Error; err != nil {
			return err
		}
		return tx.Migrator().DropTable(&pendingAttributeRepair{})
	}}
}

func deduplicateAttributeKeys(tx *gorm.DB, attrs, repairs string) error {
	statements := []string{
		`INSERT INTO ` + repairs + ` (tenant_id, entity_type, entity_id, scope, attr_key, created_at)
   SELECT tenant_id, entity_type, entity_id, scope, attr_key, CURRENT_TIMESTAMP FROM ` + attrs + `
   WHERE deleted_at IS NULL GROUP BY tenant_id, entity_type, entity_id, scope, attr_key
   HAVING count(*) > 1 ON CONFLICT DO NOTHING`,
		`UPDATE ` + attrs + ` SET deleted_at = CURRENT_TIMESTAMP WHERE deleted_at IS NULL AND id NOT IN
   (SELECT min(id) FROM ` + attrs + ` WHERE deleted_at IS NULL
    GROUP BY tenant_id, entity_type, entity_id, scope, attr_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uix_entity_attributes_key ON ` + attrs + `
   (tenant_id, entity_type, entity_id, scope, attr_key) WHERE deleted_at IS NULL`,
	}
	for _, stmt := range statements {
		if err := tx.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}
