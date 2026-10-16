// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// roleTenantSnapshot is this migration's own snapshot of iam_roles: the primary key and
// the one column it adds. A SNAPSHOT, never iam.Role, with its TableName pinned for the
// reason every snapshot here pins one.
type roleTenantSnapshot struct {
	ID uint `gorm:"primarykey"`

	// The owning tenant's TOKEN (as iam_memberships.tenant_id: a plain reference, not a
	// foreign key — the purge releases tenant rows while their tokens are reclaimed, and
	// sweeps this table by this column). NULL is the operator's catalogue, which is every
	// role that existed before this column did.
	TenantID *string `gorm:"size:128"`
}

func (roleTenantSnapshot) TableName() string { return "iam_roles" }

// roleTenantIndexes replace the single (scope, token) unique index with two partial ones.
//
// 🔴 WHY TWO, AND NOT ONE ON (scope, tenant_id, token). A unique index treats every NULL as
// distinct from every other, so one index over the three columns would let the CATALOGUE —
// whose tenant_id is NULL — hold "viewer" twice. An expression index that folds a NULL
// tenant_id to the empty string would fix that, but the live model cannot declare an expression index for the unit
// tests' AutoMigrate to build, and a test schema that differs from the real one in exactly
// the uniqueness rule is how a duplicate gets through. Two partial indexes are expressible
// in both places and say the rule directly: tokens are unique among the catalogue's roles,
// and unique among each tenant's own.
//
// The old index is dropped first. It is strictly stronger than the catalogue one (it
// covers every row, and before this migration every row is a catalogue row), so the order
// opens no window in which a duplicate could be written that the new index would refuse.
//
// Each statement is guarded (IF EXISTS / IF NOT EXISTS) so a replay from the top is a
// no-op, as the chain requires.
var roleTenantIndexes = []string{
	`DROP INDEX IF EXISTS idx_iam_role_scope_token`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_iam_roles_catalogue_token ON iam_roles (scope, token) WHERE tenant_id IS NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_iam_roles_owned_token ON iam_roles (scope, tenant_id, token) WHERE tenant_id IS NOT NULL`,
}

// NewRoleTenantMigration lets a role be owned by one tenant (ADR-081): iam_roles gains a
// nullable tenant_id, and token uniqueness moves from (scope, token) over every row to
// (scope, token) among catalogue roles plus (scope, tenant, token) among each tenant's own.
func NewRoleTenantMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016120100",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&roleTenantSnapshot{}); err != nil {
				return err
			}
			for _, stmt := range roleTenantIndexes {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`DROP INDEX IF EXISTS idx_iam_roles_owned_token`,
				`DROP INDEX IF EXISTS idx_iam_roles_catalogue_token`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_iam_role_scope_token ON iam_roles (scope, token)`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return tx.Migrator().DropColumn(&roleTenantSnapshot{}, "tenant_id")
		},
	}
}
