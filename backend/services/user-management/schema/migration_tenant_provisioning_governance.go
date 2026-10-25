// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantProvisioningGovernanceSnapshot is this migration's own snapshot of iam_tenants:
// the primary key, and the two columns it adds. Nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go: pointing it at
// iam.Tenant would make this migration mean whatever that struct means on the day it runs,
// which breaks FRESH installs the moment the model gains another field.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type name
// and this migration silently creates a `tenant_provisioning_governance_snapshots` table
// instead of altering the tenant row — succeeding, migrating nothing, and leaving every
// writer above it assigning to columns that do not exist.
// TestTheProvisioningGovernanceMigrationDoesNotCreateAStrayTable is the guard.
type tenantProvisioningGovernanceSnapshot struct {
	ID uint `gorm:"primarykey"`

	// BOTH NULLABLE, which is what makes "inherit the level below" expressible: null means
	// the tenant's tier decides, and failing that the platform default. A NOT NULL column
	// defaulting to zero would turn every un-configured tenant into one that has chosen a
	// bucket admitting nothing. It is equally not "unlimited": no level means that.
	ProvisioningRegistrationsPerSecond *float64
	ProvisioningBurst                  *int
}

func (tenantProvisioningGovernanceSnapshot) TableName() string { return "iam_tenants" }

// NewTenantProvisioningGovernanceMigration adds the per-tenant override columns of the
// self-registration (provisioning) governance dimension: how fast a tenant's devices may
// register themselves, as a rate and a burst — the same pair every rate dimension carries.
//
// AutoMigrate is individually re-runnable — it adds a column if it is absent and is a no-op
// if it is present — which the chain requires, since migrations run with
// UseTransaction:false and replay from the top after a failure. It seeds no rows, so the
// DDL-only bar for the migration-diff gate holds.
func NewTenantProvisioningGovernanceMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261024120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&tenantProvisioningGovernanceSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range []string{"provisioning_registrations_per_second", "provisioning_burst"} {
				if err := tx.Migrator().DropColumn(&tenantProvisioningGovernanceSnapshot{}, col); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
