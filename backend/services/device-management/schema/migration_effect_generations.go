// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Literal snapshot owned by this migration, without live models or mixins.
type staticMembershipGeneration struct {
	ID         uint   `gorm:"primaryKey"`
	TenantId   string `gorm:"not null;size:128;uniqueIndex:uix_static_membership_generation,priority:1"`
	GroupId    uint   `gorm:"type:bigint;not null;uniqueIndex:uix_static_membership_generation,priority:2"`
	MemberType string `gorm:"not null;size:32;uniqueIndex:uix_static_membership_generation,priority:3"`
	MemberId   uint   `gorm:"type:bigint;not null;uniqueIndex:uix_static_membership_generation,priority:4"`
	Generation int64  `gorm:"not null"`
}

// NewEffectGenerationsSchema expands storage only. Nullable columns need no
// backfill or new input from old pods. Each step tolerates replay independently
// after partial failure with UseTransaction:false. No baseline is changed.
func NewEffectGenerationsSchema() *gormigrate.Migration {
	// Local column snapshots retain the area's naming strategy (including schema
	// prefix), without borrowing a previous migration's or a live model's shape.
	type device struct{ StateGeneration sql.NullInt64 }
	type entityRelationship struct{ MembershipGeneration sql.NullInt64 }
	return &gormigrate.Migration{ID: "20261229120000", Migrate: func(tx *gorm.DB) error {
		for _, column := range []struct {
			model any
			field string
		}{{&device{}, "StateGeneration"}, {&entityRelationship{}, "MembershipGeneration"}} {
			if !tx.Migrator().HasColumn(column.model, column.field) {
				if err := tx.Migrator().AddColumn(column.model, column.field); err != nil {
					return err
				}
			}
		}
		return tx.AutoMigrate(&staticMembershipGeneration{})
	}, Rollback: func(*gorm.DB) error {
		return errors.New("effect generations cannot be rolled back during rolling upgrades")
	}}
}
