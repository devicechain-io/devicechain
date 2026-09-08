// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 🔴 NewSecretStoreSchema IS AN APPLIED MIGRATION, AND THE INDEX IT CREATES IS FROZEN.
// The name and the WHERE predicate below are schema. They are not built here: the
// migration passes a literal name to rdb.CreatePartialUniqueIndex, which supplies the
// predicate — so a change in EITHER file moves what a fresh install builds, while every
// database that already ran this migration keeps the index it was given. Both report a
// clean migration and hold different schemas, which is the failure the "never edit an
// existing migration" rule exists to prevent. hack/migration-diff.sh narrows that but
// does not close it: `verify` fails when this area's chain stops reproducing its golden,
// and the ordinary response to a red verify is to re-snapshot — which for a change of
// this class looks exactly like re-snapshotting after a legitimate append.
//
// This test runs the real migration and reads the index back out of the database, so it
// covers the whole path — the migration's literal name, core's predicate, the column
// list, and the fact that the index was actually created rather than merely attempted.
//
// If this fails, the change under it is a schema change to an applied migration. Append
// a new migration instead of editing this expectation.
func TestSecretsHandleIndexIsFrozen(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var indexes []struct {
		Name string
		Sql  string
	}
	if err := db.Raw(
		"SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'secrets' AND sql IS NOT NULL ORDER BY name",
	).Scan(&indexes).Error; err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}

	const wantName = "uix_secrets_tenant_scope_name"
	var handle string
	var seen []string
	for _, ix := range indexes {
		seen = append(seen, ix.Name)
		if ix.Name == wantName {
			handle = ix.Sql
		}
	}
	if handle == "" {
		t.Fatalf("the migration must create an index named %q; it created %v", wantName, seen)
	}

	// sqlite_master stores the definition the database kept, which drops the statement's
	// IF NOT EXISTS. That clause is the migration's re-runnability rather than its schema,
	// and it is pinned on the statement itself by TestPartialUniqueIndexStatementIsFrozen.
	const want = "CREATE UNIQUE INDEX `uix_secrets_tenant_scope_name` ON `secrets` " +
		"(`tenant_id`, `scope`, `name`) WHERE deleted_at IS NULL"
	if handle != want {
		t.Fatalf("the secrets handle index moved.\n want: %s\n  got: %s", want, handle)
	}
}

// The counterweight: a frozen statement is worth nothing if the index it names stopped
// doing its job. The handle is unique among LIVE rows only, so a soft-deleted handle
// frees for reuse, and the instance-scoped empty tenant sentinel is constrained like any
// other tenant rather than being treated as a distinct NULL.
func TestSecretsHandleIndexEnforcesLiveUniqueness(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	insert := func(tenant string) error {
		return db.Exec(
			"INSERT INTO secrets (created_at, updated_at, tenant_id, scope, name, ciphertext, nonce, wrapped_dek, kek_version, alg) "+
				"VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?, 'tenant', 'smtp', x'00', x'00', x'00', 1, 'aes-gcm')",
			tenant).Error
	}

	for _, tenant := range []string{"A", "B", ""} {
		if err := insert(tenant); err != nil {
			t.Fatalf("first insert for tenant %q must succeed: %v", tenant, err)
		}
	}
	for _, tenant := range []string{"A", ""} {
		if err := insert(tenant); err == nil {
			t.Fatalf("a duplicate live handle under tenant %q must be rejected", tenant)
		} else if !strings.Contains(err.Error(), "UNIQUE") {
			t.Fatalf("expected a uniqueness rejection for tenant %q, got: %v", tenant, err)
		}
	}

	if err := db.Exec("UPDATE secrets SET deleted_at = CURRENT_TIMESTAMP WHERE tenant_id = 'A'").Error; err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	if err := insert("A"); err != nil {
		t.Fatalf("a handle must be reusable after soft-delete, got: %v", err)
	}
}
