// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/x509"
	"database/sql"
	"errors"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// DeviceTrustAnchor is a CA certificate a tenant trusts to vouch for its devices' client
// certificates. It is PUBLIC material and stored as plain text: envelope encryption would
// protect nothing.
//
// A credential of type X509_CERTIFICATE names the anchor its leaf must chain to, and is checked
// against that anchor at EVERY connect, so disabling the anchor withdraws the CA at once and
// disabling the credential withdraws one device. A root or an intermediate may be an anchor; an
// anchor at the narrowest intermediate is how a tenant trusts one sub-CA of a manufacturer's PKI.
//
// 🔴 THE ANCHOR IS THE IDENTITY BOUNDARY. Name constraints on it or on any CA below it are
// refused rather than honoured (see x509trust.VerifyClientChain).
type DeviceTrustAnchor struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	// CertificatePem is the canonical PEM of the one CA certificate.
	CertificatePem string `gorm:"not null;type:text"`
	// FingerprintSha256 is the hex SHA-256 of the certificate's DER, unique per tenant.
	FingerprintSha256 string `gorm:"not null;size:64"`
	Subject           string `gorm:"not null;size:1024"`
	Issuer            string `gorm:"not null;size:1024"`
	NotBefore         time.Time
	NotAfter          time.Time
	// Enabled is whether chains to this anchor authenticate. Turning it off ends the live
	// sessions of every device whose credential relies on it.
	Enabled bool `gorm:"not null"`

	// RevocationMode is the anchor's revocation mode (x509trust.Mode): NULL reads as ENTRIES, and
	// a stored value outside the vocabulary is refused, never defaulted (RevocationModeOf).
	RevocationMode sql.NullString `gorm:"size:32"`
	// RevocationStaleGrace is, in seconds, how long past nextUpdate a list still counts as
	// current under REQUIRE_CURRENT. NULL reads as DefaultRevocationStaleGrace.
	RevocationStaleGrace sql.NullInt64
	// RevocationRows counts the anchor's lists plus its manual entries, maintained by every
	// revocation writer under the anchor's row lock. NULL reads as 0, and an ENTRIES anchor with
	// none runs no revocation query on a connect.
	RevocationRows sql.NullInt64
}

// DefaultOrder implements rdb.Sortable: newest first, token as the unique tiebreak.
func (DeviceTrustAnchor) DefaultOrder() string {
	return "device_trust_anchors.created_at DESC, device_trust_anchors.token ASC"
}

// Certificate parses the stored PEM. A row that does not hold exactly one certificate is a
// defect in the stored data, and the error says so rather than yielding a nil anchor.
func (a *DeviceTrustAnchor) Certificate() (*x509.Certificate, error) {
	_, certs, err := x509trust.ParseCertificates(a.CertificatePem)
	if err != nil {
		return nil, err
	}
	if len(certs) != 1 {
		return nil, errors.New("a stored trust anchor does not hold exactly one certificate")
	}
	return certs[0], nil
}

// Data required to create a trust anchor.
type DeviceTrustAnchorCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	// CertificatePem is exactly one CA certificate: a CA with keyCertSign, unexpired, with an
	// allowed key and no name constraints. A private key anywhere in the text is refused.
	CertificatePem string
	// Enabled defaults to true when omitted.
	Enabled  *bool
	Metadata *string
}

// A PARTIAL update to a trust anchor: omit a field to leave the stored value alone. The
// certificate is immutable (a different CA is a different anchor), and there is no Token (the
// anchor is identified by the mutation's `token` argument).
type DeviceTrustAnchorUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// Enabled is a NOT NULL column; a null is refused. Setting it false ends the live sessions
	// of the devices that rely on the anchor.
	Enabled  dcgraphql.OptionalBool
	Metadata dcgraphql.OptionalString
	// RevocationMode is ENTRIES or REQUIRE_CURRENT. A null is refused (ENTRIES is a value, not
	// an absence). Switching to REQUIRE_CURRENT is refused while the anchor holds no current list
	// or a registered certificate's issuer has none.
	RevocationMode OptionalRevocationMode
	// RevocationStaleGrace is a Go duration (whole seconds, 0s to 168h). A null clears it, so the
	// instance's default applies.
	RevocationStaleGrace dcgraphql.OptionalString
}

// Search criteria for trust anchors.
type DeviceTrustAnchorSearchCriteria struct {
	rdb.Pagination
	Enabled *bool
}

// Results for trust anchor search.
type DeviceTrustAnchorSearchResults struct {
	Results    []DeviceTrustAnchor
	Pagination rdb.SearchResultsPagination
}
