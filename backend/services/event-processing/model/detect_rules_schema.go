// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDetectRulesSchema adds the durable detection-rule projection (ADR-051 slice 4b-3):
// the rule set the DETECT engine rebuilds from at startup, so a rule survives a restart
// independent of the finite-retention fact stream. One row per runtime rule id, upserted
// by the published-rule fact consumer before it acks the fact.
func NewDetectRulesSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260710000000",
		Migrate: func(tx *gorm.DB) error {
			type DetectRule struct {
				RuleId              string `gorm:"primaryKey;size:512;not null"`
				Tenant              string `gorm:"size:256;not null;index"`
				ProfileVersionToken string `gorm:"size:256;not null"`
				RuleToken           string `gorm:"size:256;not null"`
				Definition          string `gorm:"not null"`
				UpdatedAt           time.Time
			}
			return tx.AutoMigrate(&DetectRule{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("detect_rules")
		},
	}
}
