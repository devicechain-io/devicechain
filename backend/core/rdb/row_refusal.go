// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
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
	if _, ok := db.Statement.Dest.(*sql.Row); ok {
		return
	}
	db.Statement.Dest = refusingPool{err: db.Error}.QueryRowContext(db.Statement.Context, "")
}
