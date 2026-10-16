// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sqlitetest opens throwaway in-memory SQLite databases for tests: one per call,
// closed when the test ends. It is its own package rather than part of rdbtest because
// rdbtest is also linked into the schema tools under backend/tools, which have no use for
// a SQLite driver.
package sqlitetest

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// sqliteNonce makes every MemoryDSN distinct for the life of the process. In-memory
// databases are per process, so a process-wide counter is all the uniqueness they need.
var sqliteNonce atomic.Uint64

// MemoryDSN returns the DSN of a named, shared-cache, in-memory SQLite database that
// belongs to this call alone. Each extra query parameter ("_pragma=foreign_keys(1)") is
// appended as given.
//
// A test fixture wants the shared cache, not a bare ":memory:", because a bare one gives
// every pooled connection its OWN database: a seed that opens a transaction writes into
// one and reads back from another, and that fails as a missing table. Sharing the cache
// needs a NAME, and the name is where the trouble was.
//
// 🔴 THE NAME CARRIES A PER-CALL NONCE, NOT JUST THE TEST'S NAME. A shared-cache database
// lives while any connection to it is open, and is found again by its name. Built from
// t.Name() alone, the name repeats whenever the same test runs twice in one process
// (`go test -count=2`, or a flake hunt with -count=N), and the second run reopens the
// first run's database, rows included. That made some three hundred tests across core,
// device-management, update-management and user-management fail at -count=2 while CI,
// which runs -count=1, stayed green: a publish minted version 2 instead of 1, containment
// checks found doubled members, and every failure looked like a real defect. The nonce makes a fresh database a property of the call
// rather than of how the test binary happened to be invoked.
//
// A caller that needs a SECOND handle on the same database (to watch a write from outside
// the transaction, say) opens this same DSN again; it must not call this a second time,
// which would give it a different, empty database.
//
// The name is restricted to letters and digits plus '_'. A subtest name carries '/', and
// a repeated one '#', and a URI reads '#' as the start of a fragment: the name silently
// truncates and sqlite falls back to an ON-DISK file in the working directory.
//
// This is the only place in the repository that writes a shared-cache DSN; the
// repository guard in core/test refuses one anywhere else, so the nonce cannot be left
// out of the next fixture by copying an older one.
func MemoryDSN(t testing.TB, params ...string) string {
	t.Helper()
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, t.Name())
	dsn := "file:" + name + "_" + strconv.FormatUint(sqliteNonce.Add(1), 10) + "?mode=memory&cache=shared"
	for _, p := range params {
		dsn += "&" + p
	}
	return dsn
}

// OpenMemory opens a fresh database at MemoryDSN(t, params...) and closes its
// pool when the test ends. A nil cfg means a zero gorm.Config.
//
// 🔴 THE CLOSE IS NOT TIDINESS. gorm closes nothing on its own, and a shared-cache
// in-memory database exists for as long as one connection to it is open, so without the
// Cleanup every database a test opened would live until the process exits. The nonce
// already keeps a later run from seeing it; the Close is what keeps a -count=N run, or a
// large package, from holding N copies of every fixture's tables in memory at once.
func OpenMemory(t testing.TB, cfg *gorm.Config, params ...string) *gorm.DB {
	t.Helper()
	return OpenDSN(t, MemoryDSN(t, params...), cfg)
}

// OpenDSN opens dsn — normally one MemoryDSN returned — and closes the pool
// when the test ends. It is for the caller that needs the DSN as well as the handle, to
// open a second one on the same database. A nil cfg means a zero gorm.Config.
func OpenDSN(t testing.TB, dsn string, cfg *gorm.Config) *gorm.DB {
	t.Helper()
	if cfg == nil {
		cfg = &gorm.Config{}
	}
	db, err := gorm.Open(sqlite.Open(dsn), cfg)
	if err != nil {
		t.Fatalf("open sqlite %s: %v", dsn, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite pool for %s: %v", dsn, err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("closing sqlite %s: %v", dsn, err)
		}
	})
	return db
}
