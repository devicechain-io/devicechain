// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 🔴 THE ROWS. migration-diff sees the ledger's DDL and no rows, so a backfill that recorded
// nothing would pass it with every area green. This runs the backfill statement itself over
// groups an upgraded instance already holds.
func TestTheBackfillRecordsEveryExistingGroupToken(t *testing.T) {
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, db.Exec(`CREATE TABLE entity_groups (id integer primary key, tenant_id text not null,
		token text not null, deleted_at datetime)`).Error)
	require.NoError(t, db.AutoMigrate(&issuedGroupToken{}))
	require.NoError(t, db.Exec(`INSERT INTO entity_groups (tenant_id, token, deleted_at) VALUES
		('acme', 'north', NULL), ('acme', 'south', NULL), ('globex', 'north', NULL),
		('acme', 'gone', CURRENT_TIMESTAMP)`).Error)
	// A token already in the ledger (a replay after a partial first pass) is kept, once.
	require.NoError(t, db.Exec(`INSERT INTO issued_group_tokens (tenant_id, token, created_at)
		VALUES ('acme', 'north', CURRENT_TIMESTAMP)`).Error)

	type row struct{ TenantId, Token string }
	read := func() []row {
		var rows []row
		require.NoError(t, db.Raw(`SELECT tenant_id, token FROM issued_group_tokens
			ORDER BY tenant_id, token`).Scan(&rows).Error)
		return rows
	}
	want := []row{{"acme", "gone"}, {"acme", "north"}, {"acme", "south"}, {"globex", "north"}}

	require.NoError(t, backfillIssuedGroupTokens(db, "issued_group_tokens", "entity_groups"))
	require.Equal(t, want, read(), "every group row's (tenant, token) must be in the ledger")

	require.NoError(t, backfillIssuedGroupTokens(db, "issued_group_tokens", "entity_groups"))
	require.Equal(t, want, read(), "a second pass must write nothing")
}
