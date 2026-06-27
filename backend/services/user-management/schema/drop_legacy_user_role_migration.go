// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDropLegacyUserRole drops the legacy tenant-bound auth tables — users, roles,
// and the user_roles join — superseded by the global iam model (ADR-033). The
// auth path moved to iam.Identity / iam.Membership / iam.Role and the legacy
// GraphQL CRUD was removed, leaving these tables dead; this reclaims them. On a
// fresh install the earlier migrations no longer create them, so the IF EXISTS
// drops are a no-op; on an upgraded instance they remove the orphaned tables.
// Postgres-specific (the runtime RDB), matching the other raw-SQL migrations.
func NewDropLegacyUserRole() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260628000000",
		Migrate: func(tx *gorm.DB) error {
			// Join table first, then the two it referenced — DropTable is
			// IF-EXISTS-safe, so this no-ops on a fresh install.
			return dropTables(tx, []string{"user_roles", "roles", "users"})
		},
		// Irreversible by design: the legacy shapes are gone from the codebase
		// (pre-GA decisive cutover), so there is nothing to recreate on rollback.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
