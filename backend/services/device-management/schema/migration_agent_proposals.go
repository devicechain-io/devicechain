// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// agentProposal is the SNAPSHOT of the agent-proposal ledger row as of this migration. It is not
// model.AgentProposal (nor core/actiongate.Record, which that model embeds) and must never be
// replaced by either: a migration's shapes are a point in time, and pointing one at a live model
// silently rewrites an already-applied migration whenever that model changes, which breaks FRESH
// installs while every existing database applies cleanly and looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped) and the embedded ledger columns are INLINED, matching
// every other shape in this package, and field order matches the live layout because physical
// column order is part of the schema. There is no TableName(), for the reason maintenanceWindow
// records.
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

// NewAgentProposalsSchema adds the agent-proposal ledger: one row per action an AI agent asked
// for in this area (an alarm acknowledge or clear in this release), with its canonical payload,
// frozen targets, digest, proposer, decision and approvals.
//
// This is an APPENDED migration; the baseline is frozen. It creates a new table and touches no
// existing one, so old pods running beside new ones during a rolling upgrade never see it.
//
// Two unique indexes, both over live rows:
//
//   - (tenant_id, token): a proposal token names one row;
//   - (tenant_id, proposer_client, proposer_sub, request_id): an agent's idempotency key names one
//     proposal of that client and principal, so a retried call finds the original rather than
//     making a second, and two racing calls with the same key cannot both land.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. The table is
// AutoMigrate (a no-op when present) and both indexes are IF NOT EXISTS.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it directly with no
// exemption to register. Its personal fields (proposer_sub, the approvers in approvals,
// decided_by, rationale) go with the rows.
func NewAgentProposalsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261125090000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&agentProposal{}); err != nil {
				return err
			}
			if err := createTenantTokenIndex(tx, &agentProposal{}); err != nil {
				return err
			}
			return createPartialUniqueIndexWhere(tx, &agentProposal{}, "uix_agent_proposals_request",
				"deleted_at IS NULL", "tenant_id", "proposer_client", "proposer_sub", "request_id")
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"agent_proposals"})
		},
	}
}
