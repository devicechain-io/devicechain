// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// agentActuationOffSnapshot contains only this migration's columns and primary key.
type agentActuationOffSnapshot struct {
	ID                 uint `gorm:"primarykey"`
	TenantActuationOff bool `gorm:"not null;default:false"`
	ActuationOffAt     *time.Time
	ActuationOffBy     *string `gorm:"size:256"`
}

func (agentActuationOffSnapshot) TableName() string { return "iam_agent_policies" }

// NewAgentActuationOffMigration adds the document-independent tenant kill switch.
// AutoMigrate adds absent columns and is individually re-runnable.
func NewAgentActuationOffMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID:      "20261109130000",
		Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&agentActuationOffSnapshot{}) },
		Rollback: func(tx *gorm.DB) error {
			for _, c := range []string{"tenant_actuation_off", "actuation_off_at", "actuation_off_by"} {
				if err := tx.Migrator().DropColumn(&agentActuationOffSnapshot{}, c); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
