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
	return &gormigrate.Migration{
		ID: "20260712200000",
		Migrate: func(tx *gorm.DB) error {
			type DetectionRule struct {
				AuthoringGraph datatypes.JSON
			}
			return tx.AutoMigrate(&DetectionRule{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn("detection_rules", "authoring_graph")
		},
	}
}
