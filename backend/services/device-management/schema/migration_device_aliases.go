// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// deviceAlias is the SNAPSHOT of a device's identity alias as of this migration: a
// (scheme, value) pair naming the device in a sender's vocabulary, such as a LoRaWAN DevEUI.
// It is not model.DeviceAlias and must never be replaced by it: a migration's shapes are a
// point in time, and pointing one at a live model silently rewrites an already-applied
// migration whenever that model changes, which breaks FRESH installs while every existing
// database applies cleanly and looks healthy.
//
// # Uniqueness
//
// (tenant_id, scheme, value) is unique, with no WHERE clause. Devices are hard-deleted, so
// there is no "among live devices" to filter on: the foreign key cascades, and a deleted
// device frees its aliases in the same statement. (device_id, scheme) is unique too: a device
// has at most one value per scheme, so replacing a DevEUI is one replace, never two rows
// that both claim to be it. The tenant-leading unique index is also the tenant_id index, so
// the column carries no index of its own.
//
// The device foreign key is added by hand below, not declared as a relation field: a relation
// field would make gorm AutoMigrate the referenced snapshot table too, and a snapshot of
// another table is a claim about that table that this migration has no business making.
//
// No TableName(), for the reason geoFence gives: pinning one on a shape no other shape
// describes would make gorm derive UNPREFIXED index names here while every sibling gets
// prefixed ones.
type deviceAlias struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"not null;size:128;uniqueIndex:uix_device_aliases_tenant_scheme_value,priority:1"`

	DeviceId uint `gorm:"not null;uniqueIndex:uix_device_aliases_device_scheme,priority:1"`

	Scheme string `gorm:"not null;size:70;uniqueIndex:uix_device_aliases_tenant_scheme_value,priority:2;uniqueIndex:uix_device_aliases_device_scheme,priority:2"`
	Value  string `gorm:"not null;size:128;uniqueIndex:uix_device_aliases_tenant_scheme_value,priority:3"`
}

// integrationUnregisteredDevice is the SNAPSHOT of a sighting of a sender-named device the
// tenant has no device for: one row per (integration, scheme, value), upserted with a
// last-seen time and a count, and capped per integration by the API.
//
// The integration foreign key cascades (a deleted integration takes its sightings with it) and
// is added by hand below for the reason deviceAlias gives. tenant_id is carried so the
// catalog-driven tenant purge classifies the table directly.
type integrationUnregisteredDevice struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	IntegrationId uint   `gorm:"not null;uniqueIndex:uix_integration_unregistered_devices_key,priority:1"`
	Scheme        string `gorm:"not null;size:70;uniqueIndex:uix_integration_unregistered_devices_key,priority:2"`
	Value         string `gorm:"not null;size:128;uniqueIndex:uix_integration_unregistered_devices_key,priority:3"`

	FirstSeen time.Time `gorm:"not null"`
	LastSeen  time.Time `gorm:"not null"`
	Count     int64     `gorm:"not null;default:1"`
}

// NewDeviceAliasesSchema creates the device alias table and the unregistered-sightings table
// an inbound integration records into.
//
// This is an APPENDED migration. The baseline is frozen, and these tables are created here,
// after it, which is the same position a fresh install and an upgraded one both put them in.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so this
// must be individually re-runnable. AutoMigrate is: it creates a table only when it is absent
// and adds only missing columns and indexes. Each foreign key is added only when the catalog
// does not already hold it (Postgres has no ADD CONSTRAINT IF NOT EXISTS).
//
// # No seed, and none is wanted
//
// No device has an alias and nothing has been sighted. Nothing here writes a row, so the
// DDL-only bar the baselines are held to holds here too.
//
// # Tenant purge
//
// Both tables carry tenant_id, so the catalog-driven tenant purge classifies them directly
// with no exemption to register. That is checked by the coverage gate that runs alongside
// migration-diff verify.
func NewDeviceAliasesSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261026130000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&deviceAlias{}, &integrationUnregisteredDevice{}); err != nil {
				return err
			}
			if err := addForeignKeyIfAbsent(tx, "device_aliases", "fk_device_aliases_device",
				`ALTER TABLE "device-management"."device_aliases" `+
					`ADD CONSTRAINT fk_device_aliases_device FOREIGN KEY (device_id) `+
					`REFERENCES "device-management"."devices"(id) ON DELETE CASCADE NOT VALID;`); err != nil {
				return err
			}
			return addForeignKeyIfAbsent(tx, "integration_unregistered_devices", "fk_integration_unregistered_devices_integration",
				`ALTER TABLE "device-management"."integration_unregistered_devices" `+
					`ADD CONSTRAINT fk_integration_unregistered_devices_integration FOREIGN KEY (integration_id) `+
					`REFERENCES "device-management"."integrations"(id) ON DELETE CASCADE NOT VALID;`)
		},
		// 🔴 THERE IS NO ROLLBACK, and that is deliberate: dropping the tables would erase every
		// tenant's device aliases, which are how senders find their devices. That is not a state
		// to walk back to by script.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the device aliases migration cannot be rolled back: dropping the tables would erase " +
				"every tenant's device aliases and the unregistered devices its integrations have seen")
		},
	}
}
