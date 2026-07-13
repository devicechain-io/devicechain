// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewDetectionRuleAuthoringGraphSchema adds the nullable authoring_graph column to
// detection_rules (ADR-053 slice 9b): the opaque visual-canvas CanvasDefinition document the
// console editor round-trips so a canvas-authored rule re-opens on the canvas. It is authoring
// metadata only — device-management never parses it and never freezes it into the publish
// snapshot (the runtime consumes only definition). Nullable: a form-authored rule has none.
// The migration is frozen against future model changes, so the inline struct declares only the
// columns this slice adds (AutoMigrate adds the missing column, leaving the rest untouched).
func NewDetectionRuleAuthoringGraphSchema() *gormigrate.Migration {
	// Frozen snapshot: gorm.Model + the new column. The name maps to the existing
	// "detection_rules" table; a typed struct (not a string table name) is passed to the
	// migrator so both AutoMigrate and the rollback DropColumn resolve the schema — the
	// established additive-column pattern (see external_id_migration.go).
	type DetectionRule struct {
		gorm.Model
		AuthoringGraph datatypes.JSON
	}
	return &gormigrate.Migration{
		ID: "20260712200000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&DetectionRule{})
		},
		Rollback: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&DetectionRule{}, "AuthoringGraph") {
				return tx.Migrator().DropColumn(&DetectionRule{}, "AuthoringGraph")
			}
			return nil
		},
	}
}
