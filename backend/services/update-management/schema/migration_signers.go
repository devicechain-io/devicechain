// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// SignersID is the signer-registry migration's frozen ID. It is recorded in every database that
// ran it, so it never changes.
const SignersID = "20261013000100"

// artifactSigner is the snapshot of the artifact_signers table: a per-tenant registry of public
// keys whose signatures make an artifact's origin attributable. Only PUBLIC keys are ever stored.
//
// Self-contained down to the mixins, like every snapshot here, and with no TableName method for
// the reason given on artifact: the area's table prefix applies only to a DERIVED name.
//
// There is no deleted_at: a signer is never deleted. Revocation is a terminal state, so the row
// (and every signature that names it) stays as the record of what was once trusted.
type artifactSigner struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	Token        string `gorm:"not null;size:128"`
	Name         string `gorm:"not null;size:128"`
	Algorithm    string `gorm:"not null;size:32"`
	PublicKeyPem string `gorm:"not null;size:4096"`
	// KeyId is the lowercase hex SHA-256 of the key's SubjectPublicKeyInfo DER.
	KeyId     string `gorm:"not null;size:64"`
	State     string `gorm:"not null;size:16"`
	RevokedAt sql.NullTime
	CreatedBy string `gorm:"size:256"`
	RevokedBy string `gorm:"size:256"`
}

// artifactSignature is the snapshot of the artifact_signatures table: one signer's signature over
// one artifact's digest. A row is verified before it is written and is never edited or deleted;
// whether it still counts is decided at read time from the signer's state.
type artifactSignature struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	ArtifactId uint   `gorm:"not null;index"`
	SignerId   uint   `gorm:"not null;index"`
	Signature  []byte `gorm:"not null"`
	Actor      string `gorm:"size:256"`
}

// NewSignersSchema appends the signer registry and the signature table.
//
// It is individually re-runnable (migrations run with UseTransaction:false and replay from the top
// after a failure): AutoMigrate adds only what is missing and every index is CREATE ... IF NOT
// EXISTS.
//
// Three unique indexes carry the invariants the code leans on: a signer's token and its key id are
// each unique within a tenant, and a signer signs a given artifact at most once. Index names are
// written out here against the snapshot, deliberately not through a helper in core.
func NewSignersSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: SignersID,
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&artifactSigner{}, &artifactSignature{}); err != nil {
				return err
			}
			for _, ix := range []struct {
				model   any
				name    string
				columns []string
			}{
				{&artifactSigner{}, "uix_artifact_signers_tenant_token", []string{"tenant_id", "token"}},
				{&artifactSigner{}, "uix_artifact_signers_tenant_key", []string{"tenant_id", "key_id"}},
				{&artifactSignature{}, "uix_artifact_signatures_artifact_signer", []string{"artifact_id", "signer_id"}},
			} {
				if err := createUniqueIndex(tx, ix.model, ix.name, "", ix.columns...); err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("artifact_signatures"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("artifact_signers")
		},
	}
}
