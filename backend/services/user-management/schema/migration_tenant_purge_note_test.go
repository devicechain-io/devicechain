// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 WHY THIS TEST EXISTS, given that hack/migration-diff.sh already gates the schema.
//
// migration-diff would catch this — it diffs pg_dump --schema-only, so both a missing column
// and a stray table show up. But it needs a Postgres container and it runs in CI, which makes
// it the wrong instrument for the one mistake this migration is most likely to make: dropping
// the pinned TableName. That mistake does not fail. AutoMigrate happily creates a
// `tenant_purge_store_note_snapshots` table, reports success, and leaves the ledger without
// the column every writer above it now assigns to — a green migration that migrated nothing.
//
// So it is pinned here too, where it costs milliseconds and no container.
//
// Note the ASYMMETRY in what the two tests can see: this one runs the chain on sqlite, so it
// proves the migration is wired into Migrations and does what it says. It cannot speak for the
// Postgres column type. That is migration-diff's job, and neither test substitutes for the other.

// purgeStoreNoteRow is an independent read-back type, deliberately NOT the snapshot the
// migration writes through. Sharing the struct would let a wrong TableName pass by being
// wrong in both places at once.
type purgeStoreNoteRow struct {
	ID   uint
	Note string
}

func (purgeStoreNoteRow) TableName() string { return "iam_tenant_purge_stores" }

func newMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	for _, m := range Migrations {
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	return db
}

// TestTheNoteMigrationAddsTheColumnToTheLedgerTable runs the whole chain and proves the column
// landed on the ledger table — by writing through it and reading it back, not by asking the
// migrator whether it thinks the column exists.
func TestTheNoteMigrationAddsTheColumnToTheLedgerTable(t *testing.T) {
	db := newMigratedDB(t)

	require.True(t, db.Migrator().HasColumn(&purgeStoreNoteRow{}, "note"),
		"the note column must exist on iam_tenant_purge_stores after the chain runs")

	require.NoError(t, db.Exec(
		`INSERT INTO iam_tenant_purge_stores (tenant_purge_id, store, note) VALUES (?, ?, ?)`,
		1, "kv", "the exempted buckets were not scanned").Error)

	var back purgeStoreNoteRow
	require.NoError(t, db.First(&back).Error)
	require.Equal(t, "the exempted buckets were not scanned", back.Note,
		"a note written to the ledger table must read back from it")
}

// TestTheNoteMigrationDoesNotCreateAStrayTable is the control, and it is the one that catches
// the mistake this migration is actually exposed to.
//
// Without the pinned TableName, AutoMigrate derives the table from the Go type name and creates
// a brand-new table — succeeding, and leaving the ledger untouched. The test above would then
// fail on the missing column, but only because it happens to check the right table; this one
// names the failure directly, so a future reader sees WHAT went wrong rather than only that
// something did.
func TestTheNoteMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)

	for _, stray := range []string{
		"tenant_purge_store_note_snapshots",
		"tenant_purge_store_notes",
	} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the note migration created %q instead of altering the ledger — its snapshot has lost "+
				"its pinned TableName, so it migrated nothing", stray)
	}
}

// TestTheNoteMigrationIsReRunnable pins what the chain requires of every appended migration:
// migrations run with UseTransaction:false and replay from the top after a failure, so one
// that is not individually re-runnable turns a single transient failure into a stuck instance.
func TestTheNoteMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantPurgeNoteMigration().Migrate(db),
		"re-running the note migration must be a no-op, not a duplicate-column error")
	require.True(t, db.Migrator().HasColumn(&purgeStoreNoteRow{}, "note"))
}

// TestTheNoteMigrationRollbackDropsOnlyTheNoteColumn pins WHICH column the rollback drops.
//
// Nothing in the platform invokes gormigrate rollbacks today, so this is latent — which is
// exactly why it is worth a test rather than a reading. The day someone reaches for one, a
// rollback naming the wrong column would drop `deferred`, the field completion reasons about
// in words, and it would do so while succeeding.
func TestTheNoteMigrationRollbackDropsOnlyTheNoteColumn(t *testing.T) {
	db := newMigratedDB(t)
	m := NewTenantPurgeNoteMigration()
	require.NoError(t, m.Rollback(db))

	require.False(t, db.Migrator().HasColumn(&purgeStoreNoteRow{}, "note"),
		"the rollback must drop the column this migration added")
	for _, kept := range []string{"deferred", "failure", "complete", "rows", "clean_since"} {
		require.Truef(t, db.Migrator().HasColumn(&purgeStoreNoteRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
