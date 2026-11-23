// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrFleetOperationNotActivated is returned by the activation check when the database does not
// enforce the device attribute writer protocol. It is the one answer to "may a fleet operation
// post right now", and the answer is no unless the guard is provably in place.
var ErrFleetOperationNotActivated = errors.New("fleet operations are not activated: the database does not enforce the device attribute writer protocol")

// CheckFleetOperationActivation is the check every fleet-operation posting transaction makes
// before its first write, inside that transaction.
//
// It first takes ROW EXCLUSIVE on the device attribute table, the lock the posting writes need
// anyway, and only then reads the catalog. The order is the point: ALTER TABLE ... DISABLE TRIGGER
// and DROP TRIGGER take SHARE ROW EXCLUSIVE, which conflicts with ROW EXCLUSIVE, so once this
// transaction holds the lock nobody can disable the guard until it ends. Reading first and locking
// second would leave a window in which the answer goes stale before the writes it permits.
//
// The catalog must show the expected trigger (name, function name AND body hash, BEFORE ROW on
// INSERT, UPDATE and DELETE, no WHEN condition) enabled ALWAYS. Anything else, an unreadable
// catalog included, fails closed. On a database other than PostgreSQL (the SQLite unit fixtures)
// there is no guard to find and the check refuses.
func CheckFleetOperationActivation(tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return errors.New("the fleet-operation activation check requires a transaction")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return errors.New("the fleet-operation activation check requires a transaction")
	}
	if tx.Dialector.Name() != "postgres" {
		return ErrFleetOperationNotActivated
	}
	if err := tx.Exec(`LOCK TABLE "device-management".entity_attributes IN ROW EXCLUSIVE MODE`).Error; err != nil {
		return fmt.Errorf("lock the device attribute table for the activation check: %w", err)
	}
	states, err := fleetOperationEnforcementStates(tx)
	if err != nil {
		return fmt.Errorf("read the catalog for the activation check: %w", err)
	}
	if fleetOperationEnforcementFromStates(states) != "ENFORCED" {
		return ErrFleetOperationNotActivated
	}
	return nil
}
