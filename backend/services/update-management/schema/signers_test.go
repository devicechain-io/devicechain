// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSignersIDSortsAfterEveryEarlierMigration(t *testing.T) {
	for _, earlier := range []string{BaselineID, ArtifactsID, UploadLeaseID, AssignmentsID, SecretsID} {
		require.Greater(t, SignersID, earlier)
	}
}

// Migrations run with UseTransaction:false and replay from the top after a failure.
func TestTheSignersMigrationIsRerunnable(t *testing.T) {
	db := openDB(t)
	for range 3 {
		require.NoError(t, NewSignersSchema().Migrate(db))
	}
	require.True(t, db.Migrator().HasTable("artifact_signers"))
	require.True(t, db.Migrator().HasTable("artifact_signatures"))
}

func TestTheSignerTablesShape(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewSignersSchema().Migrate(db))
	// Physical column order is part of the schema.
	require.Equal(t, []string{"id", "created_at", "updated_at", "tenant_id", "token", "name", "algorithm",
		"public_key_pem", "key_id", "state", "revoked_at", "created_by", "revoked_by"}, columnNames(t, db, "artifact_signers"))
	require.Equal(t, []string{"id", "created_at", "tenant_id", "artifact_id", "signer_id", "signature", "actor"},
		columnNames(t, db, "artifact_signatures"))
}

func TestTheSignerUniqueIndexes(t *testing.T) {
	db := openDB(t)
	require.NoError(t, NewSignersSchema().Migrate(db))
	signer := func(tenant, token, key string) error {
		return db.Exec(`INSERT INTO artifact_signers (tenant_id, token, name, algorithm, public_key_pem, key_id, state)
			VALUES (?, ?, 'n', 'ED25519', 'pem', ?, 'ACTIVE')`, tenant, token, key).Error
	}
	require.NoError(t, signer("acme", "s1", "k1"))
	require.Error(t, signer("acme", "s1", "k2"), "(tenant, token) is unique")
	require.Error(t, signer("acme", "s2", "k1"), "(tenant, key id) is unique")
	require.NoError(t, signer("globex", "s1", "k1"), "both are per tenant")

	sig := func(artifact, signerID int) error {
		return db.Exec(`INSERT INTO artifact_signatures (tenant_id, artifact_id, signer_id, signature) VALUES ('acme', ?, ?, x'01')`,
			artifact, signerID).Error
	}
	require.NoError(t, sig(1, 1))
	require.Error(t, sig(1, 1), "a signer signs an artifact once")
	require.NoError(t, sig(1, 2))
	require.NoError(t, sig(2, 1))
}
