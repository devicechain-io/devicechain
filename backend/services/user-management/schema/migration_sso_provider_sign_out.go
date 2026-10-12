// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// ssoProviderSignOutSnapshot is this migration's own snapshot of iam_idp_connections: the
// primary key and the one column it adds. Never the live model (see migrations.go), and
// the TableName is load-bearing: without it AutoMigrate would create a table named after
// the type and leave the connections table without the column.
type ssoProviderSignOutSnapshot struct {
	ID uint `gorm:"primarykey"`

	// NOT NULL with a false default, so every connection that exists when this runs keeps
	// signing people out of this platform only, exactly as before.
	ProviderSignOut bool `gorm:"not null;default:false"`
}

func (ssoProviderSignOutSnapshot) TableName() string { return "iam_idp_connections" }

// NewSsoProviderSignOutMigration adds a connection's "also sign out at the provider" switch.
//
// AutoMigrate is individually re-runnable (adds the column if absent, no-op if present),
// which the chain requires since migrations run with UseTransaction:false and replay from
// the top after a failure.
func NewSsoProviderSignOutMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261012120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&ssoProviderSignOutSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&ssoProviderSignOutSnapshot{}, "provider_sign_out")
		},
	}
}
