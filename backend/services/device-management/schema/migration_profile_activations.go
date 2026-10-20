// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// deviceProfileActivation is the SNAPSHOT of the profile activation log as of this
// migration. It is not model.DeviceProfileActivation and must never be replaced by it, for
// the reason every shape in this package gives (see migration_device_replacements.go). The
// tenant mixin is inlined for the same reason.
//
// # What the table is
//
// One row per change of a profile's active version: a publish (which activates the version
// it mints) and a rollback. It is written in the transaction that moves
// device_profiles.active_version, and it is append-only: nothing updates or deletes a row
// except the cascade that removes a deleted profile's history and the tenant purge.
//
// It exists because the pointer alone cannot answer "which version was active at T".
// device_profiles.active_since holds only the LATEST activation, and
// device_profile_versions.created_at is the publish instant, which is wrong for any version
// a rollback later re-activated.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies and erases it
// with no exemption to register.
type deviceProfileActivation struct {
	ID       uint   `gorm:"primarykey"`
	TenantId string `gorm:"index;not null;size:128"`

	// Serves the only read: one profile's log, newest first, by keyset on id. It is a single
	// column on purpose, not (device_profile_id, id): a profile gains an entry per human publish
	// or rollback, so the rows one profile matches are few and the sort over them is free;
	// measured on PostgreSQL 16, the composite plans the same bitmap scan and sort.
	DeviceProfileId uint  `gorm:"not null;index:ix_device_profile_activations_profile_id,priority:1"`
	Version         int32 `gorm:"not null"`

	// publish | rollback.
	Kind        string    `gorm:"not null;size:16"`
	ActivatedAt time.Time `gorm:"not null"`
	ActivatedBy string    `gorm:"size:256"`
}

// TableName is NOT declared: a TableName() bypasses this area's TablePrefix (see
// migration_list_order_indexes.go), and the type name derives device_profile_activations.

// NewProfileActivationsSchema creates the profile activation log.
//
// # No backfill
//
// The log starts at the upgrade. Nothing recorded when a rollback happened, so for a profile
// published before this ran the only honest history is the version rows, and those say when a
// version was PUBLISHED, not when it was active. Writing a row per existing version from them
// would state, as a fact, an activation instant that is wrong exactly where this table is
// meant to be right. An upgraded profile therefore has no rows until its next publish or
// rollback, and a reader treats the absence of a row as "unknown", never as "inactive".
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure.
// AutoMigrate creates the table and index only when absent.
func NewProfileActivationsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261020120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&deviceProfileActivation{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"device_profile_activations"})
		},
	}
}
