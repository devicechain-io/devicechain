// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	v1 "github.com/devicechain-io/dc-device-management/schema/v1"
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Drop all tables from the list.
func dropTables(tx *gorm.DB, tables []string) error {
	for _, table := range tables {
		err := tx.Migrator().DropTable(table)
		if err != nil {
			return err
		}
	}
	return nil
}

// Creates the initial schema migration for this functional area.
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20220101000000",
		Migrate: func(tx *gorm.DB) error {
			// One uniform EntityRelationship edge table + EntityRelationshipType
			// replace the per-family relationship/relationship-type tables (ADR-013).
			models := []any{
				&v1.Device{}, &v1.DeviceType{}, &v1.DeviceGroup{}, &v1.DeviceCredential{},
				&v1.AssetType{}, &v1.Asset{}, &v1.AssetGroup{},
				&v1.CustomerType{}, &v1.Customer{}, &v1.CustomerGroup{},
				&v1.AreaType{}, &v1.Area{}, &v1.AreaGroup{},
				&v1.EntityRelationshipType{}, &v1.EntityRelationship{},
			}
			if err := tx.AutoMigrate(models...); err != nil {
				return err
			}
			// ADR-042 P1: every one of these is a tenant-scoped token entity — give
			// each a per-tenant partial unique index on token (replaces the global
			// UNIQUE that rdb.TokenReference no longer declares).
			for _, m := range models {
				if err := rdb.CreateTenantTokenIndex(tx, m); err != nil {
					return err
				}
			}
			// ADR-014: the (credential_type, credential_id) resolve lookup must be
			// unique among LIVE rows only. A struct-tag unique index would count
			// soft-deleted rows — a rotated credential's slot would stay locked and a
			// tombstone could still be resolved to a device. The partial predicate
			// frees the slot on delete and confines the invariant to resolvable rows.
			// Scoped per-tenant (tenant_id leads): credential resolution always runs
			// under a tenant (the callout parses it from the MQTT username, the event
			// resolver from the pipeline context), so per-tenant uniqueness suffices —
			// and it avoids a cross-tenant credential-id squatting/existence oracle.
			if err := rdb.CreatePartialUniqueIndex(tx, &v1.DeviceCredential{},
				"idx_device_credential_lookup", "tenant_id", "credential_type", "credential_id"); err != nil {
				return err
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{
				"device_credentials",
				"entity_relationships", "entity_relationship_types",
				"devices", "device_types", "device_groups",
				"assets", "asset_types", "asset_groups",
				"customers", "customer_types", "customer_groups",
				"areas", "area_types", "area_groups"})
		},
	}
}
