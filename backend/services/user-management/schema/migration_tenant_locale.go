// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantLocaleSnapshot is this migration's own snapshot of iam_tenants: the primary
// key, and the one column it adds. Nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go. Pointing
// this at iam.Tenant would make the migration mean whatever that struct means on the
// day it runs — which breaks FRESH installs the moment the tenant gains another field
// (the baseline creates the table without it, this migration adds it, and the next
// fresh install replays into "column already exists") while every existing database
// applies cleanly and looks healthy.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type
// name and this migration silently creates a `tenant_locale_snapshots` table instead
// of touching the tenant table — succeeding, migrating nothing, and leaving the column
// that every reader and writer above it now assigns to.
// TestTheLocaleMigrationAddsTheColumnToTheTenantTable is the guard, and it asserts the
// absence of the stray table as well as the presence of the column.
type tenantLocaleSnapshot struct {
	ID uint `gorm:"primarykey"`

	// Untagged and nullable, matching the branding/basemap override blocks beside it:
	// a null field means "inherit the tier below". The BOUND on the value is not a
	// column width — it lives in the locale package, applied at the mint point, for
	// the same reason every other override's rules do: a size-capped column truncates,
	// where a validator refuses.
	Locale *string
}

func (tenantLocaleSnapshot) TableName() string { return "iam_tenants" }

// NewTenantLocaleMigration adds the per-tenant default-locale column (ADR-066
// sub-workstream d).
//
// The console's locale was a per-BROWSER preference and nothing else: a user picked a
// language, it went into localStorage, and every other user in the tenant — and the
// same user on another machine — started again from whatever their browser happened
// to advertise. A tenant whose staff all work in Spanish had no way to say so once.
// This column is where that one statement lives, and it sits at rung 2 of the console's
// precedence: it beats the browser and loses to a user's own explicit choice.
//
// AutoMigrate is individually re-runnable — it adds the column if absent and is a
// no-op if present — which the chain requires, since migrations run with
// UseTransaction:false and replay from the top after a failure.
func NewTenantLocaleMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260904150000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantLocaleSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&tenantLocaleSnapshot{}, "locale")
		},
	}
}
