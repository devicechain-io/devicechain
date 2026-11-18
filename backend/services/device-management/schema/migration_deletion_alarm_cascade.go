// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewDeletionAlarmCascadeSchema expands the outbox without changing old pods' writes.
// Existing rows have no cascade. HasColumn makes a partially applied upgrade replayable.
func NewDeletionAlarmCascadeSchema() *gormigrate.Migration {
	// This migration owns its table-named snapshot, independent of the live model.
	type pendingEntityDeletion struct {
		Cascade *datatypes.JSON
	}
	return &gormigrate.Migration{
		ID: "20261215130000",
		Migrate: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&pendingEntityDeletion{}, "Cascade") {
				return nil
			}
			if tx.Dialector.Name() == "sqlite" {
				type pendingEntityDeletion struct {
					Cascade *string `gorm:"type:text"`
				}
				return tx.Migrator().AddColumn(&pendingEntityDeletion{}, "Cascade")
			}
			return tx.Migrator().AddColumn(&pendingEntityDeletion{}, "Cascade")
		},
		Rollback: func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(&pendingEntityDeletion{}, "Cascade") {
				return nil
			}
			return tx.Migrator().DropColumn(&pendingEntityDeletion{}, "Cascade")
		},
	}
}
