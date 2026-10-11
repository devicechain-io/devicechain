// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// UploadLeaseID is the upload-lease migration's frozen ID.
const UploadLeaseID = "20261010000200"

// NewUploadLeaseSchema appends the column that holds an artifact's single in-flight upload
// lease.
//
// The snapshot is declared INSIDE the function and named artifact, on purpose. gorm derives
// the table name from the Go type's name (and applies the area's table prefix only to a
// derived name), so the type has to be called artifact to address the artifacts table, and
// the package-level artifact in migration_artifacts.go is the first migration's frozen
// shape, which must not gain a column. A local type shadows it without touching it.
//
// AutoMigrate on this partial struct adds only the column that is missing and never drops
// or alters the others, which also makes the migration re-runnable.
func NewUploadLeaseSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: UploadLeaseID,
		Migrate: func(tx *gorm.DB) error {
			type artifact struct {
				ID             uint `gorm:"primarykey"`
				UploadingSince sql.NullTime
			}
			return tx.AutoMigrate(&artifact{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn("artifacts", "uploading_since")
		},
	}
}
