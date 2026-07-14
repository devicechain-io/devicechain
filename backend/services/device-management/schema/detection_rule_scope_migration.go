// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDetectionRuleScopeRefSchema creates the detection_rule_scope_refs table (ADR-062 S4):
// the reference-counting join between a LIVE (published, enabled) detection rule and the
// dynamic entity-group version it scopes to. The membership read-model for a group@v is
// materialized while any row references it and GC'd when the last one goes; the rows are
// re-synced when a profile's active version changes and torn down on profile delete. A
// dedicated table (rather than scanning published-version snapshot JSON) makes the refcount
// and the DeleteEntityGroup guard O(1) indexed lookups.
func NewDetectionRuleScopeRefSchema() *gormigrate.Migration {
	type DetectionRuleScopeRef struct {
		gorm.Model
		rdb.TenantScoped
		DeviceProfileId uint   `gorm:"not null;uniqueIndex:uix_dr_scope_ref,priority:1"`
		RuleToken       string `gorm:"not null;size:255;uniqueIndex:uix_dr_scope_ref,priority:2"`
		GroupToken      string `gorm:"not null;size:255;index:idx_dr_scope_ref_group,priority:1"`
		GroupVersion    int32  `gorm:"not null;index:idx_dr_scope_ref_group,priority:2"`
	}
	return &gormigrate.Migration{
		ID: "20260714180000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&DetectionRuleScopeRef{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&DetectionRuleScopeRef{})
		},
	}
}

// NewDetectionRuleScopeSchema adds the nullable entity_group_token / entity_group_version
// columns to detection_rules (ADR-062 S4): the OPTIONAL scope pinning a rule to one
// published dynamic entity-group version, so the rule fires only for events whose resolved
// entity is a member of {token}@{version} (stamped into ResolvedEvent.ScopeMemberships at
// resolution). Both are nullable — an unscoped rule (the profile-wide default) leaves them
// NULL — and set together (validated in the model layer). The migration is frozen against
// future model changes: the inline struct declares only the columns this slice adds, so
// AutoMigrate adds the two missing columns and leaves the rest untouched (the established
// additive-column pattern, mirroring detection_rule_authoring_graph_migration.go).
func NewDetectionRuleScopeSchema() *gormigrate.Migration {
	// Frozen snapshot: gorm.Model + the two new nullable columns, mapping to the existing
	// "detection_rules" table. Pointer types so the columns are nullable (unscoped ⇒ NULL).
	type DetectionRule struct {
		gorm.Model
		EntityGroupToken   *string
		EntityGroupVersion *int32
	}
	return &gormigrate.Migration{
		ID: "20260714170000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&DetectionRule{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{"EntityGroupToken", "EntityGroupVersion"} {
				if tx.Migrator().HasColumn(&DetectionRule{}, col) {
					if err := tx.Migrator().DropColumn(&DetectionRule{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}
