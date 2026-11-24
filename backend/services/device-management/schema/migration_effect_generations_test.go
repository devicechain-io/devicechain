// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func TestEffectGenerationsMigrationReplayAndOldWriters(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	// Literal old-pod shapes: neither insert names a generation column.
	require.NoError(t, db.Exec("CREATE TABLE devices (id integer PRIMARY KEY, disabled_at datetime)").Error)
	require.NoError(t, db.Exec("CREATE TABLE entity_relationships (id integer PRIMARY KEY, token text)").Error)
	migration := NewEffectGenerationsSchema()
	registered := 0
	for _, candidate := range Migrations {
		if candidate.ID == migration.ID {
			registered++
		}
	}
	require.Equal(t, 1, registered, "the service must execute this migration")
	for i := 0; i < 2; i++ {
		require.NoError(t, migration.Migrate(db))
		require.NoError(t, db.Exec("INSERT INTO devices (id) VALUES (?)", i+1).Error)
		require.NoError(t, db.Exec("INSERT INTO entity_relationships (id, token) VALUES (?, 'old')", i+1).Error)
	}
	require.NoError(t, db.Exec("UPDATE devices SET state_generation = 7 WHERE id = 1").Error)
	require.NoError(t, db.Exec("UPDATE entity_relationships SET membership_generation = 9 WHERE id = 1").Error)
	require.NoError(t, db.Exec("INSERT INTO static_membership_generations (tenant_id, group_id, member_type, member_id, generation) VALUES ('acme', 1, 'device', 2, 11)").Error)
	require.NoError(t, migration.Migrate(db))
	var generation int64
	require.NoError(t, db.Raw("SELECT state_generation FROM devices WHERE id = 1").Scan(&generation).Error)
	require.EqualValues(t, 7, generation)
	require.NoError(t, db.Raw("SELECT membership_generation FROM entity_relationships WHERE id = 1").Scan(&generation).Error)
	require.EqualValues(t, 9, generation)
	require.NoError(t, db.Raw("SELECT generation FROM static_membership_generations").Scan(&generation).Error)
	require.EqualValues(t, 11, generation)
	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM devices WHERE state_generation IS NULL").Scan(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Raw("SELECT count(*) FROM entity_relationships WHERE membership_generation IS NULL").Scan(&count).Error)
	require.EqualValues(t, 1, count)
	var foreignKeys []struct{ Table string }
	require.NoError(t, db.Raw("PRAGMA foreign_key_list(static_membership_generations)").Scan(&foreignKeys).Error)
	require.Empty(t, foreignKeys, "old-pod deletes must still work")
	require.Error(t, db.Exec("INSERT INTO static_membership_generations (tenant_id, group_id, member_type, member_id, generation) VALUES ('acme', 1, 'device', 2, 12)").Error)
}
