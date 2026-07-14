// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantBranding adds the per-tenant white-labeling override columns to
// iam_tenants (ADR-038 Phase 2): nullable branding_title / branding_logo /
// branding_logo_max_height / branding_primary / branding_background /
// branding_foreground / branding_accent, where NULL means "inherit" (the operator
// default, then the code default, then the console's built-in look). Additive —
// AutoMigrate only adds the new nullable columns and leaves existing rows (which
// inherit) alone, mirroring NewTenantGovernance / NewTenantOutboundGovernance. The
// Rollback drops just these columns, not the table.
func NewTenantBranding() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260714120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{
				"branding_title", "branding_logo", "branding_logo_max_height",
				"branding_primary", "branding_background", "branding_foreground", "branding_accent",
			} {
				if err := tx.Migrator().DropColumn(&iam.Tenant{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
