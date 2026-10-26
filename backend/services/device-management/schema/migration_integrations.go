// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// integration is the SNAPSHOT of the inbound integration entity as of this migration. It is
// not model.Integration and must never be replaced by it: a migration's shapes are a point in
// time, and pointing one at a live model silently rewrites an already-applied migration
// whenever that model changes — which breaks FRESH installs while every existing database
// applies cleanly and looks healthy.
//
// The mixins (gorm.Model, rdb.TenantScoped, rdb.TokenReference, rdb.NamedEntity,
// rdb.MetadataEntity) are INLINED rather than embedded, matching every other shape in this
// package. Field order matches the live model's embedded layout, because physical column order
// is part of the schema.
//
// 🔴 NO TableName(), for the reason geoFence gives: pinning one on a shape no other shape
// describes would make gorm derive UNPREFIXED index names here while every sibling table gets
// prefixed ones.
//
// token_digest is the keyed digest of the bearer token an integration is authenticated with
// (a salted HMAC, the same construction and the same 128-character width as a provisioning
// profile's provision_secret_digest). Nothing here can hold the token itself.
type integration struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	Type     string `gorm:"not null;size:32"`
	Enabled  bool   `gorm:"not null"`
	Identity string `gorm:"not null;size:128"`
	Generic  *datatypes.JSON

	LinkMetrics bool `gorm:"not null"`

	TokenDigest   string    `gorm:"not null;size:128"`
	TokenIssuedAt time.Time `gorm:"not null"`

	UndecodedCount      int64 `gorm:"not null;default:0"`
	LastUndecodedAt     sql.NullTime
	LastUndecodedSample sql.NullString `gorm:"size:1024"`
	LastAuthFailureAt   sql.NullTime
}

// integrationDeviceType is the SNAPSHOT of an integration's scope: one row per device type
// the integration may resolve devices of. It carries tenant_id so the catalog-driven tenant
// purge classifies it directly.
//
// Its two foreign keys are added by hand below, not declared as relation fields: a relation
// field would make gorm AutoMigrate the referenced snapshot tables too, and a snapshot of
// another table is a claim about that table that this migration has no business making. The
// device type key carries NO cascade, which is what refuses deleting a type an integration is
// scoped to; the integration key cascades, so deleting an integration takes its scope with it.
type integrationDeviceType struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	IntegrationId uint `gorm:"not null;uniqueIndex:uix_integration_device_types_pair,priority:1"`
	DeviceTypeId  uint `gorm:"not null;index;uniqueIndex:uix_integration_device_types_pair,priority:2"`
}

// NewIntegrationsSchema creates the inbound integration tables: integrations, the tenant's
// authenticated senders (a network server or a webhook), and integration_device_types, the
// device types each one may resolve devices of.
//
// This is an APPENDED migration. The baseline is frozen, and these tables are created here,
// after it, which is the same position a fresh install and an upgraded one both put them in.
// Physical order is part of what the golden schema diff compares.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so this
// must be individually re-runnable. AutoMigrate is: it creates a table only when it is absent
// and adds only missing columns. The token index is CREATE UNIQUE INDEX IF NOT EXISTS, and
// each foreign key is added only when the catalog does not already hold it (Postgres has no
// ADD CONSTRAINT IF NOT EXISTS).
//
// # No seed, and none is wanted
//
// A tenant starts with no integrations. Nothing here writes a row, so the DDL-only bar the
// baselines are held to holds here too.
//
// # Tenant purge
//
// Both tables carry tenant_id, so the catalog-driven tenant purge classifies them directly
// with no exemption to register. That is checked by the coverage gate that runs alongside
// migration-diff verify.
func NewIntegrationsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261026120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&integration{}, &integrationDeviceType{}); err != nil {
				return err
			}
			// A token is unique within a tenant among LIVE rows only. The helper is the
			// baseline's deliberate local copy.
			if err := createTenantTokenIndex(tx, &integration{}); err != nil {
				return err
			}
			if err := addForeignKeyIfAbsent(tx, "integration_device_types", "fk_integration_device_types_integration",
				`ALTER TABLE "device-management"."integration_device_types" `+
					`ADD CONSTRAINT fk_integration_device_types_integration FOREIGN KEY (integration_id) `+
					`REFERENCES "device-management"."integrations"(id) ON DELETE CASCADE NOT VALID;`); err != nil {
				return err
			}
			return addForeignKeyIfAbsent(tx, "integration_device_types", "fk_integration_device_types_device_type",
				`ALTER TABLE "device-management"."integration_device_types" `+
					`ADD CONSTRAINT fk_integration_device_types_device_type FOREIGN KEY (device_type_id) `+
					`REFERENCES "device-management"."device_types"(id) ON DELETE RESTRICT NOT VALID;`)
		},
		// 🔴 THERE IS NO ROLLBACK, and that is deliberate: dropping the tables would erase every
		// tenant's integrations and their scopes, and with them the senders' addressing. That
		// is not a state to walk back to by script.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the integrations migration cannot be rolled back: dropping the tables would erase " +
				"every tenant's integrations and their device type scopes")
		},
	}
}
