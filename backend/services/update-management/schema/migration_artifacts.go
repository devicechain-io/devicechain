// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ArtifactsID is the artifact-catalogue migration's frozen ID. It is recorded in every
// database that ran it, so it never changes.
const ArtifactsID = "20261010000100"

// artifact is the snapshot of the artifacts table this migration creates, captured at
// the point in time it was written. It is private to this package and is NOT the live
// model: a column the live model gains later arrives in a NEW appended migration, and
// this struct stays exactly as it is.
//
// Self-contained down to the mixins (gorm.Model, rdb.TenantScoped and rdb.TokenReference
// are inlined), so a change in core cannot rewrite this migration from a PR that never
// touched this service. Field order matches the embedded layout of the live model, because
// physical column order is part of the schema.
//
// 🔴 IT HAS NO TableName METHOD. core/rdb pins the gorm NamingStrategy's TablePrefix to
// the functional area, and that prefix applies only when gorm DERIVES the table name; an
// explicit TableName bypasses it and silently renames every index on the table. Renaming
// this type retargets the migration, so leave the name alone.
//
// 🔴 DIGEST IS A STRING COLUMN, NOT JSON. The digest is compared as bytes, and a jsonb
// column re-renders what it holds (key order, whitespace, number text) - fine for a
// document nobody hashes, wrong for a value whose whole job is to be byte-identical to
// what a device computes. CompatibleProfiles and Provenance are jsonb because they are
// read as documents and never compared byte-for-byte.
type artifact struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Kind               string         `gorm:"not null;size:16"`
	Name               string         `gorm:"not null;size:128"`
	Version            string         `gorm:"not null;size:64"`
	Component          string         `gorm:"not null;size:64"`
	SizeBytes          int64          `gorm:"not null"`
	Digest             string         `gorm:"not null;size:71"`
	MediaType          string         `gorm:"not null;size:127"`
	CompatibleProfiles datatypes.JSON `gorm:"not null"`
	TrustStatus        string         `gorm:"not null;size:16"`
	Provenance         datatypes.JSON
	BlobRef            sql.NullString `gorm:"size:512"`
	State              string         `gorm:"not null;size:16"`
}

// NewArtifactsSchema appends the artifact catalogue to the empty baseline.
//
// It is individually re-runnable (migrations run with UseTransaction:false and replay from
// the top after a failure): AutoMigrate adds only what is missing and both indexes are
// CREATE ... IF NOT EXISTS.
//
// Two partial unique indexes, both over LIVE rows only (WHERE deleted_at IS NULL) so a
// deleted artifact frees its slot instead of locking it forever:
//   - (tenant_id, token): the catalogue's lookup key;
//   - (tenant_id, name, component, version): the artifact's IMMUTABLE identity - the same
//     name, component and version can never name two different binaries.
//
// They are declared here against the snapshot struct with literal names, deliberately
// not through a helper in core: an index NAME and a WHERE predicate are schema, and one
// sourced from another module starts building something different on fresh installs the
// day that module changes.
func NewArtifactsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: ArtifactsID,
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&artifact{}); err != nil {
				return err
			}
			if err := createPartialUnique(tx, &artifact{}, "uix_artifacts_tenant_token",
				"tenant_id", "token"); err != nil {
				return err
			}
			return createPartialUnique(tx, &artifact{}, "uix_artifacts_tenant_identity",
				"tenant_id", "name", "component", "version")
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("artifacts")
		},
	}
}

func createPartialUnique(tx *gorm.DB, model any, name string, columns ...string) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for index %s: %w", name, err)
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = stmt.Quote(c)
	}
	return tx.Exec(fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE deleted_at IS NULL",
		stmt.Quote(name), stmt.Quote(stmt.Table), strings.Join(quoted, ", "),
	)).Error
}
