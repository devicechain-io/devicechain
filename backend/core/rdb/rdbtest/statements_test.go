// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbtest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const testMarker = "calib_marker"

type calibMarker struct {
	ID uint `gorm:"primaryKey"`
}

func (calibMarker) TableName() string { return testMarker }

type calibOther struct {
	ID uint `gorm:"primaryKey"`
}

func (calibOther) TableName() string { return "calib_other" }

// openCounted opens a private in-memory SQLite database whose every statement goes
// through a fresh counter for testMarker.
func openCounted(t *testing.T) (*gorm.DB, *StatementCounter) {
	t.Helper()
	c := NewStatementCounter(testMarker)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: c})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&calibMarker{}, &calibOther{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, c
}

// readMarker and readOther are one statement each; only the first names the marker.
func readMarker(db *gorm.DB) error {
	var n int64
	return db.Model(&calibMarker{}).Count(&n).Error
}

func readOther(db *gorm.DB) error {
	var n int64
	return db.Model(&calibOther{}).Count(&n).Error
}

// assertDisarmedAndReset checks the contract Calibrate leaves behind: both totals zero,
// and a statement run afterwards is NOT counted.
func assertDisarmedAndReset(t *testing.T, db *gorm.DB, c *StatementCounter) {
	t.Helper()
	if all, marked := c.Counts(); all != 0 || marked != 0 {
		t.Fatalf("counts after Calibrate = (%d, %d), want (0, 0): the counter was not reset", all, marked)
	}
	if err := readMarker(db); err != nil {
		t.Fatalf("post-calibration read: %v", err)
	}
	if all, marked := c.Counts(); all != 0 || marked != 0 {
		t.Fatalf("counts after a post-calibration statement = (%d, %d), want (0, 0): the counter was left armed", all, marked)
	}
}

func TestCalibrateReportsStatementsPerOperationOnAMatch(t *testing.T) {
	db, c := openCounted(t)
	per, err := c.Calibrate(5, func(int) error {
		if err := readMarker(db); err != nil {
			return err
		}
		return readOther(db)
	}, 1)
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if per != 2 {
		t.Fatalf("stmts/op = %v, want 2", per)
	}
	assertDisarmedAndReset(t, db, c)
}

// The check the helper exists for: the "off" leg of a benchmark claims zero marked
// statements, and a callback that silently survived a Remove makes one per operation.
func TestCalibrateRefusesAMarkedCountThatIsNotTheClaim(t *testing.T) {
	cases := []struct {
		name       string
		op         func(db *gorm.DB, i int) error
		wantMarked int64
	}{
		{
			name:       "off leg still reads the marker",
			op:         func(db *gorm.DB, _ int) error { return readMarker(db) },
			wantMarked: 0,
		},
		{
			name:       "on leg reads it one time too few per op",
			op:         func(db *gorm.DB, _ int) error { return readMarker(db) },
			wantMarked: 2,
		},
		{
			name: "one op in the run is off by one",
			op: func(db *gorm.DB, i int) error {
				if i == 0 {
					return readOther(db)
				}
				return readMarker(db)
			},
			wantMarked: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, c := openCounted(t)
			per, err := c.Calibrate(4, func(i int) error { return tc.op(db, i) }, tc.wantMarked)
			if err == nil {
				t.Fatalf("Calibrate accepted a marked count that is not %d per op (stmts/op %v)", tc.wantMarked, per)
			}
			if !strings.Contains(err.Error(), "matching") {
				t.Fatalf("Calibrate failed for the wrong reason: %v", err)
			}
			assertDisarmedAndReset(t, db, c)
		})
	}
}

func TestCalibrateRefusesAFractionalStatementsPerOperation(t *testing.T) {
	db, c := openCounted(t)
	_, err := c.Calibrate(3, func(i int) error {
		if i == 0 {
			if err := readOther(db); err != nil {
				return err
			}
		}
		return readOther(db)
	}, 0)
	if err == nil || !strings.Contains(err.Error(), "whole number") {
		t.Fatalf("Calibrate over 4 statements in 3 ops: err = %v, want a whole-number refusal", err)
	}
	assertDisarmedAndReset(t, db, c)
}

func TestCalibrateRefusesNoOperations(t *testing.T) {
	for _, ops := range []int{0, -1} {
		c := NewStatementCounter(testMarker)
		called := false
		if _, err := c.Calibrate(ops, func(int) error { called = true; return nil }, 0); err == nil {
			t.Fatalf("Calibrate(%d ops) returned no error", ops)
		}
		if called {
			t.Fatalf("Calibrate(%d ops) ran the operation", ops)
		}
	}
}

func TestCalibratePropagatesAnOperationFailure(t *testing.T) {
	db, c := openCounted(t)
	boom := errors.New("boom")
	_, err := c.Calibrate(3, func(i int) error {
		if i == 1 {
			return boom
		}
		return readMarker(db)
	}, 1)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the operation's failure", err)
	}
	assertDisarmedAndReset(t, db, c)
}
