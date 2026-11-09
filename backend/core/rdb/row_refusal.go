// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// rowReturnsRefusal makes a Row() read that a callback refused (no tenant, a wrong tenant, an
// unscoped statement) hand back a *sql.Row that carries the refusal.
//
// 🔴 WHY: gorm's row callback skips on a handle that already carries an error, so Row() finds
// no *sql.Row in the statement and returns nil, and Scan on a nil *sql.Row is a nil-pointer
// panic. Every refusal the scoping callbacks raise with AddError therefore became a panic on
// the Row() path while the same refusal on Find, Update or Delete came back as an error.
// This runs after gorm:row, which is the only place left to put a row: it reads the error
// the earlier callbacks recorded and builds a row that returns it from Scan.
func rowReturnsRefusal(db *gorm.DB) {
	if db.Error == nil {
		return
	}
	// Rows() reads through the same chain but hands back a *sql.Rows, which nobody here builds.
	if rows, _ := db.Get("rows"); rows == true {
		return
	}
	// A *sql.Row already in Dest counts only if gorm:row put it there in THIS execution. On a
	// reused handle Dest still holds the previous, already scanned, row; handing that back would
	// make Scan report sql.ErrNoRows instead of the refusal.
	if row, ok := db.Statement.Dest.(*sql.Row); ok {
		if stale, _ := db.Statement.Settings.Load(staleRowKey); stale != row {
			return
		}
	}
	// WithoutCancel: no I/O happens here, and a cancelled context must not hide the refusal.
	ctx := context.Background()
	if db.Statement.Context != nil {
		ctx = context.WithoutCancel(db.Statement.Context)
	}
	db.Statement.Dest = refusingPool{err: db.Error}.QueryRowContext(ctx, "")
}

// staleRowKey holds the *sql.Row (if any) the statement carried when this execution began.
const staleRowKey = "dc:row_stale"

// rememberStaleRow runs first on the Row chain and notes any row left over from a prior execution.
func rememberStaleRow(db *gorm.DB) {
	if row, ok := db.Statement.Dest.(*sql.Row); ok {
		db.Statement.Settings.Store(staleRowKey, row)
	} else {
		db.Statement.Settings.Delete(staleRowKey)
	}
}
