// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 🔴 THE ROWS. migration-diff sees the ledger's DDL and no rows, so a backfill that
// recorded nothing would pass it with every area green. This runs the backfill statement
// itself over customers an upgraded instance already holds.
func TestTheBackfillRecordsEveryExistingCustomerToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE customers (id integer primary key, tenant_id text not null,
		token text not null, deleted_at datetime)`).Error)
	require.NoError(t, db.AutoMigrate(&issuedCustomerToken{}))
	require.NoError(t, db.Exec(`INSERT INTO customers (tenant_id, token, deleted_at) VALUES
		('acme', 'north', NULL), ('acme', 'south', NULL), ('globex', 'north', NULL),
		('acme', 'gone', CURRENT_TIMESTAMP)`).Error)
	// A token already in the ledger (a replay after a partial first pass) is kept, once.
	require.NoError(t, db.Exec(`INSERT INTO issued_customer_tokens (tenant_id, token, created_at)
		VALUES ('acme', 'north', CURRENT_TIMESTAMP)`).Error)

	type row struct{ TenantId, Token string }
	read := func() []row {
		var rows []row
		require.NoError(t, db.Raw(`SELECT tenant_id, token FROM issued_customer_tokens
			ORDER BY tenant_id, token`).Scan(&rows).Error)
		return rows
	}
	want := []row{{"acme", "gone"}, {"acme", "north"}, {"acme", "south"}, {"globex", "north"}}

	require.NoError(t, backfillIssuedCustomerTokens(db, "issued_customer_tokens", "customers"))
	require.Equal(t, want, read(), "every customer row's (tenant, token) must be in the ledger")

	// Re-runnable, as the chain requires.
	require.NoError(t, backfillIssuedCustomerTokens(db, "issued_customer_tokens", "customers"))
	require.Equal(t, want, read(), "a second pass must write nothing")
}
