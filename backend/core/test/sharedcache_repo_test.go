// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"path/filepath"
	"testing"
)

// No file in the repository but the rdbtest helper may spell out a shared-cache SQLite DSN.
//
// 🔴 WHY. A shared-cache in-memory database is found again by its NAME and lives while any
// connection to it is open. Fixtures named theirs after t.Name() and many never closed the
// pool, so running a test twice in one process (`go test -count=2`, the first thing anyone
// reaches for when chasing a flake) reopened the first run's database, rows and all: a
// publish minted version 2 instead of 1, a containment check found doubled members. CI
// runs -count=1, so every one of those was green there. The fix was one helper —
// rdbtest.OpenSQLiteMemory, which puts a per-call nonce in the name and closes the pool
// with the test — and this is what keeps the next fixture from copying an older shape.
//
// It reads files outside this module, so `-count=1` is load-bearing (Go's test cache does
// not track them); CI passes it.
func TestNoFixtureBuildsItsOwnSharedCacheDSN(t *testing.T) {
	root := workspaceRoot(t)
	allowed, refused, visited, err := sharedCacheLiteralsUnder(root, SharedCacheDSNOwners)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	// The known positive: the helper itself writes the one legitimate literal. A scan that
	// stopped recognizing literals would otherwise report this tree clean.
	if len(allowed) == 0 {
		t.Errorf("the scan found no shared-cache literal in %v, which hold the one the helper "+
			"needs; a matcher that recognizes nothing reports clean for the same reason a "+
			"clean tree does", SharedCacheDSNOwners)
	}
	for _, dir := range workspaceModuleDirs(t, root) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about "+
				"that tree it did not look at", root, abs)
		}
	}
	for _, r := range refused {
		t.Errorf("%s writes a shared-cache SQLite DSN. Open the database with "+
			"rdbtest.OpenSQLiteMemory (or rdbtest.SQLiteMemoryDSN + OpenSQLiteDSN for a second "+
			"handle): a name built from the test alone repeats when the test runs twice in one "+
			"process, and the second run inherits the first one's rows", r.Pos)
	}
}
