// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTheArtifactsMigrationIsAppendedAfterTheBaselineWithItsOwnID(t *testing.T) {
	require.Len(t, Migrations, 9)
	require.Equal(t, BaselineID, Migrations[0].ID)
	require.Equal(t, ArtifactsID, Migrations[1].ID)
	require.Equal(t, UploadLeaseID, Migrations[2].ID)
	require.Equal(t, AssignmentsID, Migrations[3].ID)
	require.Equal(t, SecretsID, Migrations[4].ID)
	require.Equal(t, SignersID, Migrations[5].ID)
	require.Equal(t, AssignmentSignatureID, Migrations[6].ID)
	require.Equal(t, AssignmentScopeAnchorsID, Migrations[7].ID)
	require.Equal(t, RolloutsID, Migrations[8].ID)
	require.Greater(t, AssignmentScopeAnchorsID, AssignmentSignatureID)
	require.NotEqual(t, BaselineID, ArtifactsID)
	// IDs are unique across the chain: gormigrate keys applied migrations by ID.
	seen := map[string]bool{}
	for _, m := range Migrations {
		require.False(t, seen[m.ID], "duplicate migration ID %s", m.ID)
		seen[m.ID] = true
	}
}

// Migrations run with UseTransaction:false and replay from the top after a failure, so the
// migration must survive running twice over its own output.
func TestTheArtifactsMigrationIsRerunnable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, NewBaselineSchema().Migrate(db))
		require.NoError(t, NewArtifactsSchema().Migrate(db))
		require.NoError(t, NewUploadLeaseSchema().Migrate(db))
	}
	require.True(t, db.Migrator().HasTable("artifacts"))
}

func TestTheArtifactsTableShape(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, NewArtifactsSchema().Migrate(db))
	require.NoError(t, NewUploadLeaseSchema().Migrate(db))

	cols, err := db.Migrator().ColumnTypes("artifacts")
	require.NoError(t, err)
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name())
	}
	// Physical column order is part of the schema.
	require.Equal(t, []string{"id", "created_at", "updated_at", "deleted_at", "tenant_id", "token",
		"kind", "name", "version", "component", "size_bytes", "digest", "media_type",
		"compatible_profiles", "trust_status", "provenance", "blob_ref", "state", "uploading_since"}, names)

	// The digest is a STRING column, never JSON: it is compared as bytes, and a jsonb
	// column re-renders what it holds. (The live Postgres type, varchar(71), is pinned by
	// the committed golden schema.)
	for _, c := range cols {
		if c.Name() == "digest" {
			typ := strings.ToLower(c.DatabaseTypeName())
			require.True(t, strings.Contains(typ, "varchar") || strings.Contains(typ, "text"),
				"digest column type %q must be a string type", typ)
		}
	}
}

func TestTheUniqueIndexesAreLiveRowOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, NewArtifactsSchema().Migrate(db))

	insert := func(token, name string) error {
		return db.Exec(`INSERT INTO artifacts (tenant_id, token, kind, name, version, component, size_bytes, digest,
			media_type, compatible_profiles, trust_status, state, deleted_at)
			VALUES ('acme', ?, 'FIRMWARE', ?, '1', 'firmware', 1, 'sha256:x', 'a/b', '[]', 'UNVERIFIED', 'UPLOADING', NULL)`,
			token, name).Error
	}
	require.NoError(t, insert("t1", "n1"))
	require.Error(t, insert("t1", "n2"), "(tenant, token) is unique")
	require.Error(t, insert("t2", "n1"), "(tenant, name, component, version) is unique")

	// A deleted row frees both slots.
	require.NoError(t, db.Exec(`UPDATE artifacts SET deleted_at = CURRENT_TIMESTAMP`).Error)
	require.NoError(t, insert("t1", "n1"))
}

// The lease column is added to an artifacts table that already holds rows, without
// disturbing them: this is the upgrade path from the first migration.
func TestTheUploadLeaseMigrationKeepsExistingRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, NewArtifactsSchema().Migrate(db))
	require.NoError(t, db.Exec(`INSERT INTO artifacts (tenant_id, token, kind, name, version, component, size_bytes, digest,
		media_type, compatible_profiles, trust_status, state)
		VALUES ('acme', 't1', 'FIRMWARE', 'n1', '1', 'firmware', 7, 'sha256:x', 'a/b', '[]', 'UNVERIFIED', 'UPLOADING')`).Error)

	require.NoError(t, NewUploadLeaseSchema().Migrate(db))

	var size int64
	var since *string
	row := db.Raw(`SELECT size_bytes, uploading_since FROM artifacts WHERE token = 't1'`).Row()
	require.NoError(t, row.Scan(&size, &since))
	require.EqualValues(t, 7, size)
	require.Nil(t, since, "an existing artifact starts with no upload in flight")
}
