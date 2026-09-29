// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxBindParameters is the most parameters one statement can bind on the PostgreSQL wire
// protocol, whose Bind message counts them in an unsigned 16-bit field. pgx refuses a larger
// statement itself ("extended protocol limited to 65535 parameters"), so the refusal never
// reaches the server and never becomes a *pgconn.PgError. That holds on the path gorm takes
// through pgx's database/sql driver too, which prepares the statement first: pgx sizes the
// server's ParameterDescription from the message length rather than its wrapped count, so the
// argument count matches and the refusal is this one, raised by ExecPrepared.
const MaxBindParameters = math.MaxUint16

// ErrRowWidthUnknown is returned for a model whose parameters per row cannot be bounded by
// counting its columns: it has none, or a column's type binds an expression of its own
// (gorm.Valuer) rather than one value.
var ErrRowWidthUnknown = errors.New("cannot bound the parameters one row of this model binds")

var gormValuerType = reflect.TypeOf((*gorm.Valuer)(nil)).Elem()

// RowsPerInsert is how many rows of rows' element type one multi-row INSERT on db can carry
// without binding more than MaxBindParameters.
//
// It counts the columns gorm can insert for the model — the schema's database columns that
// are creatable, the same list gorm builds an INSERT's column list from — which is an upper
// bound on what one row binds, since a column with a default is left out when no row sets it.
// The parameters db's own ON CONFLICT clause binds (a DO UPDATE SET to a literal, say) are
// bound once per statement, and are taken off the limit before it is divided. rows may be a
// model, a pointer, or a slice or pointer to a slice of either.
//
// 🔴 A model with a column whose type implements gorm.Valuer is REFUSED with
// ErrRowWidthUnknown, and that includes every gorm.io/datatypes JSON type: GormValue returns
// an expression that can bind any number of parameters, so counting the column as one would
// be a guess, and a guess here is a statement the driver refuses on every retry. The event
// store's models have no such column.
func RowsPerInsert(db *gorm.DB, rows any) (int, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(rows); err != nil {
		return 0, err
	}
	cols := 0
	for _, name := range stmt.Schema.DBNames {
		f := stmt.Schema.FieldsByDBName[name]
		if f == nil || !f.Creatable {
			continue
		}
		if bindsExpression(f.FieldType) {
			return 0, fmt.Errorf("%w: %s.%s binds an expression", ErrRowWidthUnknown, stmt.Schema.Name, f.Name)
		}
		cols++
	}
	if cols == 0 {
		return 0, fmt.Errorf("%w: %s has no insertable columns", ErrRowWidthUnknown, stmt.Schema.Name)
	}
	fixed := onConflictVars(db)
	if fixed >= MaxBindParameters-cols {
		return 0, fmt.Errorf("%w: %s's ON CONFLICT clause binds %d parameters, leaving no room for a row",
			ErrRowWidthUnknown, stmt.Schema.Name, fixed)
	}
	return (MaxBindParameters - fixed) / cols, nil
}

// bindsExpression reports whether a column of type t hands gorm an expression to bind
// rather than one value.
func bindsExpression(t reflect.Type) bool {
	return t.Implements(gormValuerType) || reflect.PointerTo(t).Implements(gormValuerType)
}

// onConflictVars is how many parameters db's ON CONFLICT clause binds, counted by building it
// on a throwaway statement. It is the only clause of an INSERT, beyond its VALUES, that can
// bind any.
func onConflictVars(db *gorm.DB) int {
	c, ok := db.Statement.Clauses["ON CONFLICT"]
	if !ok {
		return 0
	}
	tmp := &gorm.Statement{DB: db, Clauses: map[string]clause.Clause{}}
	c.Build(tmp)
	return len(tmp.Vars)
}

// CreateChunked inserts rows as consecutive multi-row INSERTs of at most RowsPerInsert rows
// each, so no statement binds more than MaxBindParameters however many rows there are. Every
// clause already on db is kept, so an ON CONFLICT arbiter applies to every statement, and the
// create callbacks — tenant stamping, the erasure fence, the token grammar — run for each.
//
// Up to one statement's worth it is exactly db.Create(rows). Beyond that gorm runs the
// statements in one transaction, or under a SAVEPOINT when db already is one, so the rows are
// written all or nothing: a statement that fails rolls back to the savepoint and its error is
// returned. The result's RowsAffected is the sum over the statements.
//
// gorm never releases that savepoint, so each call that splits holds a subtransaction open
// until the enclosing transaction ends, and PostgreSQL slows every snapshot once one backend
// holds more than 64 of them. A caller that writes many rows in one transaction should make
// one call per table, not one per group of rows.
//
// A model RowsPerInsert cannot bound is refused with its error and no statement is sent.
func CreateChunked(db *gorm.DB, rows any) *gorm.DB {
	n, err := RowsPerInsert(db, rows)
	if err != nil {
		tx := db.Session(&gorm.Session{})
		_ = tx.AddError(err)
		return tx
	}
	return db.CreateInBatches(rows, n)
}

// IsStatementTooLarge reports whether err is the driver refusing a statement for binding more
// than MaxBindParameters. The same statement is refused every time it is sent, so a caller
// deciding whether to retry must treat it as permanent.
//
// pgx gives this refusal no type and no sentinel — pgconn builds it with fmt.Errorf — so its
// message is the only signal there is. statement_limit_integration_test.go provokes it at
// exactly MaxBindParameters+1 parameters on the real driver, so a pgx upgrade that rewords it
// fails there rather than silently turning this false.
func IsStatementTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), statementTooLargeText)
}

const statementTooLargeText = "extended protocol limited to"
