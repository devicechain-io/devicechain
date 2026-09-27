// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb_test

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/conflict"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// staleWidget is a row with a version and one association, so a test can see whether
// the guarded write touches anything but the row it names.
type staleWidget struct {
	gorm.Model
	Name  string
	Parts []staleWidgetPart `gorm:"foreignKey:WidgetID"`
}

type staleWidgetPart struct {
	gorm.Model
	WidgetID uint
	Label    string
}

func newStaleWidgetDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// One connection: every :memory: connection is its own database.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&staleWidget{}, &staleWidgetPart{}))
	return db
}

// readWidget re-reads a row from the database, so an assertion is about what is STORED
// and not about the in-memory copy a write left behind.
func readWidget(t *testing.T, db *gorm.DB, id uint) staleWidget {
	t.Helper()
	var w staleWidget
	require.NoError(t, db.Preload("Parts").First(&w, id).Error)
	return w
}

var staleWidgets = rdb.NewStaleWriteError("widget")

// The console recognises the refusal by its wording alone (it carries no code), so the
// sentence is pinned byte for byte.
func TestStaleWriteErrorIsTheSentenceTheConsoleMatches(t *testing.T) {
	assert.Equal(t, "dashboard was modified by another writer; reload and try again",
		rdb.NewStaleWriteError("dashboard").Error())
	assert.Equal(t, "notification policy was modified by another writer; reload and try again",
		rdb.NewStaleWriteError("notification policy").Error())
}

// A lost update is not a taken value: CONFLICT would tell a client "already exists, carry
// on" about a save that never happened.
func TestStaleWriteErrorIsNotAUniquenessConflict(t *testing.T) {
	var err error = staleWidgets
	assert.False(t, conflict.Is(err), "a stale write must not classify as a conflict")
	_, coded := err.(interface{ Extensions() map[string]interface{} })
	assert.False(t, coded, "a stale write must carry no extensions.code")
}

// The precondition a caller sends is the string core/graphql.FormatTime served. The
// check must accept that string for the instant it was formatted from, to the nanosecond.
func TestRefuseIfMovedAcceptsTheServedLayout(t *testing.T) {
	ts := time.Date(2026, 9, 27, 10, 0, 0, 123456789, time.UTC)
	served := gqlcore.FormatTime(ts)
	require.NotNil(t, served)
	require.Equal(t, "2026-09-27T10:00:00.123456789Z", *served)

	assert.NoError(t, rdb.RefuseIfMoved(ts, served, staleWidgets))
	assert.NoError(t, rdb.RefuseIfMoved(ts, nil, staleWidgets), "no precondition means last write wins")
}

// A copy stale by less than a second is still stale.
func TestRefuseIfMovedIsSubSecond(t *testing.T) {
	stored := time.Date(2026, 9, 27, 10, 0, 0, 900_000_000, time.UTC)
	sent := time.Date(2026, 9, 27, 10, 0, 0, 100_000_000, time.UTC)
	// The negative control: at whole seconds the two are the same string, so the test
	// below proves sub-second precision rather than merely "a different value".
	require.Equal(t, stored.Format(time.RFC3339), sent.Format(time.RFC3339))

	err := rdb.RefuseIfMoved(stored, gqlcore.FormatTime(sent), staleWidgets)
	assert.Same(t, staleWidgets, err)
}

// A writer who landed between the read and the guarded write keeps their value; the
// late write is refused with the service's own error and changes nothing.
func TestUpdateIfUnmovedRefusesARowThatMovedAndLeavesItAlone(t *testing.T) {
	db := newStaleWidgetDB(t)
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	loaded := readWidget(t, db, row.ID)
	readAt := loaded.UpdatedAt

	// The other writer, after our read. Its own version is one second later, so the two
	// cannot collide on the clock.
	require.NoError(t, db.Model(&staleWidget{}).Where("id = ?", row.ID).
		Updates(map[string]any{"name": "other", "updated_at": readAt.Add(time.Second)}).Error)

	err := rdb.UpdateIfUnmoved(db, &loaded, readAt, map[string]any{"name": "mine"}, staleWidgets)
	require.True(t, errors.Is(err, staleWidgets), "got %v", err)
	assert.Same(t, staleWidgets, err)

	after := readWidget(t, db, row.ID)
	assert.Equal(t, "other", after.Name)
	assert.True(t, after.UpdatedAt.Equal(readAt.Add(time.Second)), "the other writer's version moved: %v", after.UpdatedAt)
}

// The counterweight: an unmoved row is written, and its version moves.
func TestUpdateIfUnmovedWritesAnUnmovedRow(t *testing.T) {
	db := newStaleWidgetDB(t)
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	loaded := readWidget(t, db, row.ID)
	readAt := loaded.UpdatedAt

	require.NoError(t, rdb.UpdateIfUnmoved(db, &loaded, readAt, map[string]any{"name": "mine"}, staleWidgets))

	after := readWidget(t, db, row.ID)
	assert.Equal(t, "mine", after.Name)
	assert.True(t, after.UpdatedAt.After(readAt), "updated_at %v did not move past %v", after.UpdatedAt, readAt)
}

// Only the named row is written, even when another row shares its version exactly.
// The key condition comes from the loaded record, so this is what breaks if it is lost.
func TestUpdateIfUnmovedWritesOnlyTheLoadedRow(t *testing.T) {
	db := newStaleWidgetDB(t)
	a := &staleWidget{Name: "a"}
	b := &staleWidget{Name: "b"}
	require.NoError(t, db.Create(a).Error)
	require.NoError(t, db.Create(b).Error)
	shared := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec("UPDATE stale_widgets SET updated_at = ?", shared).Error)
	loaded := readWidget(t, db, a.ID)

	require.NoError(t, rdb.UpdateIfUnmoved(db, &loaded, loaded.UpdatedAt, map[string]any{"name": "mine"}, staleWidgets))

	assert.Equal(t, "mine", readWidget(t, db, a.ID).Name)
	assert.Equal(t, "b", readWidget(t, db, b.ID).Name, "a row sharing the version was written too")
}

// A record with no key would leave `updated_at = ?` as the only condition, which updates
// every row that shares the timestamp. It is refused outright, and nothing is written.
func TestUpdateIfUnmovedRefusesARecordWithNoKey(t *testing.T) {
	db := newStaleWidgetDB(t)
	a := &staleWidget{Name: "a"}
	require.NoError(t, db.Create(a).Error)
	loaded := readWidget(t, db, a.ID)

	err := rdb.UpdateIfUnmoved(db, &staleWidget{}, loaded.UpdatedAt, map[string]any{"name": "mine"}, staleWidgets)
	require.Error(t, err)
	assert.False(t, errors.Is(err, staleWidgets), "a misuse must not read as a concurrent edit: %v", err)
	assert.Contains(t, err.Error(), "needs the loaded row")
	assert.Equal(t, "a", readWidget(t, db, a.ID).Name)
}

// Nothing to write is a caller defect, not a success that moved nothing.
func TestUpdateIfUnmovedRefusesAnEmptyWrite(t *testing.T) {
	db := newStaleWidgetDB(t)
	a := &staleWidget{Name: "a"}
	require.NoError(t, db.Create(a).Error)
	loaded := readWidget(t, db, a.ID)

	err := rdb.UpdateIfUnmoved(db, &loaded, loaded.UpdatedAt, map[string]any{}, staleWidgets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no columns to write")
	after := readWidget(t, db, a.ID)
	assert.True(t, after.UpdatedAt.Equal(loaded.UpdatedAt), "an empty write moved the version")
}

// The guarded write saves the row and nothing it carries. A map Updates on a model
// upserts its preloaded associations otherwise, so a part another writer deleted would
// come back — whether or not the row itself was written.
func TestUpdateIfUnmovedNeverSavesTheRecordsAssociations(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "unmoved"
		if moved {
			name = "moved"
		}
		t.Run(name, func(t *testing.T) {
			db := newStaleWidgetDB(t)
			row := &staleWidget{Name: "w", Parts: []staleWidgetPart{{Label: "p1"}}}
			require.NoError(t, db.Create(row).Error)
			loaded := readWidget(t, db, row.ID)
			require.Len(t, loaded.Parts, 1)
			readAt := loaded.UpdatedAt

			// Another writer hard-deletes the part the loaded copy still carries.
			require.NoError(t, db.Unscoped().Where("widget_id = ?", row.ID).Delete(&staleWidgetPart{}).Error)
			if moved {
				require.NoError(t, db.Exec("UPDATE stale_widgets SET updated_at = ? WHERE id = ?",
					readAt.Add(time.Second), row.ID).Error)
			}

			err := rdb.UpdateIfUnmoved(db, &loaded, readAt, map[string]any{"name": "mine"}, staleWidgets)
			if moved {
				require.Same(t, staleWidgets, err)
			} else {
				require.NoError(t, err)
			}

			var parts int64
			require.NoError(t, db.Unscoped().Model(&staleWidgetPart{}).Count(&parts).Error)
			assert.Equal(t, int64(0), parts, "the guarded write re-inserted an association it was handed")
		})
	}
}
