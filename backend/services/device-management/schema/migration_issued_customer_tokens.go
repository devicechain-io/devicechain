// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// issuedCustomerToken is the SNAPSHOT of the issued-customer-token ledger as of this
// migration. It is not model.IssuedCustomerToken and must never be replaced by it, for
// the reason every shape in this package gives (see migration_device_replacements.go).
//
// # What the table is
//
// One row per customer token ever issued in a tenant. CreateCustomer writes it in the
// customer's own transaction and DeleteCustomer never removes it, so a deleted customer's
// token stays taken: customer-scoped records (scope stamps on commands, alarms and update
// assignments, dashboard shares, event anchors) name the customer only as
// "customer:<token>" and outlive it, and a new customer at the same token would inherit
// them. The primary key is the constraint that refuses the second issue.
//
// It holds the token and nothing else — no name, no metadata — so a deleted customer
// leaves behind only the fact that its token was used.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies and erases it
// with no exemption to register; erasing it is what frees a purged tenant's tokens.
type issuedCustomerToken struct {
	TenantId  string `gorm:"primaryKey;not null;size:128"`
	Token     string `gorm:"primaryKey;not null;size:128"`
	CreatedAt time.Time
}

// TableName is NOT declared: a TableName() bypasses this area's TablePrefix (see
// migration_list_order_indexes.go), and the type name already derives the table name
// issued_customer_tokens.

// NewIssuedCustomerTokensSchema creates the ledger and backfills it from the customers
// that exist when it runs.
//
// # The backfill, and why it is not optional here
//
// This is an APPENDED migration, so it runs on databases holding customers. A customer
// created before the ledger existed has no row in it, and without the backfill deleting
// that customer would — but for DeleteCustomer's own write — free its token. DeleteCustomer
// records the token too, so the backfill is not the only thing standing between an old
// customer and a reused token; it is what makes "a token is in the ledger exactly when a
// customer was created at it" true from the first moment, rather than true only for the
// customers created afterwards. Customers deleted BEFORE this migration ran left no trace
// anywhere, and their tokens cannot be recovered — pre-GA, that is accepted.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure.
// AutoMigrate creates the table only when it is absent, and the backfill's ON CONFLICT DO
// NOTHING makes a second pass write nothing. Nothing here depends on a statement before
// it beyond the table existing.
func NewIssuedCustomerTokensSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&issuedCustomerToken{}); err != nil {
				return err
			}
			return backfillIssuedCustomerTokens(tx,
				`"device-management"."issued_customer_tokens"`, `"device-management"."customers"`)
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"issued_customer_tokens"})
		},
	}
}

// backfillIssuedCustomerTokens records every token a customer row holds. The table names
// are parameters only so the statement can be exercised on SQLite, which has no
// "device-management" schema; the migration passes them literally.
//
// `WHERE true` is not decoration: SQLite cannot otherwise tell an INSERT … SELECT's
// ON CONFLICT from a join's ON, and Postgres accepts it unchanged.
func backfillIssuedCustomerTokens(tx *gorm.DB, ledger, customers string) error {
	return tx.Exec(`INSERT INTO ` + ledger + ` (tenant_id, token, created_at) ` +
		`SELECT DISTINCT tenant_id, token, CURRENT_TIMESTAMP FROM ` + customers + ` WHERE true ` +
		`ON CONFLICT (tenant_id, token) DO NOTHING`).Error
}
