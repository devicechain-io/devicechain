// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func TestAttributeGenerationsMigrationReplay(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	migration := NewAttributeGenerationsSchema()
	require.NoError(t, migration.Migrate(db))
	require.True(t, db.Migrator().HasTable("entity_attribute_generations"))
	for _, column := range []string{"id", "tenant_id", "device_id", "scope", "attr_key", "generation", "updated_at"} {
		require.True(t, db.Migrator().HasColumn(&attributeGenerationSnapshot{}, column), column)
	}
	require.NoError(t, db.Exec("INSERT INTO entity_attribute_generations (tenant_id, device_id, scope, attr_key, generation) VALUES ('acme', 1, 'SERVER', 'k', 7)").Error)
	require.NoError(t, db.Exec("INSERT INTO entity_attribute_generations (tenant_id, device_id, scope, attr_key, generation) VALUES ('acme', 1, 'SERVER', 'other', 8), ('acme', 1, 'CLIENT', 'k', 9), ('other', 1, 'SERVER', 'k', 10), ('acme', 2, 'SERVER', 'k', 11)").Error)
	require.NoError(t, migration.Migrate(db))
	var head int64
	require.NoError(t, db.Raw("SELECT generation FROM entity_attribute_generations WHERE tenant_id='acme' AND device_id=1 AND scope='SERVER' AND attr_key='k'").Scan(&head).Error)
	require.Equal(t, int64(7), head)
	// The old pod must still be able to delete the parent device.
	var foreignKeys []struct{ Table string }
	require.NoError(t, db.Raw("PRAGMA foreign_key_list(entity_attribute_generations)").Scan(&foreignKeys).Error)
	require.Empty(t, foreignKeys)
	require.True(t, db.Migrator().HasIndex(&attributeGenerationSnapshot{}, "uix_attribute_generation_key"))
	require.Error(t, db.Exec("INSERT INTO entity_attribute_generations (tenant_id, device_id, scope, attr_key, generation) VALUES ('acme', 1, 'SERVER', 'k', 8)").Error)
}
