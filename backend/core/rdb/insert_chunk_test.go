// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Fixtures for RowsPerInsert. Each names its columns in its own shape; the expected counts
// below are written out, not derived, so a change to what is counted shows up here.
type chunkThree struct {
	A string
	B string
	C int
}

type chunkScoped struct {
	TenantScoped
	A string
	B string
}

type chunkReadOnly struct {
	A string
	B string
	C int
	D string `gorm:"<-:false"`
}

type chunkIgnored struct {
	A string
	B string
	C int
	D string `gorm:"-"`
}

type chunkEmpty struct {
	A string `gorm:"-"`
}

// chunkJSON has a column gorm binds through GormValue: an expression that can carry any
// number of parameters, so the row's width cannot be bounded by counting.
type chunkJSON struct {
	A   string
	Doc datatypes.JSON
}

// ptrValued has a GormValue with a POINTER receiver, and its expression binds two parameters.
// gorm reads a column's value by value, so a ptrValued column is not a gorm.Valuer to it and
// binds one plain value; a *ptrValued column is, and binds the expression.
type ptrValued string

func (p *ptrValued) GormValue(context.Context, *gorm.DB) clause.Expr {
	return clause.Expr{SQL: "? || ?", Vars: []any{string(*p), "x"}}
}

type chunkValueOfPtrValuer struct {
	A string
	P ptrValued
}

type chunkPtrToPtrValuer struct {
	A string
	P *ptrValued
}

// chunkStamped has an update-time column: gorm binds its value into an UpdateAll's DO UPDATE.
type chunkStamped struct {
	A         string
	B         string
	UpdatedAt time.Time
}

func chunkDB(t *testing.T, cfg *gorm.Config) *gorm.DB {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = logger.Discard
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), cfg)
	require.NoError(t, err)
	return db
}

func TestRowsPerInsert(t *testing.T) {
	db := chunkDB(t, &gorm.Config{})
	for _, tc := range []struct {
		name string
		rows any
		want int
	}{
		{"three columns", chunkThree{}, 21845},
		{"a pointer to it", &chunkThree{}, 21845},
		{"a slice of it", []chunkThree{}, 21845},
		{"a pointer to a slice of pointers to it", &[]*chunkThree{}, 21845},
		// The embedded tenant column counts once, like any other.
		{"an embedded tenant column", []*chunkScoped{}, 21845},
		// A column gorm will not insert binds nothing.
		{"a read-only column", []chunkReadOnly{}, 21845},
		{"an ignored field", []chunkIgnored{}, 21845},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := RowsPerInsert(db, tc.rows)
			require.NoError(t, err)
			assert.Equal(t, tc.want, n)
		})
	}

	t.Run("no columns", func(t *testing.T) {
		_, err := RowsPerInsert(db, []chunkEmpty{})
		assert.ErrorIs(t, err, ErrRowWidthUnknown)
	})
	t.Run("a column that binds an expression", func(t *testing.T) {
		_, err := RowsPerInsert(db, []chunkJSON{})
		assert.ErrorIs(t, err, ErrRowWidthUnknown)
	})
	t.Run("a pointer column whose type binds an expression", func(t *testing.T) {
		_, err := RowsPerInsert(db, []chunkPtrToPtrValuer{})
		assert.ErrorIs(t, err, ErrRowWidthUnknown)
	})
	// Only a column gorm itself treats as a gorm.Valuer is refused. A value column whose type
	// has GormValue on its pointer is not one, so it is counted like any other.
	t.Run("a value column whose pointer binds an expression", func(t *testing.T) {
		n, err := RowsPerInsert(db, []chunkValueOfPtrValuer{})
		require.NoError(t, err)
		assert.Equal(t, 32767, n)
	})
}

// What gorm actually binds for the two ptrValued columns, which is what the RowsPerInsert
// cases above rest on: one parameter a row for the value column, the expression's two for the
// pointer column.
func TestGormBindsAPointerReceiverValuerOnlyThroughAPointer(t *testing.T) {
	db := chunkDB(t, &gorm.Config{DryRun: true})
	p := ptrValued("p")

	stmt := db.Create(&[]chunkValueOfPtrValuer{{A: "a", P: p}, {A: "b", P: p}}).Statement
	assert.Len(t, stmt.Vars, 4, "a value column must bind one parameter a row: %s", stmt.SQL.String())

	stmt = db.Create(&[]chunkPtrToPtrValuer{{A: "a", P: &p}, {A: "b", P: &p}}).Statement
	assert.Len(t, stmt.Vars, 6, "a pointer column must bind its expression: %s", stmt.SQL.String())
}

// An ON CONFLICT clause is bound once per statement. A DO NOTHING arbiter, or a DO UPDATE
// from the excluded row, binds nothing; a DO UPDATE to a literal binds the literal, which
// comes off the limit before it is divided.
func TestRowsPerInsertLeavesRoomForTheConflictClause(t *testing.T) {
	db := chunkDB(t, &gorm.Config{})
	target := []clause.Column{{Name: "a"}}
	for _, tc := range []struct {
		name string
		c    clause.OnConflict
		want int
	}{
		{"do nothing", clause.OnConflict{Columns: target, DoNothing: true}, 21845},
		{"update from the excluded row", clause.OnConflict{Columns: target,
			DoUpdates: clause.AssignmentColumns([]string{"b"})}, 21845},
		{"update to one literal", clause.OnConflict{Columns: target,
			DoUpdates: clause.Assignments(map[string]any{"b": "x"})}, 21844},
		{"update to two literals", clause.OnConflict{Columns: target,
			DoUpdates: clause.Assignments(map[string]any{"b": "x", "c": 7})}, 21844},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := RowsPerInsert(db.Clauses(tc.c), []chunkThree{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, n)
		})
	}
}

// The count is checked against what gorm actually binds, not against the column count it
// was derived from: RowsPerInsert rows fit in one statement, and one more row would not.
func TestRowsPerInsertIsWhatOneStatementBinds(t *testing.T) {
	db := chunkDB(t, &gorm.Config{DryRun: true})
	target := []clause.Column{{Name: "a"}}
	for _, tc := range []struct {
		name string
		c    clause.OnConflict
	}{
		{"do nothing", clause.OnConflict{Columns: target, DoNothing: true}},
		{"update to one literal", clause.OnConflict{Columns: target,
			DoUpdates: clause.Assignments(map[string]any{"b": "x"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := RowsPerInsert(db.Clauses(tc.c), []chunkThree{})
			require.NoError(t, err)
			bound := func(rows int) int {
				batch := make([]chunkThree, rows)
				for i := range batch {
					batch[i] = chunkThree{A: fmt.Sprint(i), B: "b", C: i + 1}
				}
				stmt := db.Clauses(tc.c).Create(&batch).Statement
				return len(stmt.Vars)
			}
			assert.LessOrEqual(t, bound(n), MaxBindParameters, "RowsPerInsert rows must fit in one statement")
			assert.Greater(t, bound(n+1), MaxBindParameters, "one row more must not fit, or the bound is loose")
		})
	}
}

// An ON CONFLICT … UpdateAll on a model with an update-time column is refused: gorm adds a bound
// assignment of that column when the statement runs, after the count is taken. The control
// shows that the count it would otherwise give binds one parameter too many.
func TestRowsPerInsertRefusesUpdateAllOverAnUpdateTimeColumn(t *testing.T) {
	updateAll := clause.OnConflict{Columns: []clause.Column{{Name: "a"}}, UpdateAll: true}

	// Control: three insertable columns, so 65535/3 = 21845 rows by the column count — and
	// gorm binds 21845×3 + 1 parameters for them under UpdateAll.
	dry := chunkDB(t, &gorm.Config{DryRun: true})
	batch := make([]chunkStamped, 21845)
	for i := range batch {
		batch[i] = chunkStamped{A: fmt.Sprint(i), B: "b", UpdatedAt: time.Unix(1, 0)}
	}
	stmt := dry.Clauses(updateAll).Create(&batch).Statement
	require.Len(t, stmt.Vars, MaxBindParameters+1, "the control no longer over-binds")

	db := chunkDB(t, &gorm.Config{})
	n, err := RowsPerInsert(db.Clauses(updateAll), []chunkStamped{})
	require.ErrorIs(t, err, ErrRowWidthUnknown, "counted %d rows", n)

	require.NoError(t, db.AutoMigrate(&chunkStamped{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uix_chunk_stampeds_a ON chunk_stampeds (a)`).Error)
	rows := []chunkStamped{{A: "1", B: "x"}, {A: "2", B: "y"}}
	require.ErrorIs(t, CreateChunked(db.Clauses(updateAll), &rows).Error, ErrRowWidthUnknown)
	var written int64
	require.NoError(t, db.Model(&chunkStamped{}).Count(&written).Error)
	assert.Equal(t, int64(0), written, "a refused insert wrote rows")
}

// UpdateAll on a model with NO update-time column binds nothing beyond the rows, so it is
// counted, not refused; so is a named DoUpdates over a model that has one.
func TestRowsPerInsertCountsUpdateAllWithNoUpdateTimeColumn(t *testing.T) {
	db := chunkDB(t, &gorm.Config{})
	target := []clause.Column{{Name: "a"}}
	n, err := RowsPerInsert(db.Clauses(clause.OnConflict{Columns: target, UpdateAll: true}), []chunkThree{})
	require.NoError(t, err)
	assert.Equal(t, 21845, n)

	n, err = RowsPerInsert(db.Clauses(clause.OnConflict{Columns: target,
		DoUpdates: clause.AssignmentColumns([]string{"b", "updated_at"})}), []chunkStamped{})
	require.NoError(t, err)
	assert.Equal(t, 21845, n)
}

// A model whose width cannot be bounded is refused before anything is written.
func TestCreateChunkedRefusesAnUnboundedModel(t *testing.T) {
	db := chunkDB(t, &gorm.Config{})
	require.NoError(t, db.AutoMigrate(&chunkJSON{}))
	rows := []chunkJSON{{A: "a", Doc: datatypes.JSON(`{}`)}, {A: "b", Doc: datatypes.JSON(`{}`)}}

	err := CreateChunked(db, &rows).Error
	require.ErrorIs(t, err, ErrRowWidthUnknown)

	var n int64
	require.NoError(t, db.Model(&chunkJSON{}).Count(&n).Error)
	assert.Equal(t, int64(0), n, "a refused insert wrote rows")
}

// Up to one statement's worth, CreateChunked is a plain Create: the rows are written, the
// clauses on the handle apply, and RowsAffected counts what the arbiter let through.
func TestCreateChunkedWritesRowsAndKeepsTheArbiter(t *testing.T) {
	db := chunkDB(t, &gorm.Config{})
	require.NoError(t, db.AutoMigrate(&chunkThree{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uix_chunk_threes_a ON chunk_threes (a)`).Error)
	arbiter := clause.OnConflict{Columns: []clause.Column{{Name: "a"}}, DoNothing: true}

	rows := []chunkThree{{A: "1", B: "x", C: 1}, {A: "2", B: "y", C: 2}}
	res := CreateChunked(db.Clauses(arbiter), &rows)
	require.NoError(t, res.Error)
	assert.Equal(t, int64(2), res.RowsAffected)

	again := []chunkThree{{A: "1", B: "x", C: 1}, {A: "3", B: "z", C: 3}}
	res = CreateChunked(db.Clauses(arbiter), &again)
	require.NoError(t, res.Error)
	assert.Equal(t, int64(1), res.RowsAffected, "the arbiter must drop the row already written")

	var n int64
	require.NoError(t, db.Model(&chunkThree{}).Count(&n).Error)
	assert.Equal(t, int64(3), n)
}

func TestIsStatementTooLarge(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"a cancelled context", context.Canceled, false},
		{"a value the server refused", &pgconn.PgError{Code: "22003", Message: "numeric field overflow"}, false},
		{"some other error", errors.New("connection reset by peer"), false},
		{"the driver's refusal, wrapped", fmt.Errorf("insert: %w",
			errors.New("extended protocol limited to 65535 parameters")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsStatementTooLarge(tc.err))
		})
	}
}

func TestIsEncodeRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"a cancelled context", context.Canceled, false},
		{"a value the server refused", &pgconn.PgError{Code: "22003", Message: "numeric field overflow"}, false},
		{"the size refusal", errors.New("extended protocol limited to 65535 parameters"), false},
		{"some other error", errors.New("connection reset by peer"), false},
		{"the driver's refusal, wrapped", fmt.Errorf("insert: %w", errors.New(
			"failed to encode args[2]: unable to encode 0xffffffffffffffff into binary format for int8 (OID 20)")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsEncodeRefusal(tc.err))
		})
	}
}
