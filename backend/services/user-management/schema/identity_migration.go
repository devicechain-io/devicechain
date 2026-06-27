// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewIdentitySchema creates the ADR-033 multi-tenant identity tables: the global
// Identity, per-tenant Membership, and the scoped Role catalog, plus the two
// many-to-many join tables (identity↔system roles, membership↔tenant roles)
// materialized from the associations. These coexist with the legacy
// users/roles/user_roles during the phased cutover, so they carry the `iam_`
// table prefix (see iam.Role/Identity/Membership TableName). Additive — it does
// not touch the legacy tables.
func NewIdentitySchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260627190000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Role{}, &iam.Identity{}, &iam.Membership{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{
				"iam_membership_tenant_roles",
				"iam_identity_system_roles",
				"iam_memberships",
				"iam_identities",
				"iam_roles",
			})
		},
	}
}
