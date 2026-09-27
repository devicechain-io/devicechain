// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// core/integrity over REAL SQLite driver errors: a foreign-key, a check, a not-null and
// a trigger refusal. Each fixture asserts the driver's own code before anything is
// classified, so a fixture that stopped producing the violation fails here rather than
// letting a classification test pass over nothing.

// realIntegrityErrors provokes each refusal on one connection with foreign keys ON.
func realIntegrityErrors(t *testing.T) (fk, check, notNull, trigger error) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // the pragma and the tables must share one connection
	for _, ddl := range []string{
		`CREATE TABLE parents (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parents(id), qty INTEGER CHECK (qty >= 0))`,
		`CREATE TABLE gated (id INTEGER PRIMARY KEY)`,
		`CREATE TRIGGER gated_refuses BEFORE INSERT ON gated BEGIN SELECT RAISE(ABORT, 'no'); END`,
		`INSERT INTO parents (id) VALUES (1)`,
	} {
		require.NoError(t, db.Exec(ddl).Error, ddl)
	}
	fk = db.Exec(`INSERT INTO children (id, parent_id, qty) VALUES (1, 99, 1)`).Error
	check = db.Exec(`INSERT INTO children (id, parent_id, qty) VALUES (2, 1, -1)`).Error
	notNull = db.Exec(`INSERT INTO children (id, parent_id, qty) VALUES (3, NULL, 1)`).Error
	trigger = db.Exec(`INSERT INTO gated (id) VALUES (1)`).Error
	require.Equal(t, 787, errorCode(t, fk), "fixture must produce SQLITE_CONSTRAINT_FOREIGNKEY")
	require.Equal(t, 275, errorCode(t, check), "fixture must produce SQLITE_CONSTRAINT_CHECK")
	require.Equal(t, 1299, errorCode(t, notNull), "fixture must produce SQLITE_CONSTRAINT_NOTNULL")
	require.Equal(t, 1811, errorCode(t, trigger), "fixture must produce SQLITE_CONSTRAINT_TRIGGER")
	return fk, check, notNull, trigger
}

func classOf(t *testing.T, err error) integrity.Class {
	t.Helper()
	vs := integrity.All(fmt.Errorf("w: %w", err))
	require.Len(t, vs, 1, "a real SQLite constraint error must be recognised: %v", err)
	return vs[0].Class
}

func TestARealSQLiteForeignKeyViolationIsAReference(t *testing.T) {
	fk, _, _, _ := realIntegrityErrors(t)
	assert.Equal(t, integrity.ClassReference, classOf(t, fk))
	assert.False(t, conflict.Is(fk), "a foreign-key violation is never a conflict")
}

// Every other SQLite constraint code is Invalid, not only the two named ones: the
// classification fails closed.
func TestARealSQLiteCheckNotNullAndTriggerViolationAreInvalid(t *testing.T) {
	_, check, notNull, trigger := realIntegrityErrors(t)
	assert.Equal(t, integrity.ClassInvalid, classOf(t, check))
	assert.Equal(t, integrity.ClassInvalid, classOf(t, notNull))
	assert.Equal(t, integrity.ClassInvalid, classOf(t, trigger))
	assert.False(t, conflict.Is(check))
}

type childRoot struct{ err error }

func (r *childRoot) Ping() bool { return true }
func (r *childRoot) SaveChild() (bool, error) {
	return false, fmt.Errorf("save child: %w", r.err)
}

// A real SQLite foreign-key violation, through the schema every served endpoint is built
// from, reaches the caller with the code and none of the database's wording.
func TestARealSQLiteForeignKeyViolationIsAnsweredOnTheWire(t *testing.T) {
	fk, _, _, _ := realIntegrityErrors(t)
	s := gqlcore.MustParseSchema(`
		schema { query: Query mutation: Mutation }
		type Query { ping: Boolean! }
		type Mutation { saveChild: Boolean! }`, &childRoot{err: fk})
	resp := s.Exec(context.Background(), `mutation { saveChild }`, "", nil)
	require.Len(t, resp.Errors, 1)
	qe := resp.Errors[0]
	assert.Equal(t, "REFERENCE_VIOLATION", qe.Extensions["code"])
	assert.Equal(t, "save child: the request refers to a record that does not exist, "+
		"or removes one that other records still refer to", qe.Message)
	for _, leak := range []string{"FOREIGN KEY constraint failed", "787", "children"} {
		assert.False(t, strings.Contains(qe.Message, leak), "leaked %q: %s", leak, qe.Message)
	}
}
