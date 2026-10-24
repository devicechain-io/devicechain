// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// provisioningGovernanceTenantRow is an independent read-back type, deliberately NOT the
// snapshot the migration writes through: sharing the struct would let a wrong TableName
// pass by being wrong in both places at once.
type provisioningGovernanceTenantRow struct {
	ID                                 uint
	Token                              string
	Name                               string
	TierID                             uint
	ProvisioningRegistrationsPerSecond *float64
	ProvisioningBurst                  *int
}

func (provisioningGovernanceTenantRow) TableName() string { return "iam_tenants" }

// The columns land on the tenant table (proved by writing through them), and an unset
// tenant reads back NULL — inherit — not zero, which would be a bucket admitting nothing.
func TestTheProvisioningGovernanceMigrationAddsTheColumnsToTheTenantTable(t *testing.T) {
	db := newMigratedDB(t)
	for _, c := range []string{"provisioning_registrations_per_second", "provisioning_burst"} {
		require.Truef(t, db.Migrator().HasColumn(&provisioningGovernanceTenantRow{}, c),
			"%s must exist on iam_tenants after the chain runs", c)
	}

	tierID := seededTierID(t, db)
	rate, burst := 12.5, 40
	require.NoError(t, db.Create(&provisioningGovernanceTenantRow{
		Token: "bounded", Name: "Bounded", TierID: tierID,
		ProvisioningRegistrationsPerSecond: &rate, ProvisioningBurst: &burst,
	}).Error)
	var back provisioningGovernanceTenantRow
	require.NoError(t, db.First(&back, "token = ?", "bounded").Error)
	require.NotNil(t, back.ProvisioningRegistrationsPerSecond)
	require.Equal(t, 12.5, *back.ProvisioningRegistrationsPerSecond, "a fractional rate must survive")
	require.NotNil(t, back.ProvisioningBurst)
	require.Equal(t, 40, *back.ProvisioningBurst)

	require.NoError(t, db.Create(&provisioningGovernanceTenantRow{
		Token: "inherits", Name: "Inherits", TierID: tierID,
	}).Error)
	var inherits provisioningGovernanceTenantRow
	require.NoError(t, db.First(&inherits, "token = ?", "inherits").Error)
	require.Nil(t, inherits.ProvisioningRegistrationsPerSecond, "unset must be NULL (inherit), not 0")
	require.Nil(t, inherits.ProvisioningBurst, "unset must be NULL (inherit), not 0")
}

// The control for the mistake this migration is exposed to: a lost TableName makes
// AutoMigrate create a brand-new table and report success.
func TestTheProvisioningGovernanceMigrationDoesNotCreateAStrayTable(t *testing.T) {
	db := newMigratedDB(t)
	for _, stray := range []string{
		"tenant_provisioning_governance_snapshots", "tenant_provisioning_governances",
	} {
		require.Falsef(t, db.Migrator().HasTable(stray),
			"the provisioning-governance migration created %q instead of altering iam_tenants", stray)
	}
}

// Migrations replay from the top after a failure, so a second run must be a no-op.
func TestTheProvisioningGovernanceMigrationIsReRunnable(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantProvisioningGovernanceMigration().Migrate(db))
	require.True(t, db.Migrator().HasColumn(&provisioningGovernanceTenantRow{}, "provisioning_burst"))
}

// The rollback drops exactly the two columns this migration added and no neighbour.
func TestTheProvisioningGovernanceMigrationRollbackDropsOnlyItsOwnColumns(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, NewTenantProvisioningGovernanceMigration().Rollback(db))
	for _, gone := range []string{"provisioning_registrations_per_second", "provisioning_burst"} {
		require.Falsef(t, db.Migrator().HasColumn(&provisioningGovernanceTenantRow{}, gone), "%s must be dropped", gone)
	}
	for _, kept := range []string{
		"held_command_ceiling", "shed_priority", "ingest_readings_per_second", "outbound_burst",
		"ai_inference_burst", "token", "name",
	} {
		require.Truef(t, db.Migrator().HasColumn(&provisioningGovernanceTenantRow{}, kept),
			"the rollback dropped %q, which this migration never added", kept)
	}
}
