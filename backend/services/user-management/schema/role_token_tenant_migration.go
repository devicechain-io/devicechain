// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewRoleTokenPerTenant corrects role token uniqueness from global to per-tenant
// (review finding #7). The initial role schema embedded rdb.TokenReference, whose
// Token carries a *global* unique constraint — but a role token like "admin" is a
// per-tenant identifier, so two tenants seeding their own "admin" role collided.
// This drops the global uniqueness and replaces it with a composite unique index
// on (tenant_id, token). Postgres-specific (the runtime RDB); the IF EXISTS
// guards make the drops tolerant of whichever name GORM gave the original
// constraint/index.
func NewRoleTokenPerTenant() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625180000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE roles DROP CONSTRAINT IF EXISTS uni_roles_token`,
				`DROP INDEX IF EXISTS uni_roles_token`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_roles_tenant_token ON roles (tenant_id, token)`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`DROP INDEX IF EXISTS idx_roles_tenant_token`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uni_roles_token ON roles (token)`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
