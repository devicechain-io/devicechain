// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantSchema creates the iam_tenants table (ADR-033): tenants are now
// control-plane DB rows with freeform JSON config, replacing the former
// DeviceChainTenant CRD. Additive.
func NewTenantSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260627210000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"iam_tenants"})
		},
	}
}
