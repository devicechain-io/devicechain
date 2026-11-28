// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFleetEvidenceCloseIndexIsRegisteredAndReplays(t *testing.T) {
	var migration *gormigrate.Migration
	for i, candidate := range Migrations {
		if candidate.ID == "20270102120200" {
			require.Greater(t, i, 0)
			require.GreaterOrEqual(t, Migrations[i-1].ID, "20270102120100")
			migration = candidate
		}
	}
	require.NotNil(t, migration, "close-check index migration must be appended after ledger scaling")
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, db.Exec(`CREATE TABLE fleet_operation_lines
 (tenant_id TEXT, operation_id BIGINT, posting TEXT, settlement TEXT, code TEXT)`).Error)
	// SQLite accepts this index DDL after removing only the Postgres table
	// qualifier. Execute the migration itself twice, including over existing
	// data; the tagged Postgres test verifies its unmodified DDL and query plan.
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("close_index_sqlite_table", func(tx *gorm.DB) {
		statement := strings.ReplaceAll(tx.Statement.SQL.String(), `ON "device-management".fleet_operation_lines`, "ON fleet_operation_lines")
		tx.Statement.SQL.Reset()
		tx.Statement.SQL.WriteString(statement)
	}))
	require.NoError(t, migration.Migrate(db))
	require.NoError(t, db.Exec(`INSERT INTO fleet_operation_lines VALUES ('acme', 42, 'POSTED', NULL, '')`).Error)
	require.NoError(t, migration.Migrate(db))
	var columns []string
	require.NoError(t, db.Raw(`SELECT name FROM pragma_index_info('ix_fleet_operation_lines_evidence_close') ORDER BY seqno`).Scan(&columns).Error)
	require.Equal(t, []string{"tenant_id", "operation_id"}, columns, "each close probe is scoped to its operation")
	var rows int64
	require.NoError(t, db.Table("fleet_operation_lines").Count(&rows).Error)
	require.Equal(t, int64(1), rows, "replay preserves the journal")
}

// Keep the frozen DDL aligned with the literal WHERE in the production close
// query. The tagged Postgres test exercises both actual callers and their plans.
func TestFleetEvidenceCloseIndexMatchesQuery(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil).Session(&gorm.Session{DryRun: true})
	var statements []string
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("capture_close_index", func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	}))
	require.NoError(t, NewFleetLineEvidenceCloseIndexSchema().Migrate(db))
	require.Len(t, statements, 1)
	const prefix = `CREATE INDEX IF NOT EXISTS ix_fleet_operation_lines_evidence_close ON "device-management".fleet_operation_lines (tenant_id, operation_id) WHERE `
	require.True(t, strings.HasPrefix(statements[0], prefix), "replay-safe index on tenant and operation: %s", statements[0])
	indexPredicate := strings.TrimSuffix(strings.TrimPrefix(statements[0], prefix), ";")

	source, err := parser.ParseFile(token.NewFileSet(), "../model/api_fleet_settlement.go", nil, 0)
	require.NoError(t, err)
	var predicates []string
	for _, decl := range source.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "fleetEvidenceCompleteSQL" {
			continue
		}
		for _, statement := range fn.Body.List {
			ret, ok := statement.(*ast.ReturnStmt)
			if !ok {
				continue
			}
			ast.Inspect(ret, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)
				if _, tail, found := strings.Cut(text, " AND l.posting="); found {
					// The last parenthesis closes NOT EXISTS, outside the predicate.
					predicates = append(predicates, "posting="+strings.TrimSuffix(tail, ")"))
				}
				return true
			})
		}
	}
	require.Len(t, predicates, 1, "find the close check's literal posted-line predicate")
	queryPredicate := strings.ReplaceAll(predicates[0], "l.", "")
	compact := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
	}
	require.Equal(t, compact(queryPredicate), compact(indexPredicate), "partial predicate must match the close check exactly, including the TARGET_ERASED arm")
}
