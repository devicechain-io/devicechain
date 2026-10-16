// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sqlitetest

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type memRow struct {
	ID uint `gorm:"primaryKey"`
}

func (memRow) TableName() string { return "mem_rows" }

func seedOneRow(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&memRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&memRow{}).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func hasTable(db *gorm.DB) bool { return db.Migrator().HasTable(&memRow{}) }

// The -count=2 failure in one test: the same test name asks for a database twice, and
// the second must not find what the first left. Two calls within ONE test stand in for
// two runs of it, which is what a test binary cannot do to itself.
func TestEachCallGetsItsOwnDatabase(t *testing.T) {
	first := OpenMemory(t, nil)
	seedOneRow(t, first)

	second := OpenMemory(t, nil)
	if hasTable(second) {
		t.Fatal("a second database opened under the same test name found the first one's " +
			"table, so a test run twice in one process inherits its own earlier rows")
	}
}

// The counterweight: the DSN must still NAME one shared database, or a fixture's second
// handle (and every pooled connection behind one handle) would each get a private one.
func TestTheSameDSNReachesTheSameDatabase(t *testing.T) {
	dsn := MemoryDSN(t)
	a := OpenDSN(t, dsn, nil)
	seedOneRow(t, a)

	b := OpenDSN(t, dsn, nil)
	var n int64
	if err := b.Model(&memRow{}).Count(&n).Error; err != nil {
		t.Fatalf("a second handle on %s cannot read the first one's table: %v", dsn, err)
	}
	if n != 1 {
		t.Fatalf("a second handle on %s counts %d rows, want the 1 the first wrote", dsn, n)
	}
}

// The database dies with its test: the pool is closed in Cleanup, and a shared-cache
// in-memory database ends with its last connection.
func TestTheDatabaseDoesNotOutliveItsTest(t *testing.T) {
	var dsn string
	t.Run("owner", func(t *testing.T) {
		dsn = MemoryDSN(t)
		seedOneRow(t, OpenDSN(t, dsn, nil))
	})

	after, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("reopen %s: %v", dsn, err)
	}
	sqlDB, err := after.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()
	if hasTable(after) {
		t.Fatalf("%s still holds its table after the test that opened it ended, so the pool "+
			"was never closed and the database lives until the process exits", dsn)
	}
}

// A subtest name carries '/' and a repeated one '#'; '#' in a URI starts a fragment and
// sends sqlite to an on-disk file. The name part must be plain.
func TestTheNameIsSafeInAURI(t *testing.T) {
	for range 2 { // the second iteration's name gets Go's "#01" suffix
		t.Run("a/b c", func(t *testing.T) {
			dsn := MemoryDSN(t, "_pragma=foreign_keys(1)")
			name, query, ok := strings.Cut(strings.TrimPrefix(dsn, "file:"), "?")
			if !ok {
				t.Fatalf("%s has no query", dsn)
			}
			if strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
				t.Fatalf("name %q carries a character a URI would read as structure", name)
			}
			if query != "mode=memory&cache=shared&_pragma=foreign_keys(1)" {
				t.Fatalf("query is %q", query)
			}
		})
	}
}
