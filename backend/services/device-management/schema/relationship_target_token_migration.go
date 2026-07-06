// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewRelationshipTargetTokenSchema denormalizes the target entity's stable
// per-tenant token onto entity_relationships (ADR-044 decision 4). The event
// resolver emits token-addressed anchors, and this column lets it read the target
// token straight off the tracked-relationship row (carried by the
// RelationshipsBySource cache) instead of an id→token lookup on the ingest hot
// path. Additive: AutoMigrate adds the column without touching existing ones.
//
// No backfill: the token re-key is a fresh-bring-up cutover (the event-management
// and device-state hypertables re-key in lockstep and can not be altered in place),
// so pre-existing rows are recreated rather than migrated (pre-GA, greenfield).
func NewRelationshipTargetTokenSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260706120000",
		Migrate: func(tx *gorm.DB) error {
			type EntityRelationship struct {
				gorm.Model
				TargetToken string `gorm:"size:128"`
			}
			return tx.AutoMigrate(&EntityRelationship{})
		},
		Rollback: func(tx *gorm.DB) error {
			type EntityRelationship struct {
				gorm.Model
				TargetToken string `gorm:"size:128"`
			}
			if tx.Migrator().HasColumn(&EntityRelationship{}, "TargetToken") {
				return tx.Migrator().DropColumn(&EntityRelationship{}, "TargetToken")
			}
			return nil
		},
	}
}
