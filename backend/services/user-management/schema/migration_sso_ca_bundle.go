// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// ssoCaBundleSnapshot is this migration's own snapshot of iam_idp_connections: the primary
// key and the one column it adds. Never the live model (see migrations.go), and the
// TableName is load-bearing: without it AutoMigrate would create a table named after the
// type and leave the connections table without the column.
type ssoCaBundleSnapshot struct {
	ID uint `gorm:"primarykey"`

	// The connection's private trust anchors, PEM. Public certificates, so a plain column
	// rather than the secret store. Empty (the default, and every existing connection)
	// means the provider is verified against the system roots alone.
	CaBundle string `gorm:"type:text;not null;default:''"`
}

func (ssoCaBundleSnapshot) TableName() string { return "iam_idp_connections" }

// NewSsoCaBundleMigration adds a connection's private CA bundle.
//
// AutoMigrate is individually re-runnable (adds the column if absent, no-op if present),
// which the chain requires since migrations run with UseTransaction:false and replay from
// the top after a failure.
func NewSsoCaBundleMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261013120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&ssoCaBundleSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&ssoCaBundleSnapshot{}, "ca_bundle")
		},
	}
}
