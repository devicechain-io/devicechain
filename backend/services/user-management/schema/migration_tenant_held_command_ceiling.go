// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantHeldCommandCeilingSnapshot is this migration's own snapshot of iam_tenants: the
// primary key, and the one column it adds. Nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go. Pointing
// this at iam.Tenant would make this migration mean whatever that struct means on the
// day it runs — which breaks FRESH installs the moment the model gains another field
// (the baseline creates the table without it, this migration adds its one column, and the
// next fresh install replays into "column already exists") while every existing database
// applies cleanly and looks healthy.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type
// name and this migration silently creates a `tenant_held_command_ceiling_snapshots`
// table instead of altering the tenant row — succeeding, migrating nothing, and leaving
// every writer above it assigning to a column that does not exist.
// TestTheHeldCommandCeilingMigrationDoesNotCreateAStrayTable is the guard.
type tenantHeldCommandCeilingSnapshot struct {
	ID uint `gorm:"primarykey"`

	// NULLABLE, which is what makes "inherit the level below" expressible at all: null
	// means the tenant's tier decides, and failing that the enforcing service's own
	// configured default. A NOT NULL column with a zero default would turn every
	// un-configured tenant into one that has explicitly chosen to hold NOTHING — every
	// command to an absent device refused — which is the opposite of the inherit this
	// column exists to express. It is equally not "unlimited": no value at any level
	// means that.
	HeldCommandCeiling *int
}

func (tenantHeldCommandCeilingSnapshot) TableName() string { return "iam_tenants" }

// NewTenantHeldCommandCeilingMigration adds the per-tenant ceiling on how many commands
// may sit in the HELD state — the command-delivery lifecycle state meaning "the platform
// is deliberately withholding dispatch because the device is known absent" (ADR-023
// governance, packaged per the ADR-065 tier).
//
// An offline fleet's backlog accumulates in that state and can sit for days, and nothing
// drains it on its own, so it is unbounded growth in durable storage on behalf of a
// tenant whose devices are simply not there. This column is where an operator bounds it
// per tenant, above the tier's default.
//
// It is a scalar rather than a rate+burst pair for the same reason shed_priority is: a
// backlog has no burst and no per-second unit, so a governance Dimension would fabricate
// both.
//
// AutoMigrate is individually re-runnable — it adds the column if it is absent and is a
// no-op if it is present — which the chain requires, since migrations run with
// UseTransaction:false and replay from the top after a failure.
func NewTenantHeldCommandCeilingMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260813120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantHeldCommandCeilingSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&tenantHeldCommandCeilingSnapshot{}, "held_command_ceiling")
		},
	}
}
