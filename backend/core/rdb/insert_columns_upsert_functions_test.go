// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The fragment grammar's functions and keywords mean the same on both databases: the same
// upsert, run against SQLite here and Postgres in the integration test, keeps the larger
// revision by CASE and falls back with COALESCE / NULLIF.
var colUpFunctions = NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"},
	Upsert{
		Set: []Assignment{
			SetTo("rev", "CASE WHEN excluded.rev > t.rev THEN excluded.rev ELSE t.rev END"),
			SetTo("label", "coalesce(nullif(excluded.label, 'skip'), t.label)"),
			SetTo("updated_at", "excluded.updated_at"),
			SetTo("big", "excluded.big"),
			SetTo("flag", "excluded.flag"),
			SetTo("n", "excluded.n"),
			SetTo("mode", "excluded.mode"),
			SetTo("active", "excluded.active"),
			SetTo("anchors", "excluded.anchors"),
		},
		Where: "t.rev IS NOT NULL AND (excluded.rev <> t.rev OR t.label IS DISTINCT FROM excluded.label)",
	}, colUpCols...)

func upsertFunctionsRun(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := tenantCtx("acme")
	_, err := colUpFunctions.Upsert(db.WithContext(ctx), upRows(5, "a", "b"))
	require.NoError(t, err)
	hi, lo := upRow("a", 9), upRow("b", 2)
	hi.Label, lo.Label = "skip", "new"
	n, err := colUpFunctions.Upsert(db.WithContext(ctx), []*colUp{hi, lo})
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	got := map[string]colUp{}
	for _, r := range storedUps(t, db) {
		got[r.Key] = r
	}
	require.EqualValues(t, 9, got["a"].Rev)
	require.Equal(t, "l-a", got["a"].Label, "'skip' falls back to the stored label")
	require.EqualValues(t, 5, got["b"].Rev, "CASE keeps the larger revision")
	require.Equal(t, "new", got["b"].Label)
}

func TestColumnarUpsertFragmentFunctionsOnSQLite(t *testing.T) {
	db, _ := newColUpDB(t, true)
	upsertFunctionsRun(t, db)
}
