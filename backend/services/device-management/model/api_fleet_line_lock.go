// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// fleetLineLockOrder acquires existing line locks in ascending ID order inside
// the caller's transaction, before any bulk mutation or journal INSERT. Merely
// sorting an IN subquery does not constrain an UPDATE/DELETE's execution order;
// the SELECT must actually lock the rows before that statement runs.
//
// Do not skip locked rows: erasure and abort must cover every matching line.
// Settlement's per-line CAS loop uses the same order across all its chunks.
func fleetLineLockOrder(db *gorm.DB) *gorm.DB {
	return db.Order("id").Clauses(clause.Locking{Strength: "UPDATE"})
}
