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
	return newStaleWidgetDBAt(t, nil)
}

// newStaleWidgetDBAt is newStaleWidgetDB with gorm's clock replaced; nil keeps gorm's own.
func newStaleWidgetDBAt(t *testing.T, clock func() time.Time) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard, NowFunc: clock})
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

// frozen is an instant with sub-microsecond digits on purpose. It is in the past, so the real
// clock is always beyond the floor derived from it (do not move it into the future).
var frozen = time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC)

func floorOf(readAt time.Time) time.Time {
	return readAt.Truncate(time.Microsecond).Add(time.Microsecond)
}

// Two saves that read the same instant from the clock must not leave the row at the version
// the first one read: a second editor holding that version would still be accepted and would
// overwrite the first.
func TestUpdateIfUnmovedMovesTheVersionWhenTheClockDoesNot(t *testing.T) {
	db := newStaleWidgetDBAt(t, func() time.Time { return frozen })
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	first := readWidget(t, db, row.ID)
	second := readWidget(t, db, row.ID)
	readAt := first.UpdatedAt

	require.NoError(t, rdb.UpdateIfUnmoved(db, &first, readAt, map[string]any{"name": "first writer"}, staleWidgets))

	after := readWidget(t, db, row.ID)
	assert.True(t, after.UpdatedAt.Equal(floorOf(readAt)), "stored version %v, want %v", after.UpdatedAt, floorOf(readAt))
	assert.Same(t, staleWidgets, rdb.RefuseIfMoved(after.UpdatedAt, gqlcore.FormatTime(readAt), staleWidgets))
	assert.Same(t, staleWidgets, rdb.UpdateIfUnmoved(db, &second, readAt, map[string]any{"name": "second writer"}, staleWidgets))
	assert.Equal(t, "first writer", readWidget(t, db, row.ID).Name)
}

// A replica whose clock is behind must not move the version backwards.
func TestUpdateIfUnmovedMovesTheVersionWhenTheClockIsBehind(t *testing.T) {
	db := newStaleWidgetDBAt(t, func() time.Time { return frozen.Add(-time.Hour) })
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	require.NoError(t, db.Model(&staleWidget{}).Where("id = ?", row.ID).
		UpdateColumn("updated_at", frozen).Error)
	loaded := readWidget(t, db, row.ID)
	readAt := loaded.UpdatedAt
	require.True(t, readAt.Equal(frozen))

	require.NoError(t, rdb.UpdateIfUnmoved(db, &loaded, readAt, map[string]any{"name": "mine"}, staleWidgets))

	after := readWidget(t, db, row.ID)
	assert.True(t, after.UpdatedAt.Equal(floorOf(frozen)), "stored version %v, want %v", after.UpdatedAt, floorOf(frozen))
}

// The counterweight: a clock that has moved on is what is stored; the floor invents nothing.
func TestUpdateIfUnmovedStoresTheClockWhenItHasMoved(t *testing.T) {
	now := frozen
	db := newStaleWidgetDBAt(t, func() time.Time { return now })
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	loaded := readWidget(t, db, row.ID)

	later := time.Date(2100, 1, 1, 0, 0, 0, 987654321, time.UTC)
	now = later
	require.NoError(t, rdb.UpdateIfUnmoved(db, &loaded, loaded.UpdatedAt, map[string]any{"name": "mine"}, staleWidgets))

	after := readWidget(t, db, row.ID)
	assert.Equal(t, "mine", after.Name)
	assert.True(t, after.UpdatedAt.Equal(later), "stored version %v, want %v", after.UpdatedAt, later)
}

func TestUpdateIfUnmovedLeavesTheCallersMapAlone(t *testing.T) {
	db := newStaleWidgetDB(t)
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	loaded := readWidget(t, db, row.ID)

	m := map[string]any{"name": "mine"}
	require.NoError(t, rdb.UpdateIfUnmoved(db, &loaded, loaded.UpdatedAt, m, staleWidgets))
	assert.Equal(t, map[string]any{"name": "mine"}, m)
}

// The version is the write's to set; a caller naming it is a defect, not a stale write.
func TestUpdateIfUnmovedRefusesACallerSuppliedVersion(t *testing.T) {
	for _, key := range []string{"updated_at", "UpdatedAt"} {
		t.Run(key, func(t *testing.T) {
			db := newStaleWidgetDB(t)
			row := &staleWidget{Name: "original"}
			require.NoError(t, db.Create(row).Error)
			loaded := readWidget(t, db, row.ID)

			err := rdb.UpdateIfUnmoved(db, &loaded, loaded.UpdatedAt,
				map[string]any{"name": "mine", key: frozen}, staleWidgets)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is the version this write sets")
			assert.False(t, errors.Is(err, staleWidgets))

			after := readWidget(t, db, row.ID)
			assert.Equal(t, "original", after.Name)
			assert.True(t, after.UpdatedAt.Equal(loaded.UpdatedAt))
		})
	}
}

// The writes that carry no precondition must move the version too, or a writer who read it
// before them is not refused.
func TestAdvancingFromMovesAnUnguardedWrite(t *testing.T) {
	cases := map[string]func(db *gorm.DB, w *staleWidget, readAt time.Time) error{
		"Save": func(db *gorm.DB, w *staleWidget, readAt time.Time) error {
			w.Name = "unguarded"
			return rdb.AdvancingFrom(db, readAt).Omit("Parts").Save(w).Error
		},
		"Update": func(db *gorm.DB, w *staleWidget, readAt time.Time) error {
			return rdb.AdvancingFrom(db, readAt).Model(w).Update("name", "unguarded").Error
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			db := newStaleWidgetDBAt(t, func() time.Time { return frozen })
			row := &staleWidget{Name: "original"}
			require.NoError(t, db.Create(row).Error)
			loaded := readWidget(t, db, row.ID)
			stale := readWidget(t, db, row.ID)
			readAt := loaded.UpdatedAt

			require.NoError(t, write(db, &loaded, readAt))

			after := readWidget(t, db, row.ID)
			assert.Equal(t, "unguarded", after.Name)
			assert.True(t, after.UpdatedAt.Equal(floorOf(readAt)), "stored version %v, want %v", after.UpdatedAt, floorOf(readAt))
			assert.Same(t, staleWidgets, rdb.UpdateIfUnmoved(db, &stale, readAt, map[string]any{"name": "late"}, staleWidgets))
			assert.Equal(t, "unguarded", readWidget(t, db, row.ID).Name)
		})
	}
}

func TestAdvancingFromStoresTheClockWhenItHasMoved(t *testing.T) {
	later := time.Date(2100, 1, 1, 0, 0, 0, 987654321, time.UTC)
	now := frozen
	db := newStaleWidgetDBAt(t, func() time.Time { return now })
	row := &staleWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	loaded := readWidget(t, db, row.ID)

	now = later
	require.NoError(t, rdb.AdvancingFrom(db, loaded.UpdatedAt).Model(&loaded).Update("name", "x").Error)
	assert.True(t, readWidget(t, db, row.ID).UpdatedAt.Equal(later))
}
