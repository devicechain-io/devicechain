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
	// Under REPEATABLE READ or SERIALIZABLE an earlier statement has already fixed the snapshot, and the
	// catalog read below would be made against it: a DISABLE that committed after it would be invisible
	// even though this transaction holds the lock. The posting protocol runs at READ COMMITTED.
	var isolation string
	if err := tx.Raw(`SELECT current_setting('transaction_isolation')`).Scan(&isolation).Error; err != nil {
		return fmt.Errorf("read the isolation level for the activation check: %w", err)
	}
	if isolation != "read committed" {
		return fmt.Errorf("%w: the check needs READ COMMITTED, not %s", ErrFleetOperationNotActivated, isolation)
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
	// A default for the marker at the database or role level would mark EVERY writer, which is the guard
	// switched off without touching the trigger or the function.
	var defaults int64
	if err := tx.Raw(`SELECT count(*) FROM pg_catalog.pg_db_role_setting s
WHERE (s.setdatabase = 0 OR s.setdatabase = (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database()))
  AND EXISTS (SELECT 1 FROM unnest(s.setconfig) c WHERE c LIKE 'devicechain.attr_protocol=%')`).Scan(&defaults).Error; err != nil {
		return fmt.Errorf("read the database settings for the activation check: %w", err)
	}
	if defaults > 0 {
		return fmt.Errorf("%w: a default for devicechain.attr_protocol is set on the database or a role, which marks every writer", ErrFleetOperationNotActivated)
	}
	return nil
}
