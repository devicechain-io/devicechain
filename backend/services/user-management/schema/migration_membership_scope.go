// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// membershipScopeSnapshot is this migration's own snapshot of iam_memberships: the
// primary key and the one column it adds. A SNAPSHOT, never iam.Membership (see
// baseline_snapshot.go for the rule and the failure it prevents), and the pinned
// TableName is load-bearing: without it AutoMigrate creates a new table named after the
// type and leaves iam_memberships untouched.
type membershipScopeSnapshot struct {
	ID uint `gorm:"primarykey"`

	// The membership's scope (ADR-081): a JSON array of anchors such as
	// "customer:acme". NULL is the whole tenant, which is every membership that existed
	// before this column did — so no backfill, and nothing an existing member could see
	// changes. jsonb rather than a join table: the scope is read whole, at token mint, and
	// never queried by anchor on this side.
	Scope []string `gorm:"type:jsonb;serializer:json"`
}

func (membershipScopeSnapshot) TableName() string { return "iam_memberships" }

// NewMembershipScopeMigration adds iam_memberships.scope (ADR-081): the anchors that bound
// what a membership sees inside its tenant. Individually re-runnable: AutoMigrate adds the
// column when it is absent and does nothing when it is present.
func NewMembershipScopeMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261016120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&membershipScopeSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&membershipScopeSnapshot{}, "scope")
		},
	}
}
