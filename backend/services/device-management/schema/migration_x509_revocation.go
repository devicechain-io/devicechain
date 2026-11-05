// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// The shapes below are the SNAPSHOT of the X.509 revocation store as of this migration. They are
// not the model package's types and must never be replaced by them: a migration's shapes are a
// point in time, and pointing one at a live model silently rewrites an already-applied
// migration whenever that model changes, which breaks FRESH installs while every existing
// database applies cleanly and looks healthy (see migration_device_trust_anchors.go).
//
// The mixins are INLINED, the field order is the live layout, and there is NO TableName() (the
// index-prefix trap migration_device_trust_anchors.go describes). No shape declares a foreign
// key by tag: every constraint is added below by name, only when the catalog does not hold it.

// deviceRevocationIssuer is one row per (anchor, issuer identity), NEVER deleted: it carries the
// highest CRL number ever accepted for that issuer, so deleting the active list and uploading an
// older one is still a rollback. Every list write for the issuer locks this row.
type deviceRevocationIssuer struct {
	ID                 uint   `gorm:"primarykey"`
	TenantId           string `gorm:"index;not null;size:128"`
	TrustAnchorId      uint   `gorm:"not null"`
	IssuerKey          string `gorm:"not null;size:64"`
	IssuerName         string `gorm:"not null;size:64"`
	HighWaterCrlNumber string `gorm:"not null;type:numeric(50,0)"`
}

// deviceRevocationList is the one CURRENT CRL per (anchor, issuer identity).
type deviceRevocationList struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId             string    `gorm:"index;not null;size:128"`
	TrustAnchorId        uint      `gorm:"not null"`
	RevocationIssuerId   uint      `gorm:"not null"`
	IssuerKey            string    `gorm:"not null;size:64"`
	IssuerName           string    `gorm:"not null;size:64"`
	IssuerCertificatePem string    `gorm:"not null;type:text"`
	IssuerSubject        string    `gorm:"not null;size:1024"`
	CrlNumber            string    `gorm:"not null;type:numeric(50,0)"`
	ThisUpdate           time.Time `gorm:"not null"`
	NextUpdate           time.Time `gorm:"not null"`
	EntryCount           int32     `gorm:"not null;type:integer"`
	CrlSha256            string    `gorm:"not null;size:64"`
	Source               string    `gorm:"not null;size:16"`
}

// deviceRevokedCertificate is one revoked certificate: a MANUAL entry (by fingerprint, DeviceChain
// being the authority for its own pins) or a CRL entry (by issuer and serial, materialised from a
// stored list). The CHECK below keeps the two shapes apart.
type deviceRevokedCertificate struct {
	ID               uint   `gorm:"primarykey"`
	TenantId         string `gorm:"index;not null;size:128"`
	TrustAnchorId    uint   `gorm:"not null"`
	Kind             string `gorm:"not null;size:16"`
	RevocationListId *uint
	Fingerprint      *string   `gorm:"size:64"`
	IssuerKey        *string   `gorm:"size:64"`
	IssuerName       *string   `gorm:"size:64"`
	Serial           *string   `gorm:"size:64"`
	Reason           string    `gorm:"not null;size:32"`
	RevokedAt        time.Time `gorm:"not null"`
	CreatedAt        time.Time
	CreatedBy        *string `gorm:"size:255"`
}

// deviceCredentialChainLink is a path a credential's certificate is KNOWN to have used when it
// was created or registered: a HINT for finding live sessions to end, never read by the connect
// path and never used to admit anything. Depth 0 is the leaf.
type deviceCredentialChainLink struct {
	CredentialId  uint   `gorm:"primaryKey;autoIncrement:false"`
	Depth         int16  `gorm:"primaryKey;autoIncrement:false;type:smallint"`
	TenantId      string `gorm:"index;not null;size:128"`
	TrustAnchorId uint   `gorm:"not null"`
	Fingerprint   string `gorm:"not null;size:64"`
	// IssuerKey is NULL only on a legacy depth-0 link, whose issuer's key is not in the leaf.
	IssuerKey  *string `gorm:"size:64"`
	IssuerName string  `gorm:"not null;size:64"`
	Serial     string  `gorm:"not null;size:64"`
}

// NewX509RevocationSchema is the schema half of X.509 certificate revocation: one revocation
// store per tenant trust anchor, with two sources (signed CRLs and DeviceChain's own manual
// entries) and the links that let a later slice find the live sessions of a revoked certificate.
//
// # What it changes
//
//  1. device_trust_anchors gains revocation_mode (NULL reads as ENTRIES), revocation_stale_grace
//     (seconds, NULL reads as the fixed default) and revocation_rows (lists + manual entries,
//     NULL reads as 0), all nullable with no default.
//  2. pending_session_kicks gains not_before (nullable): the instant a re-sweep row may first be
//     drained.
//  3. device_revocation_issuers, device_revocation_lists, device_revoked_certificates and
//     device_credential_chain_links are created, with their partial indexes and constraints.
//
// # Nullable, no default, no backfill
//
// Every new column on an existing table is NULL for every existing row, which is the truth:
// nothing was ever revoked, and an old replica that does not know the columns keeps writing rows
// they accept. A default would put a value in the catalog's attmissingval (see
// migration_profile_location_declaration.go). The links of credentials created before this
// migration are NOT backfilled here (no data in DDL migrations): the model's reconciler writes
// their depth-0 link from the stored leaf, idempotently, per tenant.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. AutoMigrate
// creates each table only when it is absent; each index is IF NOT EXISTS; each column is
// ADD COLUMN IF NOT EXISTS; each constraint is added only when the catalog does not hold it yet,
// then validated (the NOT VALID, then VALIDATE order migration_self_registration.go explains).
//
// # Foreign keys
//
// Every key into device_trust_anchors, and the list's key into its issuer, is ON DELETE
// RESTRICT: revocation data is never silently dropped with its parent. A list's entries cascade
// with the list, and a credential's links with the credential.
//
// # DDL only
//
// No row is seeded or written.
//
// # Tenant purge
//
// Every table carries tenant_id, so the catalog-driven tenant purge classifies it directly with
// no exemption to register.
func NewX509RevocationSchema() *gormigrate.Migration {
	const (
		anchors     = `"device-management"."device_trust_anchors"`
		kicks       = `"device-management"."pending_session_kicks"`
		issuers     = `"device-management"."device_revocation_issuers"`
		lists       = `"device-management"."device_revocation_lists"`
		entries     = `"device-management"."device_revoked_certificates"`
		links       = `"device-management"."device_credential_chain_links"`
		credentials = `"device-management"."device_credentials"`
	)
	return &gormigrate.Migration{
		ID: "20261105100000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE ` + anchors + ` ADD COLUMN IF NOT EXISTS revocation_mode varchar(32);`,
				`ALTER TABLE ` + anchors + ` ADD COLUMN IF NOT EXISTS revocation_stale_grace bigint;`,
				`ALTER TABLE ` + anchors + ` ADD COLUMN IF NOT EXISTS revocation_rows bigint;`,
				`ALTER TABLE ` + kicks + ` ADD COLUMN IF NOT EXISTS not_before timestamptz;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			if err := tx.AutoMigrate(&deviceRevocationIssuer{}, &deviceRevocationList{},
				&deviceRevokedCertificate{}, &deviceCredentialChainLink{}); err != nil {
				return err
			}
			if err := createPartialUniqueIndexWhere(tx, &deviceRevocationList{},
				"uix_device_revocation_lists_identity", "deleted_at IS NULL",
				"tenant_id", "trust_anchor_id", "issuer_key", "issuer_name"); err != nil {
				return err
			}
			for _, stmt := range []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_device_revocation_issuers_identity ON ` + issuers +
					` (tenant_id, trust_anchor_id, issuer_key, issuer_name);`,
				`CREATE INDEX IF NOT EXISTS ix_device_revocation_lists_anchor ON ` + lists +
					` (trust_anchor_id);`,
				`CREATE INDEX IF NOT EXISTS ix_device_revoked_certificates_fingerprint ON ` + entries +
					` (tenant_id, trust_anchor_id, fingerprint) WHERE fingerprint IS NOT NULL;`,
				`CREATE INDEX IF NOT EXISTS ix_device_revoked_certificates_serial ON ` + entries +
					` (tenant_id, trust_anchor_id, issuer_key, issuer_name, serial) WHERE serial IS NOT NULL;`,
				`CREATE INDEX IF NOT EXISTS ix_device_revoked_certificates_list ON ` + entries +
					` (revocation_list_id) WHERE revocation_list_id IS NOT NULL;`,
				`CREATE INDEX IF NOT EXISTS ix_device_credential_chain_links_fingerprint ON ` + links +
					` (tenant_id, trust_anchor_id, fingerprint);`,
				`CREATE INDEX IF NOT EXISTS ix_device_credential_chain_links_issuer_serial ON ` + links +
					` (tenant_id, trust_anchor_id, issuer_name, serial);`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			for _, c := range []struct{ table, name, stmt string }{
				{"device_revocation_issuers", "fk_device_revocation_issuers_trust_anchor",
					`ALTER TABLE ` + issuers + ` ADD CONSTRAINT fk_device_revocation_issuers_trust_anchor ` +
						`FOREIGN KEY (trust_anchor_id) REFERENCES ` + anchors + `(id) ON DELETE RESTRICT NOT VALID;`},
				{"device_revocation_lists", "fk_device_revocation_lists_trust_anchor",
					`ALTER TABLE ` + lists + ` ADD CONSTRAINT fk_device_revocation_lists_trust_anchor ` +
						`FOREIGN KEY (trust_anchor_id) REFERENCES ` + anchors + `(id) ON DELETE RESTRICT NOT VALID;`},
				{"device_revocation_lists", "fk_device_revocation_lists_issuer",
					`ALTER TABLE ` + lists + ` ADD CONSTRAINT fk_device_revocation_lists_issuer ` +
						`FOREIGN KEY (revocation_issuer_id) REFERENCES ` + issuers + `(id) ON DELETE RESTRICT NOT VALID;`},
				{"device_revoked_certificates", "fk_device_revoked_certificates_trust_anchor",
					`ALTER TABLE ` + entries + ` ADD CONSTRAINT fk_device_revoked_certificates_trust_anchor ` +
						`FOREIGN KEY (trust_anchor_id) REFERENCES ` + anchors + `(id) ON DELETE RESTRICT NOT VALID;`},
				{"device_revoked_certificates", "fk_device_revoked_certificates_list",
					`ALTER TABLE ` + entries + ` ADD CONSTRAINT fk_device_revoked_certificates_list ` +
						`FOREIGN KEY (revocation_list_id) REFERENCES ` + lists + `(id) ON DELETE CASCADE NOT VALID;`},
				{"device_revoked_certificates", "ck_device_revoked_certificates_kind",
					`ALTER TABLE ` + entries + ` ADD CONSTRAINT ck_device_revoked_certificates_kind CHECK (` +
						`(kind = 'MANUAL' AND fingerprint IS NOT NULL AND serial IS NULL AND revocation_list_id IS NULL) OR ` +
						`(kind = 'CRL' AND fingerprint IS NULL AND issuer_key IS NOT NULL AND issuer_name IS NOT NULL ` +
						`AND serial IS NOT NULL AND revocation_list_id IS NOT NULL)) NOT VALID;`},
				{"device_credential_chain_links", "fk_device_credential_chain_links_credential",
					`ALTER TABLE ` + links + ` ADD CONSTRAINT fk_device_credential_chain_links_credential ` +
						`FOREIGN KEY (credential_id) REFERENCES ` + credentials + `(id) ON DELETE CASCADE NOT VALID;`},
				{"device_credential_chain_links", "fk_device_credential_chain_links_trust_anchor",
					`ALTER TABLE ` + links + ` ADD CONSTRAINT fk_device_credential_chain_links_trust_anchor ` +
						`FOREIGN KEY (trust_anchor_id) REFERENCES ` + anchors + `(id) ON DELETE RESTRICT NOT VALID;`},
			} {
				// addForeignKeyIfAbsent asks the catalog for any constraint of that name and then
				// validates it, which is exactly right for the CHECK as well.
				if err := addForeignKeyIfAbsent(tx, c.table, c.name, c.stmt); err != nil {
					return err
				}
			}
			return nil
		},
		// 🔴 THERE IS NO ROLLBACK, and that is deliberate: dropping the tables would erase every
		// revocation a tenant recorded, and with them the high-water marks that stop an older CRL
		// from being replayed to un-revoke a certificate. That is not a state to walk back to by
		// script.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the X.509 revocation migration cannot be rolled back: dropping the tables " +
				"would erase every recorded revocation and the CRL numbers that refuse a replayed older list")
		},
	}
}
