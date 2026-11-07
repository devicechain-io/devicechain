// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewInactivityAlarmTimeRestoreSchema repairs inactivity_alarm_time for the rows v1.1 pods
// left behind. v1.1 stopped writing the column (and served the deprecated inactivityAlarmTime
// field from last_disconnect_time instead); this release writes it again, as v1.0 did.
//
// Three kinds of row are wrong at that point, and nothing else is touched:
//
//   - an ACTIVE row with a value: v1.0 cleared it when the device came back, v1.1 did not, so
//     the value is a past episode and v1.0 would have shown null.
//   - a row whose value PREDATES its last connect: a v1.0 sweep stamped it, a v1.1 pod brought
//     the device back without clearing it, and a later v1.1 sweep left it alone. v1.0 stamps the
//     value after the last activity, so one older than last_connect_time belongs to a finished
//     episode and is cleared (the next statement may then refill it).
//   - an INACTIVE INFERRED row with no value but a last_disconnect_time that a sweep wrote: the
//     sweep stamps both columns with one instant, so the value is recovered from
//     last_disconnect_time. A disconnect report that is NOT followed by a sweep leaves
//     presence_time at or after last_disconnect_time (the report, then a demotion, both stamp
//     it), which is the shape v1.0 answered null for; those rows are excluded. A sweep after a
//     demotion moves last_disconnect_time past presence_time and is filled.
//
// Data only, no DDL. Each statement runs in batches of migrationBatch rows chosen with
// FOR UPDATE SKIP LOCKED, so it never waits for a row lock a v1.1 batch writer holds (the
// deadlock cycle documented at SweepInactive), and every statement removes the rows it matched
// from its own predicate, so the loop ends and a replay does the remaining work.
func NewInactivityAlarmTimeRestoreSchema() *gormigrate.Migration {
	const states = `"device-state".device_states`

	return &gormigrate.Migration{
		ID: "20261106120000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []struct{ set, where string }{
				{`inactivity_alarm_time = NULL`, `active AND inactivity_alarm_time IS NOT NULL`},
				{`inactivity_alarm_time = NULL`, `inactivity_alarm_time IS NOT NULL AND last_connect_time IS NOT NULL` +
					` AND inactivity_alarm_time < last_connect_time`},
				{`inactivity_alarm_time = last_disconnect_time`, `NOT active AND presence_source <> 'ASSERTED'` +
					` AND inactivity_alarm_time IS NULL AND last_disconnect_time IS NOT NULL` +
					` AND (presence_time IS NULL OR last_disconnect_time > presence_time)`},
			} {
				for {
					res := tx.Exec(`UPDATE ` + states + ` SET ` + stmt.set + ` WHERE id IN (SELECT id FROM ` + states +
						` WHERE ` + stmt.where + ` LIMIT ` + migrationBatch + ` FOR UPDATE SKIP LOCKED);`)
					if res.Error != nil {
						return res.Error
					}
					if res.RowsAffected == 0 {
						break
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}

// migrationBatch is the number of rows one statement of the restore touches.
const migrationBatch = "5000"
