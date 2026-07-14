// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDetectRuleScopeSchema adds the entity_group_token / entity_group_version columns to the
// durable detection-rule projection (ADR-062 S4): a rule's optional group scope. It MUST live
// in the projection, because the engine rebuilds its rule set from this table at restart — a
// scope threaded only from the live fact would be lost on reload, silently un-scoping every
// scoped rule (fires fleet-wide, never descopes) and breaking restart determinism. The columns
// are additive with safe defaults (empty token / version 0 = unscoped), so existing rows read
// back as the profile-wide rules they were. The migration-local struct declares only the two
// new columns (the established additive-column pattern).
func NewDetectRuleScopeSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260714190000",
		Migrate: func(tx *gorm.DB) error {
			type DetectRule struct {
				RuleId             string `gorm:"primaryKey;size:512;not null"`
				EntityGroupToken   string `gorm:"size:256;not null;default:''"`
				EntityGroupVersion int32  `gorm:"not null;default:0"`
			}
			return tx.AutoMigrate(&DetectRule{})
		},
		Rollback: func(tx *gorm.DB) error {
			type DetectRule struct {
				RuleId string `gorm:"primaryKey;size:512;not null"`
			}
			for _, col := range []string{"EntityGroupToken", "EntityGroupVersion"} {
				if tx.Migrator().HasColumn(&DetectRule{}, col) {
					if err := tx.Migrator().DropColumn(&DetectRule{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}
