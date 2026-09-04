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

// deviceReplacement is the SNAPSHOT of the device-replacement journal as of this
// migration (ADR-074). It is not model.DeviceReplacement and must never be replaced
// by it: a migration's shapes are a point in time, and pointing one at a live model
// silently rewrites an already-applied migration whenever that model changes —
// which breaks FRESH installs while every existing database applies cleanly and
// looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped) are INLINED rather than embedded,
// matching every other shape in this package, so a field added to a core mixin
// cannot rewrite this migration from a PR that never touched device-management.
//
// # What the table is
//
// One row per physical-unit swap: the failed hardware was retired and a new unit
// bound to the SAME logical device identity. Append-only — there is no update or
// delete path to it anywhere in the API or the schema — which is what makes it
// answerable after the fact.
//
// # Why it has no token
//
// Every other tenant-scoped entity in this area carries one, and this one
// deliberately does not, so it is absent from tokenIndexModels' successor list by
// design rather than by oversight. A token is an ADDRESS a caller re-points and
// looks a row up by; this is an event, addressed only as part of one device's
// history. Giving it a token would create an addressing surface for a row that must
// never be re-pointed.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it
// directly with no exemption to register. That is checked by the coverage gate that
// runs alongside migration-diff verify, not asserted here.
type deviceReplacement struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	DeviceId uint `gorm:"not null;index"`
	// Names fk_device-management_device_replacements_device.
	Device *device

	OccurredTime time.Time `gorm:"not null;index"`

	Actor          string         `gorm:"size:256"`
	Reason         sql.NullString `gorm:"size:1024"`
	UnitIdentifier sql.NullString `gorm:"size:256"`

	RetiredCredentialTokens datatypes.JSON `gorm:"not null"`

	NewCredentialToken string `gorm:"not null;size:128"`
	NewCredentialType  string `gorm:"not null;size:32"`
}

// NewDeviceReplacementsSchema creates the append-only device-replacement journal
// (ADR-074).
//
// This is an APPENDED migration. The baseline is frozen — its table list and its
// snapshot structs stay exactly as they are, and this table is created here, after
// it, which is the same position a fresh install and an upgraded one both put it in.
// Physical order is part of what the golden schema diff compares.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure,
// so this must be individually re-runnable. AutoMigrate is: it creates a table only
// when it is absent and adds only missing columns. Nothing here depends on a
// statement before it.
//
// # No backfill, and this time that claim is checked rather than assumed
//
// The reasoning that "pre-GA an instance is recreated" is true of a BASELINE and
// false of an appended migration — v0.11.0 and v0.12.0 were each reached with `helm
// upgrade`, so an appended migration does run against databases holding real data.
// What makes a backfill unnecessary HERE is narrower and specific: a replacement row
// records an operation, and no such operation has ever run. There is no historical
// state to derive one from, and inventing rows for swaps nobody performed would be
// worse than having none. An upgraded instance correctly starts with an empty
// journal.
//
// # No composite (tenant_id, device_id, occurred_time) index
//
// The per-device read filters on device_id and orders by occurred_time DESC, which
// looks like it wants one. It is not created because this table is written once per
// physical hardware swap — a rate measured in units per fleet per year, not per
// second — so the plain device_id index leaves a per-device scan of single-digit
// rows. An index sized for a table that will not grow is cost with no reader. Add it
// when the row count says to, not when the query shape suggests it.
func NewDeviceReplacementsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260904120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&deviceReplacement{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"device_replacements"})
		},
	}
}
