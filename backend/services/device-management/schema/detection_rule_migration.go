// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewDetectionRuleSchema adds the detection_rules table (ADR-051 / ADR-053 §5 / ADR-045):
// DETECT rules declared on a device profile, structurally parallel to metric_definitions,
// command_definitions, and alarm_definitions. The rule body is stored as an opaque JSON
// document (the event-processing rules.Rule schema) — device-management versions it but
// does not parse the taxonomy (that stays single-homed in event-processing). Authored
// directly against device_profile_id (the definitions already live on the profile since
// ADR-045 slice b; there is no type→profile re-parent cutover to mirror). The snapshot is
// defined inline so the migration is frozen against future model changes; it must produce
// the same columns as model.DetectionRule.
func NewDetectionRuleSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260709120000",
		Migrate: func(tx *gorm.DB) error {
			type DetectionRule struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				DeviceProfileId uint           `gorm:"not null;index"`
				Definition      datatypes.JSON `gorm:"not null"`
				Enabled         bool           `gorm:"not null;default:true"`
			}
			if err := tx.AutoMigrate(&DetectionRule{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &DetectionRule{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("detection_rules")
		},
	}
}
