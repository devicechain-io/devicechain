// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/model"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewRoleSchema adds the roles table and the user_roles join table (ADR-008
// RBAC): a tenant-scoped, named bundle of granted authorities, and the
// many-to-many association from users to roles. AutoMigrating User alongside Role
// materializes the user_roles join table from the User.Roles association.
func NewRoleSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625170000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Role{}, &model.User{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"user_roles", "roles"})
		},
	}
}
