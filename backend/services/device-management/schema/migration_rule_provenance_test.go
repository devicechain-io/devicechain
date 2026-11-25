// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func TestRuleProvenanceMigrationNullableReplay(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	// Literal old-pod shapes: the new columns must neither be required nor
	// default to a claimed door/identity when this writer omits them.
	require.NoError(t, db.Exec(`CREATE TABLE detection_rules (id integer PRIMARY KEY, definition text)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE device_profile_versions (id integer PRIMARY KEY, snapshot text)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO detection_rules (id, definition) VALUES (1, '{}')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO device_profile_versions (id, snapshot) VALUES (1, '{}')`).Error)
	migration := NewRuleProvenanceSchema()
	for i := 0; i < 2; i++ {
		require.NoError(t, migration.Migrate(db))
	}
	for table, columns := range map[string][]string{
		"detection_rules":         {"authored_via"},
		"device_profile_versions": {"published_by_id", "authored_via"},
	} {
		var info []struct {
			Name      string
			Notnull   int
			DfltValue *string
		}
		require.NoError(t, db.Raw("PRAGMA table_info("+table+")").Scan(&info).Error)
		for _, column := range columns {
			found := false
			for _, field := range info {
				if field.Name == column {
					found = true
					require.Zero(t, field.Notnull, column)
					require.Nil(t, field.DfltValue, column)
				}
			}
			require.True(t, found, column)
		}
	}
	require.NoError(t, db.Exec(`INSERT INTO detection_rules (id, definition) VALUES (2, '{}')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO device_profile_versions (id, snapshot) VALUES (2, '{}')`).Error)
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM detection_rules WHERE authored_via IS NULL`).Scan(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.Raw(`SELECT count(*) FROM device_profile_versions WHERE authored_via IS NULL AND published_by_id IS NULL`).Scan(&count).Error)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.Exec(`UPDATE detection_rules SET authored_via='["DESCRIBE"]' WHERE id=1`).Error)
	require.NoError(t, migration.Migrate(db))
	var saved string
	require.NoError(t, db.Raw(`SELECT authored_via FROM detection_rules WHERE id=1`).Scan(&saved).Error)
	require.Equal(t, `["DESCRIBE"]`, saved)
}
