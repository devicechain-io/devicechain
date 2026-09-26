// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// conflict.As over REAL SQLite driver errors. These live here rather than beside the
// classifier so that core/conflict's own tests import no database driver: that package is
// linked by dcctl, and a test import there would pull SQLite into dcctl's module graph.

// coded mimics the SQLite driver's error shape from the WRONG package.
type coded struct{}

func (coded) Error() string { return "UNIQUE constraint failed: x.y (2067)" }
func (coded) Code() int     { return 2067 }

// A Code() method on an unrelated error ABOVE the SQLite error must not stop the search,
// which is what errors.As with an interface target would do.
func TestTheSQLiteSearchIsNotStoppedByAnUnrelatedCodedError(t *testing.T) {
	sqliteDup := realSQLiteError(t, true)
	assert.True(t, conflict.Is(errors.Join(coded{}, sqliteDup)))
}

// realSQLiteError provokes a real error from the SQLite driver: a duplicate on a UNIQUE
// column, or (unique=false) a NOT NULL violation.
func realSQLiteError(t *testing.T, unique bool) error {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE widgets (token TEXT NOT NULL, name TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uix_widgets_token ON widgets (token)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO widgets (token, name) VALUES ('a', 'n')`).Error)
	if unique {
		err = db.Exec(`INSERT INTO widgets (token, name) VALUES ('a', 'n')`).Error
	} else {
		err = db.Exec(`INSERT INTO widgets (token, name) VALUES ('b', NULL)`).Error
	}
	require.Error(t, err)
	return err
}

// The SQLite branch depends on the driver's package path. This pins it against a REAL
// driver error, so a module rename fails here rather than classifying nothing.
func TestARealSQLiteDuplicateIsAConflictAndItsTypeLivesWhereTheClassifierLooks(t *testing.T) {
	err := realSQLiteError(t, true)
	c, ok := conflict.As(err)
	require.True(t, ok, "a real SQLite duplicate must be a conflict: %v", err)

	drv := c.Unwrap()
	require.NotNil(t, drv)
	typ := reflect.TypeOf(drv)
	require.Equal(t, reflect.Pointer, typ.Kind())
	assert.Equal(t, "github.com/glebarez/go-sqlite", typ.Elem().PkgPath())
	assert.Equal(t, 2067, drv.(interface{ Code() int }).Code())
}

func TestARealSQLiteNotNullViolationIsNotAConflict(t *testing.T) {
	err := realSQLiteError(t, false)
	assert.Equal(t, 1299, errorCode(t, err), "fixture must produce SQLITE_CONSTRAINT_NOTNULL")
	assert.False(t, conflict.Is(err))
}

func errorCode(t *testing.T, err error) int {
	t.Helper()
	var c interface{ Code() int }
	require.True(t, errors.As(err, &c))
	return c.Code()
}

func TestARealSQLiteDuplicateIsRedactedAtTheBoundary(t *testing.T) {
	err := fmt.Errorf("save: %w", realSQLiteError(t, true))
	got, _, changed := conflict.Redact(err.Error(), err)
	assert.Equal(t, "save: "+conflict.Message, got)
	assert.True(t, changed)
}

// A duplicate PRIMARY KEY is its own SQLite extended code (1555,
// SQLITE_CONSTRAINT_PRIMARYKEY), not the UNIQUE one (2067): it is a uniqueness conflict
// all the same, and the classifier must say so for both.
func TestARealSQLitePrimaryKeyDuplicateIsAConflict(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE gadgets (id TEXT PRIMARY KEY, name TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO gadgets (id, name) VALUES ('a', 'n')`).Error)
	err = db.Exec(`INSERT INTO gadgets (id, name) VALUES ('a', 'n')`).Error
	require.Error(t, err)
	require.Equal(t, 1555, errorCode(t, err), "fixture must produce SQLITE_CONSTRAINT_PRIMARYKEY")
	assert.True(t, conflict.Is(err))
}
