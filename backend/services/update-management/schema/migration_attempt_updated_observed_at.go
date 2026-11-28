// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// AttemptUpdatedObservedAtID is the attempt completion-observation migration's frozen ID.
const AttemptUpdatedObservedAtID = "20261128000100"

// NewAttemptUpdatedObservedAtSchema adds the observation time of the report that took an attempt to
// UPDATED: the broker's time, the same key installed versions are ordered by. A redelivered final
// report repairs the installed version with it, instead of the replica's receive time, which can run
// ahead of the broker and outrank a newer observation.
//
// Nullable, so previous-release pods keep writing attempts during a rolling upgrade, and nothing is
// backfilled: a row without it falls back to its last report time, as before. Its own snapshot
// survives replay, and the column check makes it re-runnable.
func NewAttemptUpdatedObservedAtSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: AttemptUpdatedObservedAtID,
		Migrate: func(tx *gorm.DB) error {
			type updateAttempt struct {
				ID                uint `gorm:"primarykey"`
				UpdatedObservedAt sql.NullTime
			}
			if tx.Migrator().HasColumn(&updateAttempt{}, "UpdatedObservedAt") {
				return nil
			}
			return tx.Migrator().AddColumn(&updateAttempt{}, "UpdatedObservedAt")
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
