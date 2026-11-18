// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// automationReview is this migration's frozen snapshot, with mixins inlined in
// live layout order. It deliberately has neither soft deletion nor TableName.
type automationReview struct {
	ID                     uint `gorm:"primarykey"`
	CreatedAt              time.Time
	UpdatedAt              time.Time
	TenantId               string         `gorm:"index;not null;size:128"`
	Token                  string         `gorm:"size:64;not null"`
	DeviceProfileId        uint           `gorm:"not null;index"`
	Kind                   string         `gorm:"size:32;not null"`
	RuleToken              sql.NullString `gorm:"size:128"`
	RequestId              string         `gorm:"size:128;not null"`
	BaselineActivationId   uint           `gorm:"not null"`
	BaselineVersion        int32          `gorm:"not null"`
	BaselineDigest         string         `gorm:"size:80;not null"`
	DraftDigest            string         `gorm:"size:80;not null"`
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
	ApprovedPolicyRevision sql.NullInt32
	ApprovedAt             sql.NullTime
	ExpiresAt              sql.NullTime
	ConsumedVersion        sql.NullInt32
	ConsumedTxid           sql.NullInt64
	CreatedBy              string         `gorm:"size:320;not null"`
	DecidedBy              sql.NullString `gorm:"size:320"`
	DecidedNote            sql.NullString `gorm:"size:512"`
	DecidedAt              sql.NullTime
	Revision               int `gorm:"not null;default:0"`
}

// NewAutomationReviewsSchema is an appended migration, new table only: old pods
// never see it. tenant_id lets tenant purge classify it. There is no foreign key
// on purpose, like the activation log: profile deletion removes reviews in the
// same transaction. AutoMigrate and IF NOT EXISTS make interrupted runs replayable.
func NewAutomationReviewsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261111150000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&automationReview{}); err != nil {
				return err
			}
			if err := createReviewUniqueIndex(tx, "uix_automation_reviews_tenant_token", "tenant_id", "token"); err != nil {
				return err
			}
			return createReviewUniqueIndex(tx, "uix_automation_reviews_request", "tenant_id", "request_id")
		},
		Rollback: func(tx *gorm.DB) error { return dropTables(tx, []string{"automation_reviews"}) },
	}
}

// The existing tenant-token helper requires deleted_at; this table has no such column.
func createReviewUniqueIndex(tx *gorm.DB, name string, columns ...string) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(&automationReview{}); err != nil {
		return err
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = stmt.Quote(c)
	}
	return tx.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)",
		stmt.Quote(name), stmt.Quote(stmt.Table), strings.Join(quoted, ", "))).Error
}
