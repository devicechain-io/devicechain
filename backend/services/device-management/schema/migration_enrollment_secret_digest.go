// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewEnrollmentSecretDigestSchema adds provisioning_profiles.provision_secret_digest and
// device_claims.claim_secret_digest: the keyed digests a provisioning profile's shared
// secret and a device claim's one-time secret are stored as, in place of the plaintext
// provision_secret and claim_secret held until now. It follows
// NewCredentialSecretDigestSchema, which did the same for a device credential's password.
//
// # Only DDL. The digests are written by DigestPlaintextProvisionSecrets and
// # DigestPlaintextClaimSecrets, at startup
//
// A digest needs the enrollment secret key, which is derived from the instance root key,
// and a migration chain that needed a key could not be replayed by the tools that replay it
// with none. So this migration cannot convert the rows it leaves behind: they are digested
// by those two steps, which main runs after the chain on EVERY start, so a plaintext an
// old-version pod writes while a rolling upgrade is in progress is digested by the next
// start rather than left behind by a one-off backfill.
//
// An unredeemed (OPEN) claim therefore keeps working across the upgrade: its digest is made
// from the same secret, so the owner presents the secret they were given. A CLAIMED or
// CANCELED claim has its plaintext digested and cleared too, for the secret was never
// anything to keep.
//
// # The plaintext columns are not dropped here, only made nullable
//
// Old pods INSERT and UPDATE those columns during the overlap, and a dropped column would
// fail every write they make. The live models map them write-only, so every new-version
// write NULLs them, and the startup steps NULL what they digest. NOT NULL goes because the
// new writes carry NULL. A later migration drops the columns together with that mapping,
// once no pod that writes them is left.
//
// # Nullable, no default
//
// NULL is "no secret": a row whose plaintext was empty matches no presented secret, as it
// already did. No DEFAULT, for the attmissingval reason
// migration_profile_location_declaration.go records.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. ADD
// COLUMN IF NOT EXISTS and DROP NOT NULL are both safe to repeat.
func NewEnrollmentSecretDigestSchema() *gormigrate.Migration {
	const (
		profiles = `"device-management"."provisioning_profiles"`
		claims   = `"device-management"."device_claims"`
	)

	return &gormigrate.Migration{
		ID: "20261024120000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE ` + profiles + ` ADD COLUMN IF NOT EXISTS provision_secret_digest varchar(128);`,
				`ALTER TABLE ` + profiles + ` ALTER COLUMN provision_secret DROP NOT NULL;`,
				`ALTER TABLE ` + claims + ` ADD COLUMN IF NOT EXISTS claim_secret_digest varchar(128);`,
				`ALTER TABLE ` + claims + ` ALTER COLUMN claim_secret DROP NOT NULL;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// 🔴 THERE IS NO ROLLBACK, AND THAT IS DELIBERATE. By the time anyone rolls this back the
		// startup steps have NULLed the plaintext and the digest columns are the ONLY copy of
		// every profile and claim secret: dropping them destroys the secrets with no way to
		// recover them from the database, and a digest cannot be turned back into a secret.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the enrollment secret digest migration cannot be rolled back: the " +
				"plaintext secrets have been replaced by their digests, so dropping the digest " +
				"columns would destroy every provisioning profile secret and device claim secret")
		},
	}
}

// provisionSecretRow is a snapshot of the provisioning_profiles columns the digest step reads
// and writes, and the tenant its digest is bound to. It is not the live model: the live
// model maps provision_secret write-only.
type provisionSecretRow struct {
	ID              uint
	TenantId        string
	ProvisionSecret sql.NullString
}

func (provisionSecretRow) TableName() string { return "provisioning_profiles" }

// DigestPlaintextProvisionSecrets replaces every plaintext provision_secret with its digest
// in provision_secret_digest, and NULLs the plaintext, across every tenant and including
// soft-deleted rows (a deleted profile's secret is still a secret). It returns how many rows
// it converted.
//
// It runs under a system context: it is instance maintenance, like the migration chain, and
// reads every tenant's rows. Each row is written with a compare-and-set on the plaintext it
// read (id = ? AND provision_secret = ?), so replicas starting together cannot undo one
// another: the loser of a race updates nothing, and both would have written a digest of the
// same secret. A stored empty or blank secret has its plaintext cleared to NULL and its digest
// left alone: create has refused a blank one since v0.18.0, a profile that holds no digest
// matches nothing, and one that holds a digest keeps it.
//
// 🔴 IT RUNS ON EVERY START, not once. After the first, the read finds nothing and the step
// costs one query. That is what digests a plaintext an old-version pod wrote while the
// rolling upgrade was still in progress.
func DigestPlaintextProvisionSecrets(ctx context.Context, db *gorm.DB, digest func(tenant, secret string) (string, error)) (int, error) {
	db = db.WithContext(core.WithSystemContext(ctx))
	converted := 0
	var after uint
	for {
		var rows []provisionSecretRow
		if err := db.Where("id > ? AND provision_secret IS NOT NULL", after).
			Order("id").Limit(digestBatch).Find(&rows).Error; err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		for _, row := range rows {
			after = row.ID
			// A blank plaintext is NOT a secret to digest. An old-version pod's update that does
			// not name the secret reads the column as "" and saves it back blank, over a row whose
			// digest a new pod wrote; clearing the digest for that would erase a working secret.
			// So a blank only clears the plaintext column, and the digest is left as it is (NULL
			// when there never was one, which is what a blank has always meant).
			updates := map[string]any{"provision_secret": nil}
			if strings.TrimSpace(row.ProvisionSecret.String) != "" {
				d, err := digest(row.TenantId, row.ProvisionSecret.String)
				if err != nil {
					return converted, err
				}
				updates["provision_secret_digest"] = sql.NullString{String: d, Valid: true}
			}
			// The model carries the primary key, so gorm adds id = ? and the audit journal
			// records which row changed.
			res := db.Model(&provisionSecretRow{ID: row.ID}).
				Where("provision_secret = ?", row.ProvisionSecret.String).
				Updates(updates)
			if res.Error != nil {
				return converted, res.Error
			}
			converted += int(res.RowsAffected)
		}
	}
}

// CountForeignProvisionSecretDigests counts stored provisioning secret digests that do not
// carry prefix (the running key's "v1$<key id>$"): digests no presented secret can match
// under this key. Any at all means the database is not the one this root key's instance
// wrote, and every such profile refuses every device until its secret is set again.
func CountForeignProvisionSecretDigests(ctx context.Context, db *gorm.DB, prefix string) (int64, error) {
	var n int64
	err := db.WithContext(core.WithSystemContext(ctx)).Model(&provisionDigestRow{}).
		Where("provision_secret_digest IS NOT NULL AND provision_secret_digest NOT LIKE ?", prefix+"%").
		Count(&n).Error
	return n, err
}

// provisionDigestRow is a snapshot of the one column CountForeignProvisionSecretDigests reads.
type provisionDigestRow struct {
	ID                    uint
	ProvisionSecretDigest sql.NullString
}

func (provisionDigestRow) TableName() string { return "provisioning_profiles" }

// claimSecretRow is a snapshot of the device_claims columns the digest step reads and writes,
// and the tenant its digest is bound to. It is not the live model: the live model maps
// claim_secret write-only.
type claimSecretRow struct {
	ID          uint
	TenantId    string
	ClaimSecret sql.NullString
}

func (claimSecretRow) TableName() string { return "device_claims" }

// DigestPlaintextClaimSecrets replaces every plaintext claim_secret with its digest in
// claim_secret_digest, and NULLs the plaintext, across every tenant and every claim status
// and including soft-deleted rows. It returns how many rows it converted.
//
// It is DigestPlaintextProvisionSecrets for device claims, and works the same way: a system
// context, a compare-and-set on the plaintext it read so replicas starting together cannot
// undo one another, an empty or blank stored secret cleared to NULL with the digest left alone
// (nothing redeems a claim with none), and a run on EVERY start so a claim an old-version pod opened
// during a rolling upgrade is digested by the next one.
func DigestPlaintextClaimSecrets(ctx context.Context, db *gorm.DB, digest func(tenant, secret string) (string, error)) (int, error) {
	db = db.WithContext(core.WithSystemContext(ctx))
	converted := 0
	var after uint
	for {
		var rows []claimSecretRow
		if err := db.Where("id > ? AND claim_secret IS NOT NULL", after).
			Order("id").Limit(digestBatch).Find(&rows).Error; err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		for _, row := range rows {
			after = row.ID
			// A blank only clears the plaintext and leaves the digest alone, for the reason
			// DigestPlaintextProvisionSecrets gives.
			updates := map[string]any{"claim_secret": nil}
			if strings.TrimSpace(row.ClaimSecret.String) != "" {
				d, err := digest(row.TenantId, row.ClaimSecret.String)
				if err != nil {
					return converted, err
				}
				updates["claim_secret_digest"] = sql.NullString{String: d, Valid: true}
			}
			res := db.Model(&claimSecretRow{ID: row.ID}).
				Where("claim_secret = ?", row.ClaimSecret.String).
				Updates(updates)
			if res.Error != nil {
				return converted, res.Error
			}
			converted += int(res.RowsAffected)
		}
	}
}

// CountForeignClaimSecretDigests counts stored claim secret digests that do not carry prefix
// (the running key's "v1$<key id>$"): digests no presented secret can match under this key.
func CountForeignClaimSecretDigests(ctx context.Context, db *gorm.DB, prefix string) (int64, error) {
	var n int64
	err := db.WithContext(core.WithSystemContext(ctx)).Model(&claimDigestRow{}).
		Where("claim_secret_digest IS NOT NULL AND claim_secret_digest NOT LIKE ?", prefix+"%").
		Count(&n).Error
	return n, err
}

// claimDigestRow is a snapshot of the one column CountForeignClaimSecretDigests reads.
type claimDigestRow struct {
	ID                uint
	ClaimSecretDigest sql.NullString
}

func (claimDigestRow) TableName() string { return "device_claims" }
