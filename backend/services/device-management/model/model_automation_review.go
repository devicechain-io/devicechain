// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
)

const (
	ReviewKindRuleChange            = "RULE_CHANGE"
	ReviewKindNoAutomationChange    = "NO_AUTOMATION_CHANGE"
	ReviewKindUnreviewable          = "UNREVIEWABLE"
	ReviewOutcomePassed             = "PASSED"
	ReviewOutcomeFailed             = "FAILED"
	ReviewOutcomeInconclusive       = "INCONCLUSIVE"
	ReviewOutcomeNoAutomationChange = "NO_AUTOMATION_CHANGE"
	ReviewStateRan                  = "RAN"
	ReviewStateApproved             = "APPROVED"
	ReviewStateRejected             = "REJECTED"
	ReviewStateConsumed             = "CONSUMED"
	ReviewStateExpired              = "EXPIRED"
)

// AutomationReview freezes the evidence and the exact draft a person may approve.
// No ScopedModel implementation is intentional: storage refuses scoped callers, since
// this ledger does not carry the anchors needed to authorize part of a profile.
// Result is TEXT so a jsonb round trip cannot invalidate the engine's byte digest.
type AutomationReview struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	Token                  string  `gorm:"size:64;not null"`
	DeviceProfileId        uint    `gorm:"not null;index"`
	Kind                   string  `gorm:"size:32;not null"`
	RuleToken              *string `gorm:"size:128"`
	RequestId              string  `gorm:"size:128;not null"`
	BaselineActivationId   uint    `gorm:"not null"`
	BaselineVersion        int32   `gorm:"not null"`
	BaselineDigest         string  `gorm:"size:80;not null"`
	DraftDigest            string  `gorm:"size:80;not null"`
	BaseEnvelope           *datatypes.JSON
	DraftEnvelope          *datatypes.JSON
	DraftAuthors           datatypes.JSON `gorm:"not null"`
	ProfileChanges         datatypes.JSON `gorm:"not null"`
	Scope                  datatypes.JSON `gorm:"not null"`
	ScopeDigest            string         `gorm:"size:80;not null"`
	StreamBounds           datatypes.JSON `gorm:"not null"`
	Expectations           datatypes.JSON `gorm:"not null"`
	ExpectationsDigest     string         `gorm:"size:80;not null"`
	Result                 string         `gorm:"type:text;not null"`
	ResultDigest           string         `gorm:"size:80;not null"`
	InputDigest            string         `gorm:"size:80;not null"`
	EngineBuild            string         `gorm:"size:128;not null"`
	Outcome                string         `gorm:"size:32;not null"`
	ReviewDigest           string         `gorm:"size:80;not null"`
	State                  string         `gorm:"size:16;not null;index"`
	Approvals              datatypes.JSON `gorm:"not null"`
	RequiredApprovals      int            `gorm:"not null;default:1"`
	ApprovedPolicyRevision *int32
	ApprovedAt             *time.Time
	ExpiresAt              *time.Time
	ConsumedVersion        *int32
	ConsumedTxid           *int64
	CreatedBy              string  `gorm:"size:320;not null"`
	DecidedBy              *string `gorm:"size:320"`
	DecidedNote            *string `gorm:"size:512"`
	DecidedAt              *time.Time
	Revision               int `gorm:"not null;default:0"`
}

// DefaultOrder ends on the primary key so pages have a total order.
func (AutomationReview) DefaultOrder() string {
	return "automation_reviews.created_at DESC, automation_reviews.id DESC"
}

// AuditLabel identifies the evidence in the journal by its unguessable token.
func (r AutomationReview) AuditLabel() string { return r.Token }

// effectiveReviewState derives expiry without rewriting the approval ledger. A
// missing approval timestamp fails closed. Pending and terminal states do not age.
func effectiveReviewState(r *AutomationReview, p ReviewPolicySettings, now time.Time) string {
	if r.State == ReviewStateApproved && (r.ExpiresAt == nil || r.ApprovedAt == nil ||
		!now.Before(*r.ExpiresAt) || !now.Before(r.ApprovedAt.Add(time.Duration(p.ApprovalTtlSeconds)*time.Second))) {
		return ReviewStateExpired
	}
	return r.State
}
