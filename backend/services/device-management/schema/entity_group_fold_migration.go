// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// foldEntityGroup is the migration-local shape of the uniform EntityGroup
// (ADR-061) — the member family lives in a column, plus the membership-mode
// discriminator and the (dynamic-only) CEL selector columns. Kept local to the
// migration so it never drifts with the live model.
type foldEntityGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	MemberType     string `gorm:"not null;size:32;index"`
	MembershipMode string `gorm:"not null;size:16"`
	Selector       sql.NullString
	SelectorSchema int `gorm:"not null;default:0"`
}

func (foldEntityGroup) TableName() string { return "entity_groups" }

// NewEntityGroupFoldSchema replaces the four per-family group tables
// (device_groups / asset_groups / area_groups / customer_groups) with a single
// uniform entity_groups table (ADR-061 SD-6). This is a **greenfield decisive
// cutover** (pre-GA): the four tables are dropped outright with no data carried
// over and no compat shim — a fresh deploy runs the create migrations for the old
// tables then this one drops them, and the GA migration squash will collapse the
// create/drop into a baseline that only ever had entity_groups.
//
// A group is now addressed as (entity.TypeGroup="group", token), so a group's
// token is unique per tenant across all member families — hence the standard
// per-tenant partial-unique token index (live rows only), exactly like every other
// token entity.
func NewEntityGroupFoldSchema() *gormigrate.Migration {
	legacyTables := []string{"device_groups", "asset_groups", "area_groups", "customer_groups"}
	// The former per-family group entity.Type tokens, now removed from the registry.
	legacyGroupTypes := []string{"devicegroup", "assetgroup", "areagroup", "customergroup"}
	return &gormigrate.Migration{
		ID: "20260714120000",
		Migrate: func(tx *gorm.DB) error {
			// gormigrate runs with UseTransaction:false, so wrap the create+drop in
			// one explicit transaction — the cutover is all-or-nothing.
			return tx.Transaction(func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&foldEntityGroup{}); err != nil {
					return err
				}
				if err := rdb.CreateTenantTokenIndex(tx, &foldEntityGroup{}); err != nil {
					return err
				}
				// Greenfield discard of any legacy-group polymorphic rows: the old
				// group type tokens are gone from the entity-type registry, so a
				// surviving edge/attribute that references one would make LoadEntity /
				// resolveEntity fail closed with "unknown entity type" and could never
				// be cleaned up (its group row is dropped below). Sweep them in the same
				// transaction. On a fresh deploy these deletes match nothing.
				if err := tx.Exec(
					"DELETE FROM entity_relationships WHERE source_type IN ? OR target_type IN ?",
					legacyGroupTypes, legacyGroupTypes).Error; err != nil {
					return err
				}
				if err := tx.Exec(
					"DELETE FROM entity_attributes WHERE entity_type IN ?",
					legacyGroupTypes).Error; err != nil {
					return err
				}
				for _, table := range legacyTables {
					if tx.Migrator().HasTable(table) {
						if err := tx.Migrator().DropTable(table); err != nil {
							return err
						}
					}
				}
				return nil
			})
		},
		// Roll-forward is one-way (pre-GA greenfield): the four legacy tables' schema
		// lived in the now-superseded create migrations and there is no model to
		// AutoMigrate them back. Rollback just removes this migration's table.
		Rollback: func(tx *gorm.DB) error {
			if tx.Migrator().HasTable("entity_groups") {
				return tx.Migrator().DropTable("entity_groups")
			}
			return nil
		},
	}
}
