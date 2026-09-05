// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// NewSQLiteDB builds a throwaway SQLite database migrated for one family, with the same
// global callbacks production registers.
//
// 🔴 THE DSN IS A NAMED SHARED-CACHE DATABASE, NOT ":memory:", and the difference is not
// cosmetic. A bare ":memory:" gives every pooled connection its OWN database, so a family
// whose seed opens a transaction — publishing an entity group or an asset type, minting a
// fence-set version — writes into one database and reads back from another, and the
// failure reads as a missing table rather than as the connection-per-database it is.
// Naming it after the test keeps the isolation that ":memory:" was chosen for.
//
// 🔴 AND THE POOL MUST BE CLOSED, OR THE NAME OUTLIVES THE TEST. A shared-cache in-memory
// database exists for as long as ONE connection to it is open, and gorm never closes the
// pool by itself — so a second run of the same test name reopens the database the first
// one left behind, complete with its rows, and the seed's "exactly one" reload finds two.
// `go test -count=2` failed exactly that way. CI passes -count=1, which is why this hid:
// the leak needs the same test name to run twice in one process, and nothing in CI does
// that. The Cleanup below is what makes the fixture's isolation a property of the fixture
// rather than of how it happens to be invoked.
//
// 🔴 THE TOKEN GRAMMAR IS REGISTERED BECAUSE PRODUCTION REGISTERS IT. It used not to be,
// and that is not a tidiness point: a harness weaker than the world it certifies passes
// things the real system refuses, so a defect can be green here and broken live. It let a
// whitespace-only profile token through. Suite.CreateWithToken is what pins the
// registration rather than assuming it — see the FixtureIsAsStrictAsProduction property.
func NewSQLiteDB(t *testing.T, tables ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+DSNName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	closeOnCleanup(t, db)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(tables...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TenantContext is the context every family runs under. Services pass it as
// Suite.Context; it is a function rather than a value so each subtest gets a fresh one.
func TenantContext(tenant string) func() context.Context {
	return func() context.Context { return core.WithTenant(context.Background(), tenant) }
}

// DSNName turns a test's name into something safe to sit in a SQLite URI. Subtest names
// carry "/" and the harness's carry "#" once the same name repeats, both of which a URI
// filename reads as structure rather than as a name.
//
// 🔴 THE "#" IS THE DANGEROUS ONE. A URI reads it as the start of a fragment, so the name
// silently truncates and sqlite falls back to an ON-DISK FILE — a test fixture writing to
// the working directory, sharing state with every other test whose name truncates the
// same way, and outliving the run. Go appends "#01", "#02" … whenever one test name
// repeats within a run, which is exactly what a table-driven harness produces.
func DSNName(t *testing.T) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, t.Name())
}

// closeOnCleanup releases the pool when the test ends, which is what bounds a
// shared-cache in-memory database's LIFETIME.
//
// 🔴 IT IS NOT TIDINESS. Such a database exists while at least one connection to it is
// open, and gorm closes nothing on its own — so the name survives the test that made it,
// and the next test to use the same name inherits its rows. See NewSQLiteDB for how that
// surfaced.
//
// A failure to close is reported rather than ignored: a Close that started erroring would
// otherwise reintroduce the leak silently, which is the whole failure mode again.
func closeOnCleanup(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err != nil {
			t.Errorf("reach the underlying pool to close it: %v", err)
			return
		}
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close the fixture's pool: %v — the shared-cache database outlives this "+
				"test and the next one to reuse its name inherits these rows", err)
		}
	})
}

// RequireOne is the reload guard every family's read shares: exactly one row, or the test
// is measuring something other than what it seeded.
func RequireOne[E any](t *testing.T, what string, rows []*E, err error) *E {
	t.Helper()
	if err != nil {
		t.Fatalf("reload %s: %v", what, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one %s, got %d", what, len(rows))
	}
	return rows[0]
}
