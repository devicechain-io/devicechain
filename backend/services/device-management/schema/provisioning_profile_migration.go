// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewProvisioningProfileSchema adds the provisioning_profiles table (ADR-012):
// the per-fleet provision key+secret, strategy, and target device type that drive
// device self-registration. It is a post-initial migration, so it is added here
// rather than folded into the frozen initial schema. The snapshot is defined
// inline so the migration is frozen against future model changes; it must produce
// the same columns as model.ProvisioningProfile.
func NewProvisioningProfileSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625150000",
		Migrate: func(tx *gorm.DB) error {
			// ProvisionKey is globally unique (like TokenReference.Token), so a
			// device's key resolves a single profile; tenant isolation on reads is
			// enforced by the app-layer tenant scope on the embedded TenantScoped.
			type ProvisioningProfile struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				ProvisionKey    string `gorm:"unique;not null;size:256"`
				ProvisionSecret string `gorm:"not null;size:256"`
				Strategy        string `gorm:"not null;size:32"`
				DeviceTypeId    uint   `gorm:"not null;index"`
				CredentialType  string `gorm:"not null;size:32"`
				Enabled         bool   `gorm:"not null"`
				ExpiresAt       sql.NullTime
			}
			if err := tx.AutoMigrate(&ProvisioningProfile{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token (ProvisionKey
			// keeps its own global unique — provisioning keys are cross-tenant).
			return rdb.CreateTenantTokenIndex(tx, &ProvisioningProfile{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("provisioning_profiles")
		},
	}
}
