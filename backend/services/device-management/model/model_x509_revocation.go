// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// The revocation store of a tenant's trust anchor: one row per issuer whose CRL numbers are
// tracked (DeviceRevocationIssuer), the current CRL per issuer (DeviceRevocationList), and the
// revoked certificates themselves (DeviceRevokedCertificate), from two sources: a stored CRL, and
// DeviceChain's own manual entries. DeviceCredentialChainLink records the path a credential's
// certificate was created through, to find live sessions to end.
//
// 🔴 EVERY ROW HANGS OFF A TENANT ANCHOR and is read with an explicit `tenant_id = ? AND
// trust_anchor_id = ?`, not only the rdb tenant callback (which covers neither raw SQL nor a
// projection destination): see readRevocationState.

// Sources of a stored list, and kinds of a revoked-certificate row.
const (
	RevocationSourceUpload = "UPLOAD"
	RevocationSourceFetch  = "FETCH"

	RevokedKindCRL    = "CRL"
	RevokedKindManual = "MANUAL"
)

// DeviceRevocationIssuer is one row per (anchor, issuer identity), never deleted: it carries the
// highest CRL number ever accepted for the issuer, so deleting the active list and uploading an
// older one is still a rollback. Every list write for the issuer locks this row.
type DeviceRevocationIssuer struct {
	ID            uint
	TenantId      string `gorm:"index;not null;size:128"`
	TrustAnchorId uint   `gorm:"not null"`
	IssuerKey     string `gorm:"not null;size:64"`
	IssuerName    string `gorm:"not null;size:64"`
	// HighWaterCrlNumber is the decimal text of the highest CRL number accepted (up to 20 octets).
	HighWaterCrlNumber string `gorm:"not null;type:numeric(50,0)"`
}

// AuditExempt: the row is bookkeeping derived from a list write the journal already records.
func (DeviceRevocationIssuer) AuditExempt() bool { return true }

// DeviceRevocationList is the one CURRENT CRL per (anchor, issuer identity).
type DeviceRevocationList struct {
	gorm.Model
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

// IssuerID is the identity the list covers.
func (l DeviceRevocationList) IssuerID() x509trust.IssuerID {
	return x509trust.IssuerID{Key: l.IssuerKey, Name: l.IssuerName}
}

// DeviceRevokedCertificate is one revoked certificate. A MANUAL row names a certificate by
// fingerprint and applies to any certificate on a verified path (leaf or intermediate); a CRL row
// names it by issuer and serial and belongs to the list that carries it.
type DeviceRevokedCertificate struct {
	ID               uint
	TenantId         string  `gorm:"index;not null;size:128"`
	TrustAnchorId    uint    `gorm:"not null"`
	Kind             string  `gorm:"not null;size:16"`
	RevocationListId *uint   `gorm:"index"`
	Fingerprint      *string `gorm:"size:64"`
	IssuerKey        *string `gorm:"size:64"`
	IssuerName       *string `gorm:"size:64"`
	Serial           *string `gorm:"size:64"`
	Reason           string  `gorm:"not null;size:32"`
	RevokedAt        time.Time
	CreatedAt        time.Time
	// CreatedBy is the actor of a MANUAL entry, for the journal. CRL rows carry none.
	CreatedBy *string `gorm:"size:255"`
}

// AuditLabel is the certificate's fingerprint: public, and what an auditor looks a revocation up by.
// A CRL-derived row carries none and is labelled by its issuer's name hash.
func (r DeviceRevokedCertificate) AuditLabel() string {
	if r.Fingerprint != nil {
		return *r.Fingerprint
	}
	if r.IssuerName != nil {
		return *r.IssuerName
	}
	return ""
}

// 🔴 THE TYPE IS JOURNALED (a MANUAL entry is an operator's decision), and the audit exemption is a
// property of the TYPE, not the row. The bulk insert of a CRL's entries (a later slice) therefore
// goes through its own type over the same table that opts out, rather than journaling hundreds
// of thousands of derived rows: the list's own row carries the CRL's hash and count.

// DeviceCredentialChainLink is a path a credential's certificate is KNOWN to have used when it
// was created or registered: a HINT for finding live sessions to end, never read by the connect
// path and never used to admit anything. Depth 0 is the leaf.
type DeviceCredentialChainLink struct {
	CredentialId  uint   `gorm:"primaryKey;autoIncrement:false"`
	Depth         int16  `gorm:"primaryKey;autoIncrement:false"`
	TenantId      string `gorm:"index;not null;size:128"`
	TrustAnchorId uint   `gorm:"not null"`
	Fingerprint   string `gorm:"not null;size:64"`
	// IssuerKey is nil only on a legacy depth-0 link (written from the stored leaf, whose
	// issuer's key is not derivable from it).
	IssuerKey  *string `gorm:"size:64"`
	IssuerName string  `gorm:"not null;size:64"`
	Serial     string  `gorm:"not null;size:64"`
}

// AuditExempt: links are derived from the credential they hang off.
func (DeviceCredentialChainLink) AuditExempt() bool { return true }

// The anchor's revocation settings, read from the columns the migration added. NULL reads as
// the default, and a value outside the vocabulary is an ERROR, never a default: a mode this
// build does not know is refused rather than guessed.

// DefaultRevocationStaleGrace is how long past nextUpdate a list still counts as current under
// REQUIRE_CURRENT when the anchor sets none.
const DefaultRevocationStaleGrace = 24 * time.Hour

// MaxRevocationStaleGrace bounds an anchor's configured grace.
const MaxRevocationStaleGrace = 7 * 24 * time.Hour

// RevocationModeOf is the anchor's mode: ENTRIES when none is stored.
func (a *DeviceTrustAnchor) RevocationModeOf() (x509trust.Mode, error) {
	if !a.RevocationMode.Valid {
		return x509trust.ModeEntries, nil
	}
	switch m := x509trust.Mode(a.RevocationMode.String); m {
	case x509trust.ModeEntries, x509trust.ModeRequireCurrent:
		return m, nil
	default:
		return "", fmt.Errorf("trust anchor %q stores the unknown revocation mode %q", a.Token, a.RevocationMode.String)
	}
}

// RevocationStaleGraceOf is the anchor's grace: the default when none is stored.
func (a *DeviceTrustAnchor) RevocationStaleGraceOf() (time.Duration, error) {
	if !a.RevocationStaleGrace.Valid {
		return DefaultRevocationStaleGrace, nil
	}
	secs := a.RevocationStaleGrace.Int64
	if secs < 0 || time.Duration(secs) > MaxRevocationStaleGrace/time.Second {
		return 0, fmt.Errorf("trust anchor %q stores the out-of-range revocation grace %d s", a.Token, secs)
	}
	return time.Duration(secs) * time.Second, nil
}

// HasRevocationRows is whether the anchor has lists or manual entries. It is the counter column
// maintained by every revocation writer under the anchor lock; a negative value is a defect and
// reads as "has rows" (the fast path that skips the query must never be reached by a bad count).
func (a *DeviceTrustAnchor) HasRevocationRows() bool {
	return a.RevocationRows.Valid && a.RevocationRows.Int64 != 0
}
