// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"

	"github.com/devicechain-io/dc-microservice/core"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewCredentialSecretDigestSchema adds device_credentials.secret_digest: the keyed digest a
// device credential's secret (an MQTT password) is stored as, in place of the plaintext
// credential_value held until now.
//
// # Only the column. The digest is written by DigestPlaintextCredentialSecrets, at startup
//
// A digest needs the device secret key, which is derived from the instance root key, and a
// migration chain that needed a key could not be replayed by the tools that replay it with
// none. So this migration is DDL only, and the rows are digested by
// DigestPlaintextCredentialSecrets, which main runs after the chain on EVERY start: an
// old-version pod that writes a plaintext during the rolling upgrade is digested by the
// next start rather than left behind by a one-off backfill.
//
// credential_value is NOT dropped here. Old pods read it during the overlap, and a dropped
// column would fail every credential lookup they make, ACCESS_TOKEN included. It is NULLed
// by the startup step, written NULL by every new-version write (the live model maps it
// write-only, so a rotation an old pod left there cannot outlive a later one), and dropped
// by a later migration together with that mapping.
//
// # Nullable, no default
//
// NULL is "no secret" (a credential type that carries none), as credential_value's NULL was.
// No DEFAULT, for the attmissingval reason migration_profile_location_declaration.go records.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure: the one
// statement is ADD COLUMN IF NOT EXISTS.
func NewCredentialSecretDigestSchema() *gormigrate.Migration {
	const credentials = `"device-management"."device_credentials"`

	return &gormigrate.Migration{
		ID: "20261010130000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + credentials +
				` ADD COLUMN IF NOT EXISTS secret_digest varchar(128);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + credentials +
				` DROP COLUMN IF EXISTS secret_digest;`).Error
		},
	}
}

// credentialSecretRow is a snapshot of the device_credentials columns the digest step
// reads and writes, and the tenant its digest is bound to. It is not the live model: the live model no longer maps
// credential_value at all.
type credentialSecretRow struct {
	ID              uint
	TenantId        string
	CredentialValue sql.NullString
}

func (credentialSecretRow) TableName() string { return "device_credentials" }

// digestBatch is how many plaintext rows one read of the digest step takes.
const digestBatch = 500

// DigestPlaintextCredentialSecrets replaces every plaintext credential_value with its
// digest in secret_digest, and NULLs the plaintext, across every tenant and including
// soft-deleted rows (a deleted credential's password is still a password). It returns how
// many rows it converted.
//
// It runs under a system context: it is instance maintenance, like the migration chain, and
// reads every tenant's rows. Each row is written with a compare-and-set on the plaintext it
// read (id = ? AND credential_value = ?), so replicas starting together cannot undo
// one another: the loser of a race updates nothing, and both would have written a digest of
// the same secret. A stored empty string is cleared to NULL with no digest, which is what an
// empty secret means everywhere else.
//
// 🔴 IT RUNS ON EVERY START, not once. After the first, the read finds nothing and the step
// costs one query. That is what digests a plaintext an old-version pod wrote while the
// rolling upgrade was still in progress.
func DigestPlaintextCredentialSecrets(ctx context.Context, db *gorm.DB, digest func(tenant, secret string) (string, error)) (int, error) {
	db = db.WithContext(core.WithSystemContext(ctx))
	converted := 0
	var after uint
	for {
		var rows []credentialSecretRow
		if err := db.Where("id > ? AND credential_value IS NOT NULL", after).
			Order("id").Limit(digestBatch).Find(&rows).Error; err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		for _, row := range rows {
			after = row.ID
			digested := sql.NullString{}
			if row.CredentialValue.String != "" {
				d, err := digest(row.TenantId, row.CredentialValue.String)
				if err != nil {
					return converted, err
				}
				digested = sql.NullString{String: d, Valid: true}
			}
			// The model carries the primary key, so gorm adds id = ? and the audit journal
			// records which row changed.
			res := db.Model(&credentialSecretRow{ID: row.ID}).
				Where("credential_value = ?", row.CredentialValue.String).
				Updates(map[string]any{"secret_digest": digested, "credential_value": nil})
			if res.Error != nil {
				return converted, res.Error
			}
			converted += int(res.RowsAffected)
		}
	}
}

// CountForeignCredentialDigests counts stored secret digests that do not carry prefix (the
// running key's "v1$<key id>$"): digests no presented secret can match under this key. Any
// at all means the database is not the one this root key's instance wrote — a restore next
// to the wrong root key — and every such credential is refused as misconfigured.
func CountForeignCredentialDigests(ctx context.Context, db *gorm.DB, prefix string) (int64, error) {
	var n int64
	err := db.WithContext(core.WithSystemContext(ctx)).Model(&credentialDigestRow{}).
		Where("secret_digest IS NOT NULL AND secret_digest NOT LIKE ?", prefix+"%").
		Count(&n).Error
	return n, err
}

// credentialDigestRow is a snapshot of the one column CountForeignCredentialDigests reads.
type credentialDigestRow struct {
	ID           uint
	SecretDigest sql.NullString
}

func (credentialDigestRow) TableName() string { return "device_credentials" }
