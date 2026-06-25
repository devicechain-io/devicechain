// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewEntityAttributeSchema adds the entity_attributes table (ADR-012): per-entity
// current-state key-values in CLIENT/SERVER/SHARED scopes, addressed by uniform
// (entity_type, entity_id) (ADR-013). Like the metric model, this was developed
// on a branch that never merged and is being brought onto main, so it is a
// post-initial migration with an inline snapshot (frozen against future model
// changes; it must produce the same columns as model.EntityAttribute).
func NewEntityAttributeSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625140000",
		Migrate: func(tx *gorm.DB) error {
			// The (entity_type, entity_id, scope, attr_key) composite index is the
			// natural key the app-layer upsert looks up; tenant isolation is enforced
			// by the tenant scope on the embedded TenantScoped.
			type EntityAttribute struct {
				gorm.Model
				rdb.TenantScoped
				EntityType  string         `gorm:"not null;size:32;index:idx_entity_attr_key,priority:1"`
				EntityId    uint           `gorm:"not null;index:idx_entity_attr_key,priority:2"`
				Scope       string         `gorm:"not null;size:16;index:idx_entity_attr_key,priority:3"`
				AttrKey     string         `gorm:"not null;size:256;index:idx_entity_attr_key,priority:4"`
				ValueType   string         `gorm:"not null;size:16"`
				Value       sql.NullString `gorm:"size:65536"`
				LastUpdated time.Time      `gorm:"not null"`
			}
			return tx.AutoMigrate(&EntityAttribute{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("entity_attributes")
		},
	}
}
