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

// deviceTrustAnchor is the SNAPSHOT of a tenant's X.509 trust anchor as of this migration. It
// is not model.DeviceTrustAnchor and must never be replaced by it: a migration's shapes are a
// point in time, and pointing one at a live model silently rewrites an already-applied
// migration whenever that model changes, which breaks FRESH installs while every existing
// database applies cleanly and looks healthy.
//
// The mixins are INLINED rather than embedded, matching every other shape in this package, and
// the field order matches the live model's embedded layout because physical column order is
// part of the schema.
//
// 🔴 NO TableName(), for the reason geoFence gives: pinning one on a shape no other shape
// describes would make gorm derive UNPREFIXED index names here while every sibling table gets
// prefixed ones.
//
// An anchor is PUBLIC material (one CA certificate), so it is stored as plain text: envelope
// encryption would protect nothing.
type deviceTrustAnchor struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Metadata *datatypes.JSON

	CertificatePem    string    `gorm:"not null;type:text"`
	FingerprintSha256 string    `gorm:"not null;size:64"`
	Subject           string    `gorm:"not null;size:1024"`
	Issuer            string    `gorm:"not null;size:1024"`
	NotBefore         time.Time `gorm:"not null"`
	NotAfter          time.Time `gorm:"not null"`
	Enabled           bool      `gorm:"not null"`
}

// NewDeviceTrustAnchorsSchema is the schema half of X.509 client-certificate authentication:
// the tenant's trust anchors, and the columns that bind a credential and a provisioning profile
// to one.
//
// # What it changes
//
//  1. device_trust_anchors: one row per CA certificate a tenant trusts to vouch for its devices.
//     A token is unique within a tenant among live rows, and so is the certificate's
//     fingerprint, so the same CA cannot be registered twice.
//  2. device_credentials.trust_anchor_id: the anchor an X509_CERTIFICATE credential's leaf must
//     chain to at every connect. ON DELETE RESTRICT, so an anchor that credentials rely on
//     cannot be deleted out from under them.
//  3. device_credentials.certificate_pem: the pinned leaf, for display and audit. Public.
//  4. provisioning_profiles.trust_anchor_id: the anchor an X.509 profile authenticates its
//     devices against. ON DELETE RESTRICT.
//
// # Nullable, no default, no backfill
//
// The new columns are NULL for every existing row, which is the truth: no credential or profile
// was ever bound to an anchor. A credential of the retired X509_CERTIFICATE type that predates
// this migration has a NULL anchor and never authenticates. A default would put a value in the
// catalog's attmissingval (see migration_profile_location_declaration.go).
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. AutoMigrate
// creates the table only when it is absent; the indexes are IF NOT EXISTS; each column is
// ADD COLUMN IF NOT EXISTS; and each foreign key is added only when the catalog does not hold
// it yet, then validated (the same NOT VALID, then VALIDATE, order migration_self_registration.go
// explains).
//
// # DDL only
//
// No row is seeded or written, so the DDL-only bar the baselines are held to holds here too.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies it directly with no
// exemption to register.
func NewDeviceTrustAnchorsSchema() *gormigrate.Migration {
	const (
		anchors     = `"device-management"."device_trust_anchors"`
		credentials = `"device-management"."device_credentials"`
		profiles    = `"device-management"."provisioning_profiles"`
	)
	return &gormigrate.Migration{
		ID: "20261027140000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&deviceTrustAnchor{}); err != nil {
				return err
			}
			if err := createTenantTokenIndex(tx, &deviceTrustAnchor{}); err != nil {
				return err
			}
			if err := createPartialUniqueIndexWhere(tx, &deviceTrustAnchor{},
				"uix_device_trust_anchors_tenant_fingerprint", "deleted_at IS NULL",
				"tenant_id", "fingerprint_sha256"); err != nil {
				return err
			}
			for _, stmt := range []string{
				`ALTER TABLE ` + credentials + ` ADD COLUMN IF NOT EXISTS trust_anchor_id bigint;`,
				`ALTER TABLE ` + credentials + ` ADD COLUMN IF NOT EXISTS certificate_pem text;`,
				`CREATE INDEX IF NOT EXISTS idx_device_credentials_trust_anchor ON ` + credentials +
					` (trust_anchor_id) WHERE trust_anchor_id IS NOT NULL;`,
				`ALTER TABLE ` + profiles + ` ADD COLUMN IF NOT EXISTS trust_anchor_id bigint;`,
				`CREATE INDEX IF NOT EXISTS idx_provisioning_profiles_trust_anchor ON ` + profiles +
					` (trust_anchor_id) WHERE trust_anchor_id IS NOT NULL;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			if err := addForeignKeyIfAbsent(tx, "device_credentials", "fk_device_credentials_trust_anchor",
				`ALTER TABLE `+credentials+` ADD CONSTRAINT fk_device_credentials_trust_anchor `+
					`FOREIGN KEY (trust_anchor_id) REFERENCES `+anchors+`(id) ON DELETE RESTRICT NOT VALID;`); err != nil {
				return err
			}
			return addForeignKeyIfAbsent(tx, "provisioning_profiles", "fk_provisioning_profiles_trust_anchor",
				`ALTER TABLE `+profiles+` ADD CONSTRAINT fk_provisioning_profiles_trust_anchor `+
					`FOREIGN KEY (trust_anchor_id) REFERENCES `+anchors+`(id) ON DELETE RESTRICT NOT VALID;`)
		},
		// 🔴 THERE IS NO ROLLBACK, and that is deliberate: dropping the table would erase every
		// tenant's trust anchors, and with them the only record of which CA each certificate
		// credential chains to. That is not a state to walk back to by script.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the device trust anchors migration cannot be rolled back: dropping the table " +
				"would erase every tenant's trust anchors and the binding of each certificate credential to its CA")
		},
	}
}
