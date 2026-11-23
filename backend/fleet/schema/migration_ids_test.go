// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// TestMigrationIDsAreAcceptedByGormigrate runs the same ID check the service runs before
// gormigrate.New. A duplicate ID makes gormigrate refuse the whole chain at startup, and a
// DDL test calls each migration directly, so without this nothing short of a real Postgres
// would see it.
func TestMigrationIDsAreAcceptedByGormigrate(t *testing.T) {
	require.NoError(t, rdb.CheckMigrationIDs(Migrations))
}

// TestWriterExclusionIDIsFrozen pins the ID. A shipped migration's ID is how every existing
// database knows it has already applied it: changing it re-runs the migration on all of them.
func TestWriterExclusionIDIsFrozen(t *testing.T) {
	require.Equal(t, "20261121000100", WriterExclusionID)
	require.Equal(t, WriterExclusionID, Migrations[0].ID)
}

// TestMigrationsImportNoLiveModels asserts the rule that a migration declares its OWN structs:
// no file of this package may import the module's live model package (or any package named
// model), because a migration that points at a live model is silently rewritten whenever that
// model changes, which breaks fresh installs while every existing database looks healthy.
func TestMigrationsImportNoLiveModels(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	require.NoError(t, err)
	var checked int
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			checked++
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				require.NotContains(t, path, "dc-fleet/model", "%s imports a live model", name)
				require.False(t, strings.HasSuffix(path, "/model"), "%s imports %s", name, path)
			}
		}
	}
	require.NotZero(t, checked, "the scan examined no files")
}

// TestNoFleetTableIsTenantScoped asserts the declared exclusion: none of the migration's table
// definitions carries a tenant_id column. Fleet is a different instance type, and a fleet table
// that later becomes tenant-scoped must fail here until someone decides where it belongs.
func TestNoFleetTableIsTenantScoped(t *testing.T) {
	for _, stmt := range writerExclusionStatements {
		require.NotContains(t, stmt, "tenant_id")
	}
	for _, c := range writerExclusionChecks() {
		require.NotContains(t, c.expr, "tenant_id")
	}
}
