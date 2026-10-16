// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
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
// sqlitetest.OpenMemory names it per call and closes it with the test, which keeps the
// isolation that ":memory:" was chosen for — including when the same test runs twice in
// one process, which is how `go test -count=2` used to find the first run's rows here.
//
// 🔴 ALL FOUR CALLBACKS ARE REGISTERED BECAUSE PRODUCTION REGISTERS ALL FOUR, and that
// is not a tidiness point: a harness weaker than the world it certifies passes things the
// real system refuses, so a defect can be green here and broken live. The token grammar
// used not to be here and it let a whitespace-only profile token through; the audit
// journal and the erasure fence used not to be here either, while this comment claimed
// parity with production, so every property below was being asserted against a callback
// chain two hooks shorter than the shipped one.
//
// The list is rdb.ConnectPostgres's, in its order, and the four are:
//
//   - tenant scoping — the row-level isolation predicate, fail-closed on no tenant;
//   - the token grammar — fail-closed token validation on create and update;
//   - the audit journal — an After hook that appends an AuditEvent for every mutation and
//     reports a failure to record one as the MUTATION's error;
//   - the erasure fence — a Before hook that refuses a create or update for a tenant this
//     area has reclaimed.
//
// FixtureIsAsStrictAsProduction is what pins all of it rather than assuming it.
//
// 🔴 THE LAST TWO NEED TABLES, WHICH IS WHY THE MIGRATE LIST IS PREPENDED TO RATHER THAN
// PASSED THROUGH. The fence FAILS CLOSED on an unreadable purged_tenants — an
// unanswerable fence is not an absent one — so registering it without creating that table
// refuses every write in the fixture, and the journal's insert would fail the same way
// against a missing audit_events. Production creates both in ExecuteInitialize's
// AutoMigrate for exactly this reason; the fixture does it here. The caller's slice is
// never appended to in place: the tables come back on a fresh backing array so a family's
// Migrate list cannot be grown by having been passed here.
func NewSQLiteDB(t *testing.T, tables ...any) *gorm.DB {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := rdb.RegisterAuditJournal(db); err != nil {
		t.Fatalf("register audit journal: %v", err)
	}
	if err := rdb.RegisterTenantFence(db); err != nil {
		t.Fatalf("register tenant fence: %v", err)
	}
	migrate := append([]any{&rdb.AuditEvent{}, &rdb.PurgedTenant{}}, tables...)
	if err := db.AutoMigrate(migrate...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TenantContext is the context every family runs under. Services pass it as
// Suite.Context; it is a function rather than a value so each subtest gets a fresh one.
func TenantContext(tenant string) func() context.Context {
	return func() context.Context { return core.WithTenant(context.Background(), tenant) }
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
