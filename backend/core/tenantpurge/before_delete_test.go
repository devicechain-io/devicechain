// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBeforeDeleteRunsInsidePurgeTransaction(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec("CREATE TABLE devices (tenant_id TEXT)").Error)
	require.NoError(t, db.Exec("CREATE TABLE purge_marker (marked INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO devices VALUES ('acme')").Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER require_marker BEFORE DELETE ON devices
WHEN NOT EXISTS (SELECT 1 FROM purge_marker)
BEGIN SELECT RAISE(ABORT, 'unmarked'); END`).Error)
	plan := planOf(t, []column{col("main", "devices", "tenant_id", "text")}, nil)
	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.ErrorContains(t, err, "unmarked")
	plan.BeforeDelete = func(tx *gorm.DB) error {
		_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
		require.True(t, inTx)
		return tx.Exec("INSERT INTO purge_marker VALUES (1)").Error
	}
	res, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Rows)
	// A failed later delete must roll the hook's effects back with the purge.
	require.NoError(t, db.Exec("DELETE FROM purge_marker").Error)
	require.NoError(t, db.Exec("INSERT INTO devices VALUES ('acme')").Error)
	plan.Entries = append(plan.Entries, Entry{Table: Table{Schema: "main", Name: "missing"}, Class: ClassDirect, Column: "tenant_id"})
	_, err = Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.Error(t, err)
	var n int64
	require.NoError(t, db.Raw("SELECT count(*) FROM purge_marker").Scan(&n).Error)
	require.Zero(t, n)
}

func TestAttributeGenerationsClassifyDirect(t *testing.T) {
	plan, err := classify([]column{col("device-management", "entity_attribute_generations", "tenant_id", "character varying")}, nil, nil)
	require.NoError(t, err)
	require.Len(t, plan.Actionable(), 1)
	require.Equal(t, ClassDirect, plan.Actionable()[0].Class)
}
