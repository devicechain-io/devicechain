// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// sqlRecorder is a gorm logger that keeps the SQL of every statement gorm executes.
// That is the only way to read back the text these helpers hand to Exec: the *gorm.DB
// carrying it is discarded by `return tx.Exec(sql).Error`.
type sqlRecorder struct {
	logger.Interface
	statements []string
}

func (r *sqlRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.statements = append(r.statements, sql)
}

// only returns the single statement the recorder captured, failing if the helper under
// test issued anything other than exactly one.
func (r *sqlRecorder) only(t *testing.T) string {
	t.Helper()
	if len(r.statements) != 1 {
		t.Fatalf("expected exactly one statement, got %d: %q", len(r.statements), r.statements)
	}
	return r.statements[0]
}

func newRecordingDB(t *testing.T, models ...any) (*gorm.DB, *sqlRecorder) {
	t.Helper()
	rec := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, m := range models {
		if err := db.AutoMigrate(m); err != nil {
			t.Fatalf("migrate %T: %v", m, err)
		}
	}
	rec.statements = nil // drop the AutoMigrate chatter
	return db, rec
}

// secretsShape mirrors the snapshot struct NewSecretStoreSchema declares inline: a
// soft-deletable, tenant-scoped row whose (tenant_id, scope, name) handle is unique
// among live rows. It is a stand-in only for the column set; the end-to-end pin on the
// real migration lives in core/secrets.
type secretsShape struct {
	gorm.Model
	TenantScoped
	Scope string
	Name  string
}

// 🔴 The statement CreatePartialUniqueIndex emits is FROZEN, because a migration has
// already applied it: NewSecretStoreSchema creates the secrets handle index through this
// helper. The index NAME and the WHERE predicate are schema. If either moves, a fresh
// install builds a different index from the one every existing database already holds,
// and both report a clean migration — the silent divergence the "never edit an existing
// migration" rule exists to prevent.
//
// A change that makes this assertion fail is a schema change to an applied migration. It
// is not a stale test. Widening the predicate — dropping the soft-delete clause, or
// adding a second one — or altering how the caller's name reaches the SQL means the
// migration has to be appended to instead.
func TestPartialUniqueIndexStatementIsFrozen(t *testing.T) {
	db, rec := newRecordingDB(t, &secretsShape{})

	if err := CreatePartialUniqueIndex(db, &secretsShape{}, "uix_secrets_shapes_handle", "tenant_id", "scope", "name"); err != nil {
		t.Fatalf("create index: %v", err)
	}

	const want = "CREATE UNIQUE INDEX IF NOT EXISTS `uix_secrets_shapes_handle` ON `secrets_shapes` " +
		"(`tenant_id`, `scope`, `name`) WHERE deleted_at IS NULL"
	if got := rec.only(t); got != want {
		t.Fatalf("emitted statement moved.\n want: %s\n  got: %s", want, got)
	}
}

// The soft-delete predicate is the whole reason this helper exists, so it gets an
// assertion of its own: the full-string comparison above also fails when nothing but the
// dialect's quoting changed, and its message would not say which half moved.
func TestPartialUniqueIndexPredicateIsSoftDeleteOnly(t *testing.T) {
	db, rec := newRecordingDB(t, &secretsShape{})

	if err := CreatePartialUniqueIndex(db, &secretsShape{}, "uix_secrets_shapes_handle", "tenant_id"); err != nil {
		t.Fatalf("create index: %v", err)
	}
	sql := rec.only(t)
	const marker = ") WHERE "
	i := strings.Index(sql, marker)
	if i < 0 {
		t.Fatalf("statement carries no WHERE predicate at all: %s", sql)
	}
	if where := sql[i+len(marker):]; where != "deleted_at IS NULL" {
		t.Fatalf("partial-index predicate moved: want %q, got %q (full statement: %s)",
			"deleted_at IS NULL", where, sql)
	}
}

// prefixedThing stands in for a service model under the production wiring, where the
// NamingStrategy carries a "<functional-area>." TablePrefix.
type prefixedThing struct {
	gorm.Model
	TenantScoped
	TokenReference
	ExternalReference
	Name string
}

// These helpers derive their index name from stmt.Table, which gorm hands back
// UNQUALIFIED even when the NamingStrategy prefixes the schema — the qualified form
// lands in stmt.TableExpr instead. Nothing else here would notice a gorm release
// changing that: the fixtures only ever run on SQLite, where IsUniqueViolation matches
// on columns rather than on the index name, so a fixture index that started carrying
// the prefix would be invisible to every test that installs one.
//
// Where it would surface is CreatePartialUniqueIndex's ON clause, which quotes
// stmt.Table: a prefix left in place there collapses into a single dotted identifier
// and the secrets migration fails with "relation does not exist". Pinning the property
// here turns that into a test failure on the dependency bump instead of a migration
// that cannot find its own table.
func TestFixtureIndexNameIgnoresTheSchemaPrefix(t *testing.T) {
	rec := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger:         rec,
		NamingStrategy: schema.NamingStrategy{TablePrefix: "device-management.", SingularTable: false},
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// The prefix really is configured: the parsed schema carries it, and only the
	// statement's own Table has had it stripped. Without this the test would pass just
	// as well against a DB with no prefix at all, and prove nothing.
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&prefixedThing{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if stmt.Schema.Table != "device-management.prefixed_things" {
		t.Fatalf("TablePrefix is not in effect; parsed table is %q", stmt.Schema.Table)
	}

	for _, tc := range []struct {
		name string
		call func(*gorm.DB, any) error
		want string
	}{
		{"token", CreateTenantTokenIndex, "uix_prefixed_things_tenant_token"},
		{"external id", CreateTenantExternalIdIndex, "uix_prefixed_things_tenant_external_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.statements = nil
			// The table does not exist (no AutoMigrate under this prefix on SQLite), so
			// the exec fails — the statement was still built and recorded, and its text
			// is the whole subject here.
			_ = tc.call(db, &prefixedThing{})
			sql := rec.only(t)
			if !strings.Contains(sql, "`"+tc.want+"`") {
				t.Fatalf("index name must be the unqualified %q, got statement: %s", tc.want, sql)
			}
		})
	}
}

// The behavioural counterweight to every pin above — freezing a statement is worth
// nothing if the index it names stopped enforcing anything — is already in this package,
// so it is cited rather than duplicated: TestCreateTenantTokenIndex (token_index_test.go)
// covers per-tenant scoping, rejection of a duplicate live token, reuse after
// soft-delete and idempotent re-creation; TestCreateTenantExternalIdIndex
// (external_id_index_test.go) does the same for the external id; and
// TestCreatePartialUniqueIndex covers the helper directly. The migration's own index has
// TestSecretsHandleIndexEnforcesLiveUniqueness in core/secrets.
