// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantGovernance adds the per-tenant governance override columns to
// iam_tenants (ADR-023): nullable ingest_messages_per_second / ingest_burst,
// where NULL means "inherit the platform default". Additive — AutoMigrate only
// adds the new nullable columns and leaves existing rows (which inherit) alone.
// The Rollback drops just these columns, not the table.
func NewTenantGovernance() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260707170000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn(&iam.Tenant{}, "ingest_messages_per_second"); err != nil {
				return err
			}
			return tx.Migrator().DropColumn(&iam.Tenant{}, "ingest_burst")
		},
	}
}
