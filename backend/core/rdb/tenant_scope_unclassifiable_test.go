// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// These tests pin the one shape the tenant-scope callback cannot classify: a statement
// that names a table but whose destination gorm could not parse into a schema. There is
// no TenantId field to look for, so the callback has nothing to decide on — and the
// answer used to be "proceed", i.e. real rows read or written with no predicate and no
// error. It is now a refusal.
//
// 🔑 THE REFUSAL AND ITS ESCAPE HATCH ARE TESTED AS A PAIR, and neither is worth much
// alone. A refusal with no way out is an outage on the migration path (gorm's own
// migrator reads a table's columns through exactly this shape); an escape hatch with no
// refusal is the silent bypass this exists to close.

// seedTwoTenants writes one widget for tenant A and one for tenant B, so a statement
// that escapes the predicate returns or touches a row it must not.
func seedTwoTenants(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.WithContext(core.WithTenant(context.Background(), "A")).
		Create(&widget{Name: "a1"}).Error; err != nil {
		t.Fatalf("seeding tenant A: %v", err)
	}
	if err := db.WithContext(core.WithTenant(context.Background(), "B")).
		Create(&widget{Name: "b1"}).Error; err != nil {
		t.Fatalf("seeding tenant B: %v", err)
	}
}

// A bulk UPDATE spelled the natural way — a bare table and a map — is refused. This is
// the direction the fence's own doc names, and the one that silently rewrote every
// tenant's rows.
func TestUnclassifiableUpdateIsRefused(t *testing.T) {
	db := newTestDB(t)
	seedTwoTenants(t, db)

	ctx := core.WithTenant(context.Background(), "A")
	err := db.WithContext(ctx).Table("widgets").
		Where("name IS NOT NULL").
		Updates(map[string]interface{}{"name": "rewritten"}).Error
	if !errors.Is(err, ErrUnscopedStatement) {
		t.Fatalf("an unclassifiable bulk update was not refused: %v", err)
	}

	// The refusal has to have stopped the write, not merely reported on it. Tenant B's
	// row is the witness: it is the one this statement had no business touching.
	var b widget
	if err := db.WithContext(core.WithTenant(context.Background(), "B")).
		First(&b).Error; err != nil {
		t.Fatalf("reading tenant B back: %v", err)
	}
	if b.Name != "b1" {
		t.Fatalf("the refused update still rewrote another tenant's row: name=%q", b.Name)
	}
}

// The same statement in the READ direction. Refusing writes only would be a one-sided
// bound: the guarantee this callback carries is stated over reads first ("an unscoped
// read can never leak another tenant's rows"), and a bare-table read is how that leak
// would be spelled.
func TestUnclassifiableReadIsRefused(t *testing.T) {
	db := newTestDB(t)
	seedTwoTenants(t, db)

	ctx := core.WithTenant(context.Background(), "A")
	var rows []map[string]interface{}
	err := db.WithContext(ctx).Table("widgets").Find(&rows).Error
	if !errors.Is(err, ErrUnscopedStatement) {
		t.Fatalf("an unclassifiable read was not refused: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the refused read still returned %d row(s): %+v", len(rows), rows)
	}
}

// A DELETE against a bare table, refused for the same reason. Note the polarity trap
// this closes: a bare-table CREATE is already rejected today, but by a NOT NULL column
// rather than by this callback — a different mechanism that happens to catch one case
// and does nothing at all for update and delete.
func TestUnclassifiableDeleteIsRefused(t *testing.T) {
	db := newTestDB(t)
	seedTwoTenants(t, db)

	ctx := core.WithTenant(context.Background(), "A")
	err := db.WithContext(ctx).Table("widgets").
		Where("name IS NOT NULL").
		Delete(map[string]interface{}{}).Error
	if !errors.Is(err, ErrUnscopedStatement) {
		t.Fatalf("an unclassifiable delete was not refused: %v", err)
	}

	var count int64
	if err := db.WithContext(core.WithSystemContext(context.Background())).
		Model(&widget{}).Count(&count).Error; err != nil {
		t.Fatalf("counting rows back: %v", err)
	}
	if count != 2 {
		t.Fatalf("the refused delete still removed rows: %d of 2 remain", count)
	}
}

// The counterweight, and the reason the refusal is safe to ship: a system context still
// lets the same statement through. Instance-scoped work — bootstrap, migration, the
// erasure sweep — is not a tenant's work, and this is where it says so.
func TestSystemContextStillRunsAnUnclassifiableStatement(t *testing.T) {
	db := newTestDB(t)
	seedTwoTenants(t, db)

	sys := core.WithSystemContext(context.Background())
	var rows []map[string]interface{}
	if err := db.WithContext(sys).Table("widgets").Find(&rows).Error; err != nil {
		t.Fatalf("a system context was refused an unclassifiable read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("the system-context read saw %d row(s), want both tenants' 2", len(rows))
	}

	if err := db.WithContext(sys).Table("widgets").
		Where("name = ?", "a1").
		Updates(map[string]interface{}{"name": "renamed"}).Error; err != nil {
		t.Fatalf("a system context was refused an unclassifiable update: %v", err)
	}
}

// gorm's own migrator reads an existing table's columns with Table(name).Limit(1).Rows()
// — a statement with no schema and a table name, i.e. indistinguishable from the shape
// refused above. Every migration this platform runs is bound to a system context for
// exactly that reason (see rdb.ExecuteInitialize), so re-running AutoMigrate over tables
// that already exist must keep working.
//
// 🔴 THIS IS WHY IsSystemContext IS TESTED BEFORE CLASSIFICATION IN scopedTenant. Swap
// those two and the refusal reaches the migrator, and a migration chain that replays
// after a failure — which is how this platform's migrations are required to behave —
// stops at the first table it already created.
func TestSystemContextStillReMigratesAnExistingTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("registering tenant scoping: %v", err)
	}
	sys := db.WithContext(core.WithSystemContext(context.Background()))
	if err := sys.AutoMigrate(&widget{}); err != nil {
		t.Fatalf("first AutoMigrate: %v", err)
	}
	if err := sys.AutoMigrate(&widget{}); err != nil {
		t.Fatalf("re-running AutoMigrate over an existing table: %v", err)
	}
}

// Raw SQL is deliberately NOT refused, and stating it as a test rather than as a comment
// is the point: it is out of reach of the clause builder entirely, so a refusal here
// would not be isolation — it would be an outage on every raw read, moving the same
// blind spot to a different error message. The blind spot itself is documented on
// RegisterTenantScoping rather than denied by it.
func TestRawSqlIsNotRefused(t *testing.T) {
	db := newTestDB(t)
	seedTwoTenants(t, db)

	ctx := core.WithTenant(context.Background(), "A")
	var count int64
	if err := db.WithContext(ctx).Raw("SELECT count(*) FROM widgets").Scan(&count).Error; err != nil {
		t.Fatalf("raw SQL was refused: %v", err)
	}
	if count != 2 {
		t.Fatalf("raw SQL saw %d rows, want 2 — it is unscoped by construction", count)
	}
}

// The counterweight to every refusal above: an ordinary typed statement is untouched,
// still scoped, and still sees exactly its own tenant's rows. A guard that refuses the
// unclassifiable is only safe while well-formed work still passes.
func TestTypedStatementsAreUnaffected(t *testing.T) {
	db := newTestDB(t)
	seedTwoTenants(t, db)

	ctx := core.WithTenant(context.Background(), "A")
	var found []widget
	if err := db.WithContext(ctx).Find(&found).Error; err != nil {
		t.Fatalf("a typed read was refused: %v", err)
	}
	if len(found) != 1 || found[0].Name != "a1" {
		t.Fatalf("the typed read returned %+v, want tenant A's single row", found)
	}

	if err := db.WithContext(ctx).Model(&widget{}).
		Where("name = ?", "a1").
		Updates(map[string]interface{}{"name": "a1-renamed"}).Error; err != nil {
		t.Fatalf("a typed update through a map destination was refused: %v", err)
	}

	// A model with no TenantId still passes through untouched, with no tenant at all.
	if err := db.WithContext(context.Background()).Create(&gadget{Name: "g1"}).Error; err != nil {
		t.Fatalf("an unscoped model was refused: %v", err)
	}
}

// A statement with neither a schema nor a table is left to gorm, which already reports it
// precisely ("Table not set, please set it like: db.Model(&user) or db.Table(\"users\")").
// Replacing that with this refusal would trade a specific message for a vaguer one.
func TestAStatementWithNoTableIsLeftToGorm(t *testing.T) {
	db := newTestDB(t)
	ctx := core.WithTenant(context.Background(), "A")
	var rows []map[string]interface{}
	err := db.WithContext(ctx).Find(&rows).Error
	if err == nil {
		t.Fatal("a destination with no table and no schema was accepted")
	}
	if errors.Is(err, ErrUnscopedStatement) {
		t.Fatalf("the tenant-scope refusal displaced gorm's own table-not-set error: %v", err)
	}
}
