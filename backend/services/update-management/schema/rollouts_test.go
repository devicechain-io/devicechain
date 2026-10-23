// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRolloutsIDSortsAfterEveryEarlierMigrationAndIsLast(t *testing.T) {
	for _, earlier := range []string{BaselineID, ArtifactsID, UploadLeaseID, AssignmentsID, SecretsID, SignersID,
		AssignmentSignatureID, AssignmentScopeAnchorsID} {
		require.Greater(t, RolloutsID, earlier)
	}
	require.Equal(t, RolloutsID, Migrations[len(Migrations)-1].ID, "a new migration is appended last")
}

// Migrations run with UseTransaction:false and replay from the top after a failure.
func TestTheRolloutsMigrationIsRerunnable(t *testing.T) {
	db := openDB(t)
	for range 3 {
		require.NoError(t, NewRolloutsSchema().Migrate(db))
	}
	require.True(t, db.Migrator().HasTable("update_rollouts"))
	require.True(t, db.Migrator().HasTable("update_rollout_targets"))
}

func TestTheRolloutTablesShape(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewRolloutsSchema().Migrate(db))
	// Physical column order is part of the schema.
	require.Equal(t, []string{"id", "created_at", "updated_at", "tenant_id", "token", "name", "artifact_id",
		"group_token", "group_version", "max_in_flight", "failure_threshold", "state", "pause_reason", "target_count",
		"created_by", "started_at", "finished_at", "state_changed_by", "state_changed_at"},
		columnNames(t, db, "update_rollouts"))
	require.Equal(t, []string{"id", "created_at", "updated_at", "tenant_id", "rollout_id", "device_token", "status",
		"assignment_id", "dispatched_at", "skip_code", "skip_detail"}, columnNames(t, db, "update_rollout_targets"))
}

func TestTheRolloutUniqueIndexes(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewRolloutsSchema().Migrate(db))
	rollout := func(tenant, token string) error {
		return db.Exec(`INSERT INTO update_rollouts (tenant_id, token, name, artifact_id, group_token, max_in_flight,
			state, pause_reason, target_count) VALUES (?, ?, 'n', 1, 'g', 1, 'DRAFT', '', 0)`, tenant, token).Error
	}
	require.NoError(t, rollout("acme", "r1"))
	require.Error(t, rollout("acme", "r1"), "(tenant, token) is unique")
	require.NoError(t, rollout("globex", "r1"), "per tenant")

	target := func(rolloutID int, device string) error {
		return db.Exec(`INSERT INTO update_rollout_targets (tenant_id, rollout_id, device_token, status, skip_code, skip_detail)
			VALUES ('acme', ?, ?, 'PENDING', '', '')`, rolloutID, device).Error
	}
	require.NoError(t, target(1, "d1"))
	require.Error(t, target(1, "d1"), "a device is in a rollout's target set once")
	require.NoError(t, target(2, "d1"))
}
