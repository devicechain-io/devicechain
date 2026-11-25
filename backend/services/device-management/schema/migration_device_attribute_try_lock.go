// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceAttributeTryLockSchema adds the non-waiting form of the device attribute lock.
//
// The key is the SAME key dc_entity_attr_lock takes, and it is spelled once more here on purpose: a
// function cannot call another to share a key without becoming volatile twice over, and the lock
// function's own migration says a definition is never changed in place. TestTheTryLockTakesTheSameKey
// holds the two in step against a real server (a lock held through one is refused to the other), so a
// drift would fail there rather than let two writers think they own one device.
//
// It exists for one caller: a fleet operation's posting transaction must never queue ahead of an
// interactive attribute writer, so it asks for the lock and, if it is not free, rolls back and retries.
// An older pod never calls it. Re-runnable (CREATE OR REPLACE); PostgreSQL only.
func NewDeviceAttributeTryLockSchema() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "20261225120000", Migrate: func(tx *gorm.DB) error {
		if tx.Dialector.Name() != "postgres" {
			return nil
		}
		return tx.Exec(`CREATE OR REPLACE FUNCTION "device-management".dc_entity_attr_try_lock(p_type text, p_id bigint)
RETURNS boolean LANGUAGE sql VOLATILE AS $f$
  SELECT pg_try_advisory_xact_lock(hashtextextended('dc.attr|' || p_type || '|' || p_id::text, 0))
$f$;`).Error
	}, Rollback: func(*gorm.DB) error {
		return errors.New("the device attribute try-lock cannot be rolled back during rolling upgrades")
	}}
}
