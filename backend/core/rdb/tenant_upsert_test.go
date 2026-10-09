// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func newUpsertDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(upsertModels()...))
	return db
}

func TestTenantUpsertMatrixSQLite(t *testing.T) {
	runTenantUpsertMatrix(t, newUpsertDB(t))
}

// The negative control: the matrix is only worth something if the probe goes red when the
// guard is gone.
func TestTenantUpsertNegativeControlSQLite(t *testing.T) {
	db := newUpsertDB(t)
	sabotageUpsertGuard(t, db)
	runUpsertNegativeControl(t, db)
}

// dryRunSQL renders the INSERT a create would send, without running it.
func dryRunSQL(t *testing.T, tx *gorm.DB, value any) string {
	t.Helper()
	stmt := tx.Session(&gorm.Session{DryRun: true}).Create(value).Statement
	// SQLite quotes with backticks; the assertions are written with the Postgres spelling.
	return strings.ReplaceAll(stmt.SQL.String(), "`", `"`)
}

func TestTenantUpsertGuardIsInjectedIntoTheDoUpdateArm(t *testing.T) {
	db := newUpsertDB(t)
	onKey := func(cols ...string) clause.OnConflict {
		oc := clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"val"})}
		for _, c := range cols {
			oc.Columns = append(oc.Columns, clause.Column{Name: c})
		}
		return oc
	}

	sql := dryRunSQL(t, db.WithContext(upCtxA).Clauses(onKey("tenant_id", "key")), &upPair{Key: "k", Val: "v"})
	assert.Contains(t, sql, `DO UPDATE SET "val"="excluded"."val" WHERE "up_pairs"."tenant_id" = "excluded"."tenant_id"`)

	// The column comes from the model's own schema, so the second spelling is guarded too.
	sql = dryRunSQL(t, db.WithContext(upCtxA).Clauses(onKey("tenant", "key")), &upPlain{Key: "k", Val: "v"})
	assert.Contains(t, sql, `WHERE "up_plains"."tenant" = "excluded"."tenant"`)

	// Save's fallback shape: UpdateAll with no target.
	sql = dryRunSQL(t, db.WithContext(upCtxA).Clauses(clause.OnConflict{UpdateAll: true}), &upWidget{ID: 1, Name: "n"})
	assert.Contains(t, sql, `WHERE "up_widgets"."tenant_id" = "excluded"."tenant_id"`)

	// The caller's own condition survives, ANDed with the guard, and the guard is added once
	// however many times the statement is built.
	own := clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "up_pairs.val <> excluded.val"}}}
	oc := onKey("tenant_id", "key")
	oc.Where = own
	tx := db.WithContext(upCtxA).Clauses(oc)
	sql = dryRunSQL(t, tx, &upPair{Key: "k", Val: "v"})
	assert.Contains(t, sql, `WHERE up_pairs.val <> excluded.val AND "up_pairs"."tenant_id" = "excluded"."tenant_id"`)
	sql = dryRunSQL(t, tx, &upPair{Key: "k", Val: "v"})
	assert.Equal(t, 1, strings.Count(sql, `"excluded"."tenant_id"`))
	assert.Len(t, own.Exprs, 1)
}

func TestTenantUpsertLeavesOtherStatementsAlone(t *testing.T) {
	db := newUpsertDB(t)
	// DO NOTHING cannot write, and an untenanted model is not ours to guard.
	sql := dryRunSQL(t, db.WithContext(upCtxA).Clauses(clause.OnConflict{DoNothing: true}), &upPair{Key: "k"})
	assert.NotContains(t, sql, "excluded")
	sql = dryRunSQL(t, db.WithContext(upCtxA).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"val"}),
	}), &upFree{Key: "k"})
	assert.NotContains(t, sql, "tenant")
}

// The guard names the column through the one classification the rest of the package uses.
func TestTenantUpsertGuardColumnIsATenantColumn(t *testing.T) {
	db := newUpsertDB(t)
	for _, model := range []any{&upWidget{}, &upPair{}, &upPlain{}} {
		stmt := &gorm.Statement{DB: db}
		require.NoError(t, stmt.Parse(model))
		var col string
		for _, name := range tenantFieldNames {
			if f, ok := stmt.Schema.FieldsByName[name]; ok {
				col = f.DBName
			}
		}
		assert.Contains(t, TenantColumnNames, col)
	}
}

func TestTargetNamesTenant(t *testing.T) {
	cols := func(names ...string) []clause.Column {
		var out []clause.Column
		for _, n := range names {
			out = append(out, clause.Column{Name: n})
		}
		return out
	}
	assert.True(t, targetNamesTenant(clause.OnConflict{Columns: cols("a", "Tenant_ID")}, "tenant_id"))
	assert.True(t, targetNamesTenant(clause.OnConflict{}, "tenant_id"), "an empty target is the primary key")
	assert.False(t, targetNamesTenant(clause.OnConflict{Columns: cols("rule_id")}, "tenant"))
	assert.False(t, targetNamesTenant(clause.OnConflict{OnConstraint: "pk", Columns: cols("tenant")}, "tenant"))
}

func TestCreateRowCount(t *testing.T) {
	assert.Equal(t, 1, createRowCount(&upPair{}))
	assert.Equal(t, 3, createRowCount(&[]upPair{{}, {}, {}}))
	assert.Equal(t, 2, createRowCount([2]upPair{}))
	assert.Equal(t, 0, createRowCount(nil))
	assert.Equal(t, 0, createRowCount(map[string]any{"a": 1}))
	var nilp *upPair
	assert.Equal(t, 0, createRowCount(nilp))
}
