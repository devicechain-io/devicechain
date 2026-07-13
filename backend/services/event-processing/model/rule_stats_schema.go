// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewRuleStatsSchema adds the durable per-rule firing projection (ADR-051 slice 7b): one row
// per runtime rule id, upserted inline by the single-writer publish loop as detections are
// published, read by the console's rule-health view. A counting read-model, engine-inert.
func NewRuleStatsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260712193000",
		Migrate: func(tx *gorm.DB) error {
			type RuleStat struct {
				RuleId      string    `gorm:"primaryKey;size:512;not null"`
				Tenant      string    `gorm:"size:256;not null;index"`
				LastFiredAt time.Time `gorm:"not null"`
				FireCount   int64     `gorm:"not null"`
				LastEdge    string    `gorm:"size:16;not null"`
			}
			return tx.AutoMigrate(&RuleStat{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("rule_stats")
		},
	}
}
