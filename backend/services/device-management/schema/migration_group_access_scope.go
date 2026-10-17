// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewGroupAccessScopeSchema adds entity_groups.access_scope_at (ADR-081): when the group was
// made an access scope — a group a member's access can be limited to — or NULL while it is
// not one.
//
// # A timestamp, nullable, no default
//
// For the reasons migration_device_disabled.go gives for devices.disabled_at: NULL is what
// every existing group holds after this runs, and has to mean "not an access scope", which a
// boolean could only say through a DEFAULT, and `ADD COLUMN ... DEFAULT <const>` lives in
// catalog state a logical dump/restore loses. No group was an access scope before this
// existed, so there is nothing to backfill.
//
// # Re-runnability
//
// One statement, ADD COLUMN IF NOT EXISTS. No snapshot struct: it touches no rows, and names
// the table literally for the reason migrations.go gives.
func NewGroupAccessScopeSchema() *gormigrate.Migration {
	const groups = `"device-management"."entity_groups"`

	return &gormigrate.Migration{
		ID: "20261016180000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + groups +
				` ADD COLUMN IF NOT EXISTS access_scope_at timestamptz;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + groups +
				` DROP COLUMN IF EXISTS access_scope_at;`).Error
		},
	}
}

// issuedGroupToken is the SNAPSHOT of the issued-group-token ledger as of this migration. It
// is not model.IssuedGroupToken and must never be replaced by it, for the reason every shape
// in this package gives (see migration_device_replacements.go).
//
// One row per entity-group token ever issued in a tenant, the twin of issued_customer_tokens
// (migration_issued_customer_tokens.go) for the same reason: a group a member's access is
// limited to is named "group:<token>" on the member's scope and on records that outlive the
// group, and a new group at the same token would inherit them. CreateEntityGroup writes the
// row in the group's own transaction and DeleteEntityGroup never removes it. It carries
// tenant_id, so the catalog-driven tenant purge erases it with no exemption.
type issuedGroupToken struct {
	TenantId  string `gorm:"primaryKey;not null;size:128"`
	Token     string `gorm:"primaryKey;not null;size:128"`
	CreatedAt time.Time
}

// TableName is NOT declared: a TableName() bypasses this area's TablePrefix (see
// migration_list_order_indexes.go), and the type name already derives issued_group_tokens.

// NewIssuedGroupTokensSchema creates the group-token ledger and backfills it from the groups
// that exist when it runs, so "a token is in the ledger exactly when a group was created at
// it" holds from the first moment. Groups deleted before it ran left no trace; pre-GA, that
// is accepted, as it was for customers.
//
// Re-runnable: AutoMigrate creates the table only when it is absent, and the backfill's ON
// CONFLICT DO NOTHING makes a second pass write nothing.
func NewIssuedGroupTokensSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016180100",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&issuedGroupToken{}); err != nil {
				return err
			}
			return backfillIssuedGroupTokens(tx,
				`"device-management"."issued_group_tokens"`, `"device-management"."entity_groups"`)
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"issued_group_tokens"})
		},
	}
}

// backfillIssuedGroupTokens records every token a group row holds. The table names are
// parameters only so the statement can be exercised on SQLite; the migration passes them
// literally. `WHERE true` is there for the reason backfillIssuedCustomerTokens gives.
func backfillIssuedGroupTokens(tx *gorm.DB, ledger, groups string) error {
	return tx.Exec(`INSERT INTO ` + ledger + ` (tenant_id, token, created_at) ` +
		`SELECT DISTINCT tenant_id, token, CURRENT_TIMESTAMP FROM ` + groups + ` WHERE true ` +
		`ON CONFLICT (tenant_id, token) DO NOTHING`).Error
}
