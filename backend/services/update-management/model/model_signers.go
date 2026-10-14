// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Signer lifecycle states.
const (
	// SignerActive: the signer's signatures count toward an artifact's trust.
	SignerActive = "ACTIVE"
	// SignerRevoked: terminal. The signer's signatures stay on record and stop counting.
	SignerRevoked = "REVOKED"
)

// Signature algorithms a signer's key may use.
const (
	AlgorithmECDSAP256 = "ECDSA_P256"
	AlgorithmEd25519   = "ED25519"
)

// Derived artifact trust, computed at read time from the signatures on record and the state of
// the signers that made them. It is never stored: revoking a signer changes what every artifact
// it signed reports without writing a single artifact row.
const (
	// TrustSigned: at least one signature by an ACTIVE signer verified against the digest.
	TrustSigned = "SIGNED"
)

// ArtifactSigner is a tenant-registered PUBLIC key whose signatures attribute an artifact to a
// party the tenant trusts. The private half never reaches the platform.
//
// Field order mirrors the migration's snapshot struct; physical column order is part of the schema.
type ArtifactSigner struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	rdb.TokenReference

	Name      string `gorm:"not null;size:128"`
	Algorithm string `gorm:"not null;size:32"`
	// PublicKeyPem is the canonical SPKI PEM of the public key.
	PublicKeyPem string `gorm:"not null;size:4096"`
	// KeyId is the lowercase hex SHA-256 of the key's SPKI DER, unique per tenant.
	KeyId     string `gorm:"not null;size:64"`
	State     string `gorm:"not null;size:16"`
	RevokedAt sql.NullTime
	CreatedBy string `gorm:"size:256"`
	RevokedBy string `gorm:"size:256"`
}

// DefaultOrder implements rdb.Sortable: newest first, token as the tiebreak.
func (ArtifactSigner) DefaultOrder() string {
	return "artifact_signers.created_at DESC, artifact_signers.token ASC"
}

// ArtifactSignature is one signer's verified signature over one artifact's digest. Rows are
// written only after the signature verified, and are never edited or deleted.
type ArtifactSignature struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	rdb.TenantScoped

	ArtifactId uint   `gorm:"not null;index"`
	SignerId   uint   `gorm:"not null;index"`
	Signature  []byte `gorm:"not null"`
	Actor      string `gorm:"size:256"`
}

// AuditLabel names the row in the audit journal.
func (s ArtifactSignature) AuditLabel() string {
	return fmt.Sprintf("artifact:%d signer:%d", s.ArtifactId, s.SignerId)
}

// SignerSearchResults is a page of signers plus its pagination info.
type SignerSearchResults struct {
	Results    []ArtifactSigner
	Pagination rdb.SearchResultsPagination
}
