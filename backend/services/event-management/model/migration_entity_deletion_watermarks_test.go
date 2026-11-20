// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEntityDeletionWatermarkMigrationSnapshotAndReplay(t *testing.T) {
	assert.Equal(t, []string{
		`CREATE TABLE IF NOT EXISTS "event-management".entity_deletion_watermarks (
		tenant_id varchar(128) NOT NULL,
		entity_type varchar(64) NOT NULL,
		entity_token varchar(128) NOT NULL,
		deleted_time timestamptz NOT NULL,
		resweep_after timestamptz NULL,
		armed_at timestamptz NULL,
		PRIMARY KEY (tenant_id, entity_type, entity_token)
	)`,
		`CREATE INDEX IF NOT EXISTS idx_entity_deletion_watermarks_resweep ON "event-management".entity_deletion_watermarks
		(resweep_after) WHERE resweep_after IS NOT NULL`,
	}, entityDeletionWatermarksDDL, "the migration owns its literal snapshot")
	m := NewEntityDeletionWatermarksSchema()
	assert.Equal(t, "20261119000000", m.ID)
	assert.Equal(t, migrationPosition(t, NewTenantUsageIngestSchema().ID)+1, migrationPosition(t, m.ID))
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS "event-management"`).Error)
	// SQLite qualifies an index's name rather than its ON table. Translate that
	// syntax only; run the migration itself twice, preserving its IF NOT EXISTS.
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:index_schema", func(tx *gorm.DB) {
		s := tx.Statement.SQL.String()
		s = strings.ReplaceAll(s, `idx_entity_deletion_watermarks_resweep ON "event-management".entity_deletion_watermarks`,
			`"event-management".idx_entity_deletion_watermarks_resweep ON entity_deletion_watermarks`)
		tx.Statement.SQL.Reset()
		tx.Statement.SQL.WriteString(s)
	}))
	require.NoError(t, m.Migrate(db))
	require.NoError(t, db.Exec(`INSERT INTO "event-management".entity_deletion_watermarks
		(tenant_id, entity_type, entity_token, deleted_time) VALUES ('acme', 'area', 'floor', '2026-11-10')`).Error)
	require.NoError(t, m.Migrate(db), "replaying from the top must succeed")
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM "event-management".entity_deletion_watermarks`).Scan(&n).Error)
	assert.Equal(t, int64(1), n, "replay preserves watermark rows")
}
