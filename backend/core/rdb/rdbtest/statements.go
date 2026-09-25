// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdbtest

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm/logger"
)

// StatementCounter is a gorm logger that counts every statement gorm executes, and
// separately those whose SQL contains Marker. It exists so a harness can assert, by
// value, how many statements a write path costs and how many of them read one table.
//
// It does NOT import core/rdb, and that is deliberate: the caller passes the marker
// (rdb.FenceTable, for the erasure fence), so a test inside package rdb can use this
// package without an import cycle.
//
// What it counts is STATEMENTS, not round trips. gorm does not trace BEGIN or COMMIT —
// they run on the connection pool beneath the logger — so a caller that wants round
// trips has to add two per transaction itself, and has to know how many transactions
// its path opens. A statement that fails is still counted: a refused fence read is
// still a trip to the database.
//
// It starts ARMED. A disarmed counter returns from Trace without rendering the SQL,
// because rendering it (the fc callback, which runs the dialect's Explain) is CPU that
// must not land inside a timed benchmark loop.
type StatementCounter struct {
	// Marker is the substring that makes a statement "marked".
	Marker string

	armed  atomic.Bool
	all    atomic.Int64
	marked atomic.Int64
}

// NewStatementCounter returns an armed counter for marker.
func NewStatementCounter(marker string) *StatementCounter {
	c := &StatementCounter{Marker: marker}
	c.armed.Store(true)
	return c
}

// LogMode implements logger.Interface; the counter has no levels.
func (c *StatementCounter) LogMode(logger.LogLevel) logger.Interface { return c }

// Info implements logger.Interface and discards the message.
func (c *StatementCounter) Info(context.Context, string, ...any) {}

// Warn implements logger.Interface and discards the message.
func (c *StatementCounter) Warn(context.Context, string, ...any) {}

// Error implements logger.Interface and discards the message.
func (c *StatementCounter) Error(context.Context, string, ...any) {}

// Trace implements logger.Interface: gorm calls it once per executed statement.
func (c *StatementCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if !c.armed.Load() {
		return
	}
	c.all.Add(1)
	if sql, _ := fc(); c.Marker != "" && strings.Contains(sql, c.Marker) {
		c.marked.Add(1)
	}
}

// Arm turns counting on or off without resetting the totals.
func (c *StatementCounter) Arm(on bool) { c.armed.Store(on) }

// Reset zeroes both totals.
func (c *StatementCounter) Reset() {
	c.all.Store(0)
	c.marked.Store(0)
}

// Counts returns every statement counted since the last Reset, and the marked subset.
func (c *StatementCounter) Counts() (all, marked int64) {
	return c.all.Load(), c.marked.Load()
}

// Calibrate runs op ops times with the counter armed and reports the statements per
// operation. It FAILS unless the run made exactly ops × wantMarked marked statements, and
// a whole number of statements per operation — the check a harness runs BEFORE it times
// anything.
//
// 🔴 IT EXISTS FOR THE LEG THAT CLAIMS TO BE OFF. A benchmark comparing a write path with
// and without a callback removes the callback by name, and gorm's Remove of a name it
// does not know is SILENT: rename the callback and the "off" leg keeps it, measures the
// same cost as the "on" leg, and the comparison reads as "free". Asserting the marked
// count per operation on both legs — the expected value on one, zero on the other — is
// what turns that into a failure instead of a result.
//
// The counter is left disarmed and reset on return, so the caller can time a loop
// without paying for the SQL rendering.
func (c *StatementCounter) Calibrate(ops int, op func(i int) error, wantMarked int64) (stmtsPerOp float64, err error) {
	if ops <= 0 {
		return 0, fmt.Errorf("rdbtest: calibration needs at least one operation, got %d", ops)
	}
	c.Reset()
	c.Arm(true)
	defer func() {
		c.Arm(false)
		c.Reset()
	}()
	for i := 0; i < ops; i++ {
		if err := op(i); err != nil {
			return 0, fmt.Errorf("rdbtest: calibration operation %d failed: %w", i, err)
		}
	}
	all, marked := c.Counts()
	if marked != wantMarked*int64(ops) {
		return 0, fmt.Errorf("rdbtest: calibration counted %d statement(s) matching %q over %d operation(s); "+
			"want exactly %d per operation", marked, c.Marker, ops, wantMarked)
	}
	if all%int64(ops) != 0 {
		return 0, fmt.Errorf("rdbtest: calibration counted %d statement(s) over %d identical operation(s), "+
			"which is not a whole number per operation", all, ops)
	}
	return float64(all) / float64(ops), nil
}
