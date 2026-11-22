// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Frozen migration snapshot. Never replace this with the live RuleStatSweepRecord model.
type ruleStatSweepsSchema struct {
	Tenant              string    `gorm:"column:tenant;type:varchar(256);primaryKey;not null"`
	ProfileVersionToken string    `gorm:"column:profile_version_token;type:varchar(256);primaryKey;not null"`
	SweptAt             time.Time `gorm:"column:swept_at;not null"`
}

func (ruleStatSweepsSchema) TableName() string { return "rule_stat_sweeps" }

// NewRuleStatSweeps adds the table in which the stranded rule-stat sweep records which profile
// versions it removed stats of. Additive: a replica older than it neither reads nor writes the
// table. AutoMigrate tolerates replay.
func NewRuleStatSweeps() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261122000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&ruleStatSweepsSchema{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&ruleStatSweepsSchema{})
		},
	}
}
