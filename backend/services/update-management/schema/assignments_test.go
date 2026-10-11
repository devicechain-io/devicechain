// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func TestAssignmentIDsSortAfterEveryEarlierMigration(t *testing.T) {
	// gormigrate runs in slice order, but the IDs are also the audit trail of when a change
	// landed; one that sorts before an earlier migration reads as a rewrite of history.
	for _, earlier := range []string{BaselineID, ArtifactsID, UploadLeaseID} {
		require.Greater(t, AssignmentsID, earlier)
	}
	require.Greater(t, SecretsID, AssignmentsID)
}

// Migrations run with UseTransaction:false and replay from the top after a failure.
func TestTheAssignmentsMigrationIsRerunnable(t *testing.T) {
	db := openDB(t)
	for range 3 {
		require.NoError(t, NewAssignmentsSchema().Migrate(db))
	}
	require.True(t, db.Migrator().HasTable("update_assignments"))
	require.True(t, db.Migrator().HasTable("update_attempts"))
}

func columnNames(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	cols, err := db.Migrator().ColumnTypes(table)
	require.NoError(t, err)
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name())
	}
	return names
}

func TestTheAssignmentTablesShape(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewAssignmentsSchema().Migrate(db))
	// Physical column order is part of the schema.
	require.Equal(t, []string{"id", "created_at", "tenant_id", "token", "device_token", "component",
		"artifact_id", "actor", "superseded_at"}, columnNames(t, db, "update_assignments"))
	require.Equal(t, []string{"id", "created_at", "updated_at", "tenant_id", "token", "assignment_id",
		"device_token", "component", "target_version", "target_digest", "requires_reboot", "state", "version",
		"last_seq", "last_report_at", "boot_id_at_start", "install_boot_id", "last_boot_id", "last_stage",
		"cancel_requested", "reconciled", "progress_bytes", "progress_total", "failure_stage", "failure_code",
		"failure_detail", "failure_platform"}, columnNames(t, db, "update_attempts"))
}

func insertAssignment(db *gorm.DB, tenant, token, device, component string, superseded bool) error {
	sup := "NULL"
	if superseded {
		sup = "CURRENT_TIMESTAMP"
	}
	return db.Exec(`INSERT INTO update_assignments (tenant_id, token, device_token, component, artifact_id, superseded_at)
		VALUES (?, ?, ?, ?, 1, `+sup+`)`, tenant, token, device, component).Error
}

// One CURRENT assignment per (tenant, device, component): the invariant the assign path relies on
// when two operators assign to one device at once.
func TestOnlyOneAssignmentIsCurrentPerDeviceAndComponent(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewAssignmentsSchema().Migrate(db))

	require.NoError(t, insertAssignment(db, "acme", "a1", "dev", "firmware", false))
	require.Error(t, insertAssignment(db, "acme", "a2", "dev", "firmware", false), "a second current row for the same device and component")
	require.NoError(t, insertAssignment(db, "acme", "a3", "dev", "app/agent", false), "another component is independent")
	require.NoError(t, insertAssignment(db, "acme", "a4", "dev2", "firmware", false), "another device is independent")
	require.NoError(t, insertAssignment(db, "globex", "a1", "dev", "firmware", false), "another tenant is independent")

	// History is unbounded: superseded rows do not occupy the slot.
	require.NoError(t, insertAssignment(db, "acme", "a5", "dev", "firmware", true))
	require.NoError(t, insertAssignment(db, "acme", "a6", "dev", "firmware", true))
	// Tokens are unique per tenant.
	require.Error(t, insertAssignment(db, "acme", "a5", "dev9", "firmware", true))
}

func TestAnAssignmentHasOneAttemptAndAttemptTokensAreTenantUnique(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewAssignmentsSchema().Migrate(db))
	insert := func(tenant, token string, assignment int) error {
		return db.Exec(`INSERT INTO update_attempts (tenant_id, token, assignment_id, device_token, component,
			target_version, target_digest, requires_reboot, state, version, last_seq, last_report_at,
			boot_id_at_start, install_boot_id, last_boot_id, last_stage, cancel_requested, reconciled,
			failure_stage, failure_code, failure_detail, failure_platform)
			VALUES (?, ?, ?, 'dev', 'firmware', '1', 'sha256:x', 0, 'QUEUED', 0, 0, CURRENT_TIMESTAMP,
			'', '', '', '', 0, 0, '', '', '', 0)`, tenant, token, assignment).Error
	}
	require.NoError(t, insert("acme", "t1", 1))
	require.Error(t, insert("acme", "t2", 1), "a second attempt for the same assignment")
	require.Error(t, insert("acme", "t1", 2), "a repeated attempt token in one tenant")
	require.NoError(t, insert("globex", "t1", 3), "the same token in another tenant")
}
