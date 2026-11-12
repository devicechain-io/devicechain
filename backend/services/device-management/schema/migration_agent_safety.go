// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// These are this migration's snapshots. Both carry tenant_id and are erased by
// the catalog-driven tenant purge without an exemption.
type agentActuationFence struct {
	TenantId           string    `gorm:"primaryKey;not null;size:128"`
	Enabled            bool      `gorm:"not null"`
	KilledAtGeneration int       `gorm:"not null"`
	Epoch              int64     `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}
type agentEffectOutbox struct {
	Id            string         `gorm:"primaryKey;size:32"`
	TenantId      string         `gorm:"not null;size:128"`
	ProposalToken string         `gorm:"not null;size:128"`
	Kind          string         `gorm:"not null;size:32"`
	Payload       datatypes.JSON `gorm:"not null"`
	CreatedAt     time.Time      `gorm:"not null;index"`
	Attempts      int            `gorm:"not null"`
	LastError     *string        `gorm:"type:text"`
}

// NewAgentSafetySchema is appended after the released chain. AutoMigrate makes
// each table/index creation safe to repeat after a partially applied upgrade.
func NewAgentSafetySchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261110000100",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&agentActuationFence{}, &agentEffectOutbox{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"agent_effect_outboxes", "agent_actuation_fences"})
		},
	}
}
