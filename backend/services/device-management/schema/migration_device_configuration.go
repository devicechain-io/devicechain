// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// deviceConfigurationRevision is the SNAPSHOT of the per-device configuration revision
// log as of this migration. It is not model.DeviceConfigurationRevision and must never
// be replaced by it: a migration's shapes are a point in time, and pointing one at a
// live model silently rewrites an already-applied migration whenever that model changes.
// The mixins are inlined for the same reason.
//
// Append-only: one row per distinct device-visible configuration document a device has
// been given. Revision is monotonic per device; the unique (device_id, revision) index
// is the backstop that makes two writers unable to mint the same number. No soft delete
// (it is an event log) and no foreign key: the rows are removed with their device by
// DeleteDevice and with their tenant by the catalog-driven purge, which classifies the
// table by its tenant_id column.
type deviceConfigurationRevision struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	DeviceId uint  `gorm:"not null;uniqueIndex:uix_dcr_device_revision,priority:1"`
	Revision int64 `gorm:"not null;uniqueIndex:uix_dcr_device_revision,priority:2"`

	// Nullable: a device whose type has no published profile has an empty document and
	// no profile version to cite.
	ProfileVersionId *uint

	// Plain text, never json/jsonb: jsonb re-renders a document on read (spacing, key
	// order), and the stored bytes must be exactly the bytes the digest covers.
	Document string `gorm:"not null"`
	Digest   string `gorm:"not null;size:71"`
	Actor    string `gorm:"size:256"`
}

// deviceConfigurationState is the SNAPSHOT of the per-device reported configuration
// state as of this migration: one row per device, a projection of the device's last
// configuration report. No soft delete; tenant_id for the purge.
type deviceConfigurationState struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	DeviceId uint `gorm:"not null;uniqueIndex:uix_dcs_device"`

	ReportedRevision sql.NullInt64
	ReportedDigest   sql.NullString `gorm:"size:71"`
	ReportedStatus   sql.NullString `gorm:"size:16"`
	ReportedErrors   datatypes.JSON
	ReportedAt       sql.NullTime
	LastSyncAt       sql.NullTime
}

// NewDeviceConfigurationSchema creates the device configuration revision log and the
// reported-state projection.
//
// This is an APPENDED migration; the baseline is frozen. Both tables are new and empty:
// no device has ever been given a configuration revision, so there is nothing to
// backfill, and the migration seeds no rows.
//
// Re-runnability: migrations run with UseTransaction:false and replay from the top after
// a failure. AutoMigrate creates a table only when it is absent and adds only missing
// columns and indexes, so running this twice is a no-op.
func NewDeviceConfigurationSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261010130000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&deviceConfigurationRevision{}, &deviceConfigurationState{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"device_configuration_states", "device_configuration_revisions"})
		},
	}
}
