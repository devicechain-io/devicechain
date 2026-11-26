// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// fleetEventLockOrder locks existing journal rows after the caller's ordered
// line locks. Resolution notes can be shared by disjoint sets of device lines,
// so device and line locks alone do not serialize their mutation. An ordered
// IN list does not order a bulk UPDATE/DELETE's row locks: execute this SELECT
// in the mutation's transaction first, without skipping any matching events.
func fleetEventLockOrder(db *gorm.DB) *gorm.DB {
	return db.Order("id").Clauses(clause.Locking{Strength: "UPDATE"})
}
