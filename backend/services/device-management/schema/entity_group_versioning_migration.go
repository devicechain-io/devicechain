// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewEntityGroupVersioningSchema adds dynamic entity-group versioning (ADR-062 S1):
// the entity_group_versions table (immutable frozen snapshots of a dynamic group's
// membership selector) and the entity_groups.active_version pointer (the published
// version a rule-scoped resolve reads the frozen selector from). It is additive and
// carries no data step — pre-GA decisive cutover, fresh bring-up assumed: on a fresh
// install there are no groups to back-fill, and a group version takes effect only
// once explicitly published. The migration-local structs must produce the same
// columns as model.EntityGroupVersion / model.EntityGroup.
//
// It mirrors NewProfileVersioningSchema line-for-line (the DeviceProfile versioning
// pattern this reuses). A static group is never versioned, so active_version stays
// null for it; a dynamic group's live selector remains the mutable draft until
// published.
func NewEntityGroupVersioningSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260714150000",
		Migrate: func(tx *gorm.DB) error {
			// Immutable published version snapshots. The uniqueIndex tags create the
			// per-group monotonic-version constraint (entity_group_id, version).
			type EntityGroupVersion struct {
				gorm.Model
				rdb.TenantScoped
				EntityGroupId  uint           `gorm:"not null;uniqueIndex:uix_entity_group_versions_group_version,priority:1"`
				Version        int32          `gorm:"not null;uniqueIndex:uix_entity_group_versions_group_version,priority:2"`
				Selector       string         `gorm:"not null"`
				MemberType     string         `gorm:"not null;size:32"`
				SelectorSchema int            `gorm:"not null"`
				Label          sql.NullString `gorm:"size:128"`
				Description    sql.NullString `gorm:"size:1024"`
				PublishedBy    string         `gorm:"size:256"`
			}
			if err := tx.AutoMigrate(&EntityGroupVersion{}); err != nil {
				return err
			}

			// Add the active-version pointer to entity_groups. AutoMigrate on a
			// partial snapshot adds the missing column without touching existing ones
			// (gorm never drops columns). The struct name maps to entity_groups.
			type EntityGroup struct {
				gorm.Model
				ActiveVersion sql.NullInt32
			}
			return tx.AutoMigrate(&EntityGroup{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec("ALTER TABLE entity_groups DROP COLUMN IF EXISTS active_version").Error; err != nil {
				return err
			}
			return tx.Migrator().DropTable("entity_group_versions")
		},
	}
}
