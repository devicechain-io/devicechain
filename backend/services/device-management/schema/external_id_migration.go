// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewExternalIdSchema adds the optional customer-owned external id to devices
// (ADR-049): a business key (VIN, serial, GS1, asset tag) distinct from the token,
// unique per tenant WHEN present. Additive — AutoMigrate on a frozen partial Device
// snapshot adds the external_id column without touching existing ones, then the
// per-tenant partial unique index (unique only among live rows that carry an id).
func NewExternalIdSchema() *gormigrate.Migration {
	// Frozen snapshot: only the fields this migration needs. The name maps to the
	// existing "devices" table; rdb.ExternalReference supplies external_id and
	// rdb.TenantScoped/gorm.Model supply the columns the index predicate references.
	type Device struct {
		gorm.Model
		rdb.TenantScoped
		rdb.ExternalReference
	}
	return &gormigrate.Migration{
		ID: "20260707120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&Device{}); err != nil {
				return err
			}
			return rdb.CreateTenantExternalIdIndex(tx, &Device{})
		},
		Rollback: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&Device{}, "ExternalId") {
				return tx.Migrator().DropColumn(&Device{}, "ExternalId")
			}
			return nil
		},
	}
}
