// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantOutboundGovernance adds the per-tenant OUTBOUND governance override
// columns to iam_tenants (ADR-060 SD-3): nullable outbound_messages_per_second /
// outbound_burst, where NULL means "inherit the platform default". A distinct
// dimension from the ingest overrides added by NewTenantGovernance. Additive —
// AutoMigrate only adds the new nullable columns and leaves existing rows (which
// inherit) alone. The Rollback drops just these columns, not the table.
func NewTenantOutboundGovernance() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260713170000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn(&iam.Tenant{}, "outbound_messages_per_second"); err != nil {
				return err
			}
			return tx.Migrator().DropColumn(&iam.Tenant{}, "outbound_burst")
		},
	}
}
