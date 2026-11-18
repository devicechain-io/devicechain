// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

type agentActuationGenerationSnapshot struct {
	ID                  uint `gorm:"primarykey"`
	ActuationGeneration int  `gorm:"not null;default:0"`
}

func (agentActuationGenerationSnapshot) TableName() string { return "iam_agent_policies" }

// NewAgentActuationGenerationMigration separates actuation transitions from policy publications.
func NewAgentActuationGenerationMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261110130000",
		Migrate: func(tx *gorm.DB) error {
			snapshot := &agentActuationGenerationSnapshot{}
			if tx.Migrator().HasColumn(snapshot, "actuation_generation") {
				return nil
			}
			return tx.Migrator().AddColumn(snapshot, "ActuationGeneration")
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&agentActuationGenerationSnapshot{}, "actuation_generation")
		},
	}
}
