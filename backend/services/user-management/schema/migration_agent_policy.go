// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// agentPolicySnapshot is this migration's snapshot of iam_agent_policies: one row per tenant, the
// HEAD of its agent policy.
//
//   - tenant_id is the tenant's TOKEN, unique: one head per tenant, and what the tenant purge
//     sweeps the table by.
//   - draft is the working document (jsonb), NULL when there is none.
//   - published_version is the version in force, 0 when none has been published (actuation off).
//   - operator_suspended is the operator's per-tenant override, false for every tenant.
type agentPolicySnapshot struct {
	ID                uint `gorm:"primarykey"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	TenantId          string  `gorm:"not null;size:128;uniqueIndex:idx_iam_agent_policies_tenant"`
	Draft             *string `gorm:"type:jsonb"`
	DraftUpdatedAt    *time.Time
	DraftUpdatedBy    *string `gorm:"size:256"`
	PublishedVersion  int     `gorm:"not null;default:0"`
	OperatorSuspended bool    `gorm:"not null;default:false"`
	SuspendedAt       *time.Time
	SuspendedBy       *string `gorm:"size:256"`
}

func (agentPolicySnapshot) TableName() string { return "iam_agent_policies" }

// agentPolicyVersionSnapshot is one published version of a tenant's agent policy. Versions are
// append-only: a rollback publishes a NEW version holding an older document, so the version
// number only ever grows (the kill fence in each area compares against it).
type agentPolicyVersionSnapshot struct {
	ID          uint `gorm:"primarykey"`
	CreatedAt   time.Time
	TenantId    string `gorm:"not null;size:128;uniqueIndex:idx_iam_agent_policy_versions_tenant_version,priority:1"`
	Version     int    `gorm:"not null;uniqueIndex:idx_iam_agent_policy_versions_tenant_version,priority:2"`
	Document    string `gorm:"type:jsonb;not null"`
	Origin      string `gorm:"not null;size:32"`
	PublishedBy string `gorm:"not null;size:256"`
}

func (agentPolicyVersionSnapshot) TableName() string { return "iam_agent_policy_versions" }

// serviceAccountAgentSnapshot adds the immutable agent flag to iam_service_accounts: a flagged
// account's tokens carry proposal authorities in place of its roles' writes. False for every
// existing account. Only the new column is declared, so AutoMigrate adds it and touches nothing
// else.
type serviceAccountAgentSnapshot struct {
	ID    uint `gorm:"primarykey"`
	Agent bool `gorm:"not null;default:false"`
}

func (serviceAccountAgentSnapshot) TableName() string { return "iam_service_accounts" }

// NewAgentPolicyMigration creates the agent policy head and version tables and adds the service
// account agent flag. Individually re-runnable: AutoMigrate creates what is absent and converges
// nothing that is present. No seed rows: a tenant with no policy has actuation off.
func NewAgentPolicyMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261109120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&agentPolicySnapshot{}, &agentPolicyVersionSnapshot{}, &serviceAccountAgentSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn("iam_service_accounts", "agent"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("iam_agent_policy_versions", "iam_agent_policies")
		},
	}
}
