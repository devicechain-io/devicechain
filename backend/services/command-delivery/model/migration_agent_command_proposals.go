// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"time"
)

// These are migration-local snapshots, with mixins inlined and the device-management
// ledger's exact column order, sizes and nullability. Never replace them with live models.
type agentProposal struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token             string         `gorm:"size:64;not null;index"`
	RequestId         string         `gorm:"size:128;not null"`
	EffectToken       string         `gorm:"size:64;not null"`
	Kind              string         `gorm:"size:32;not null"`
	State             string         `gorm:"size:16;not null;index"`
	Payload           datatypes.JSON `gorm:"not null"`
	TargetSet         datatypes.JSON `gorm:"not null"`
	Dependencies      datatypes.JSON `gorm:"not null"`
	Digest            string         `gorm:"size:80;not null"`
	Impact            string         `gorm:"size:16;not null"`
	TargetCount       int            `gorm:"not null"`
	ProposerSub       string         `gorm:"size:320;not null;index"`
	ProposerClient    string         `gorm:"size:255;not null;index"`
	ClientAttested    bool           `gorm:"not null"`
	ProposerSid       sql.NullString `gorm:"size:64"`
	Provenance        *datatypes.JSON
	Rationale         sql.NullString `gorm:"size:2048"`
	PolicyVersion     int            `gorm:"not null"`
	RequiredApprovals int            `gorm:"not null"`
	Approvals         datatypes.JSON `gorm:"not null"`
	Revision          int            `gorm:"not null"`
	Reasons           datatypes.JSON `gorm:"not null"`
	ExpiresAt         time.Time      `gorm:"not null;index"`
	ResultRef         sql.NullString `gorm:"size:128"`
	DecidedAt         sql.NullTime
	DecidedBy         sql.NullString `gorm:"size:320"`
	Note              sql.NullString `gorm:"size:512"`
	ExecutedAt        sql.NullTime
}

type agentActuationFence struct {
	TenantId           string    `gorm:"primaryKey;not null;size:128"`
	Enabled            bool      `gorm:"not null"`
	KilledAtGeneration int       `gorm:"not null"`
	Epoch              int64     `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

// NewAgentCommandProposalsSchema expands storage only; it creates no proposals or commands.
// Every statement is re-runnable with UseTransaction:false. Tables carry tenant_id,
// so the catalog-driven tenant purge needs no exemption or hand-maintained registration.
func NewAgentCommandProposalsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261121120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&agentProposal{}, &agentActuationFence{}); err != nil {
				return err
			}
			for _, ddl := range []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_agent_proposals_tenant_token ON "command-delivery".agent_proposals (tenant_id, token) WHERE deleted_at IS NULL;`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_agent_proposals_request ON "command-delivery".agent_proposals (tenant_id, proposer_client, proposer_sub, request_id) WHERE deleted_at IS NULL;`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS proposal_token varchar(64);`,
				// No deleted_at predicate: soft deletion can never admit a second effect for a proposal.
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_commands_tenant_proposal ON "command-delivery".commands (tenant_id, proposal_token) WHERE proposal_token IS NOT NULL;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, ddl := range []string{
				`DROP INDEX IF EXISTS "command-delivery".uix_commands_tenant_proposal;`,
				`ALTER TABLE "command-delivery".commands DROP COLUMN IF EXISTS proposal_token;`,
				`DROP TABLE IF EXISTS "command-delivery".agent_actuation_fences;`,
				`DROP TABLE IF EXISTS "command-delivery".agent_proposals;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
