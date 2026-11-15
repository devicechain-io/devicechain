// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAttributeUniqueMigrationRepairsDuplicatesAndReplays(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, db.Exec(`CREATE TABLE entity_attributes (id integer primary key,
 tenant_id text, entity_type text, entity_id integer, scope text, attr_key text, deleted_at datetime)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO entity_attributes VALUES
 (1,'acme','device',7,'SHARED','site',CURRENT_TIMESTAMP),
 (2,'acme','device',7,'SHARED','site',NULL),
 (3,'acme','device',7,'SHARED','site',NULL),
 (4,'acme','device',7,'SHARED','site',NULL),
 (5,'acme','device',7,'SHARED','other',NULL),
 (6,'globex','device',7,'SHARED','site',NULL),
 (7,'acme','device',7,'SERVER','site',NULL)`).Error)
	migration := NewEntityAttributeUniqueKeySchema()
	for range 2 {
		require.NoError(t, migration.Migrate(db))
		var ids []uint
		require.NoError(t, db.Raw(`SELECT id FROM entity_attributes WHERE deleted_at IS NULL ORDER BY id`).Scan(&ids).Error)
		require.Equal(t, []uint{2, 5, 6, 7}, ids)
		var repairs []pendingAttributeRepair
		require.NoError(t, db.Find(&repairs).Error)
		require.Len(t, repairs, 1)
		require.Equal(t, "acme", repairs[0].TenantId)
		require.Equal(t, "site", repairs[0].AttrKey)
	}
	require.Error(t, db.Exec(`INSERT INTO entity_attributes VALUES (8,'acme','device',7,'SHARED','site',NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO entity_attributes VALUES (9,'acme','device',7,'SHARED','site',CURRENT_TIMESTAMP)`).Error)
}
