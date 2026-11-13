// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
)

type ReviewPolicySettings struct {
	Required            bool `json:"required"`
	RequiredApprovals   int  `json:"requiredApprovals"`
	AllowSelfApproval   bool `json:"allowSelfApproval"`
	ApprovalTtlSeconds  int  `json:"approvalTtlSeconds"`
	StepUpMaxAgeSeconds int  `json:"stepUpMaxAgeSeconds"`
}

func DefaultReviewPolicySettings() ReviewPolicySettings {
	return ReviewPolicySettings{false, DefaultReviewRequiredApprovals, DefaultReviewAllowSelfApproval,
		int(DefaultReviewApprovalTTL / time.Second), int(DefaultReviewStepUpMaxAge / time.Second)}
}

type AutomationReviewPolicy struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	DeviceProfileId     uint   `gorm:"not null"`
	Required            bool   `gorm:"not null;default:false"`
	RequiredApprovals   int16  `gorm:"not null;default:1"`
	AllowSelfApproval   bool   `gorm:"not null;default:false"`
	ApprovalTtlSeconds  int32  `gorm:"not null;default:86400"`
	StepUpMaxAgeSeconds int32  `gorm:"not null;default:900"`
	Revision            int32  `gorm:"not null;default:1"`
	UpdatedBy           string `gorm:"size:320;not null"`
}

func (p AutomationReviewPolicy) AuditLabel() string {
	return strconv.FormatUint(uint64(p.DeviceProfileId), 10)
}
func (AutomationReviewPolicy) DefaultOrder() string { return "automation_review_policies.id DESC" }
func (p AutomationReviewPolicy) Settings() ReviewPolicySettings {
	return ReviewPolicySettings{p.Required, int(p.RequiredApprovals), p.AllowSelfApproval, int(p.ApprovalTtlSeconds), int(p.StepUpMaxAgeSeconds)}
}

type AutomationReviewPolicyChange struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	Token           string         `gorm:"size:64;not null"`
	DeviceProfileId uint           `gorm:"not null;index"`
	BaseRevision    int32          `gorm:"not null"`
	Proposed        datatypes.JSON `gorm:"not null"`
	ProposedBy      string         `gorm:"size:320;not null"`
	State           string         `gorm:"size:16;not null;index"`
	EffectiveAt     sql.NullTime
	DecidedBy       sql.NullString `gorm:"size:320"`
	DecidedAt       sql.NullTime
	Revision        int `gorm:"not null;default:0"`
}

func (p AutomationReviewPolicyChange) AuditLabel() string { return p.Token }
func (AutomationReviewPolicyChange) DefaultOrder() string {
	return "automation_review_policy_changes.created_at DESC, automation_review_policy_changes.id DESC"
}

type AutomationReviewBreakGlass struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	DeviceProfileId uint           `gorm:"not null;index"`
	Action          string         `gorm:"size:32;not null"`
	Target          string         `gorm:"size:128;not null"`
	Reason          string         `gorm:"size:512;not null"`
	Actor           string         `gorm:"size:320;not null"`
	Amr             datatypes.JSON `gorm:"not null"`
	MfaTime         sql.NullTime
	Txid            int64 `gorm:"not null;index"`
}

func (p AutomationReviewBreakGlass) AuditLabel() string { return p.Action + ":" + p.Target }
func (AutomationReviewBreakGlass) DefaultOrder() string {
	return "automation_review_break_glass.created_at DESC, automation_review_break_glass.id DESC"
}

type AutomationReviewPolicyView struct {
	Stored    *AutomationReviewPolicy
	Pending   *AutomationReviewPolicyChange
	Effective ReviewPolicySettings
}

// TableName is a literal because gorm's plural would be "automation_review_break_glasses".
func (AutomationReviewBreakGlass) TableName() string { return "automation_review_break_glass" }
