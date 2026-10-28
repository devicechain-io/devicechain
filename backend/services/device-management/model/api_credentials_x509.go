// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// refuseX509Elsewhere is the answer of every path that would build a credential from a request
// without resolving a trust anchor (a device replacement, an import row): a certificate
// credential's chain is checked at create, against the anchor, and only CreateDeviceCredential
// does that.
func refuseX509Elsewhere() error {
	return invalidAnchor("%s credentials are created with createDeviceCredential, which checks the "+
		"certificate's chain against a trust anchor; add it to the device afterwards", CredentialX509Certificate)
}

// refuseX509Fields refuses the certificate-only fields of a create for any other type, so a
// certificate sent with an ACCESS_TOKEN is an error and not a silently ignored value.
func refuseX509Fields(request *DeviceCredentialCreateRequest) error {
	if trimmedToken(request.TrustAnchorToken) != "" || (request.CertificatePem != nil && *request.CertificatePem != "") {
		return invalidAnchor("certificatePem and trustAnchorToken apply only to %s credentials", CredentialX509Certificate)
	}
	return nil
}

// createX509Credential creates an X509_CERTIFICATE credential on an already resolved device.
//
// Everything about the credential is derived from the certificate, so none of it is a caller's
// to get wrong or to forge:
//   - the id is the hex SHA-256 of the leaf DER (a caller-supplied id is refused);
//   - the expiry is the earliest notAfter over the verified chain (a caller-supplied one is
//     refused), which the connect path also clamps the session to;
//   - the chain is verified NOW against the named anchor, which must be enabled, so a stored
//     row that could never authenticate does not exist.
//
// The text holds the device's certificate first, then any intermediates that link it to the
// anchor. Only the leaf is stored (the device presents its intermediates at connect).
func (api *Api) createX509Credential(ctx context.Context, device *Device, request *DeviceCredentialCreateRequest) (*DeviceCredential, error) {
	if request.CredentialId != "" {
		return nil, invalidAnchor("credentialId is computed from the certificate and must be omitted for %s", CredentialX509Certificate)
	}
	if request.CredentialValue != nil && *request.CredentialValue != "" {
		return nil, invalidAnchor("credentialValue does not apply to %s", CredentialX509Certificate)
	}
	if request.ExpiresAt != nil {
		return nil, invalidAnchor("expiresAt is the certificate chain's and cannot be set for %s", CredentialX509Certificate)
	}
	anchorToken := trimmedToken(request.TrustAnchorToken)
	if anchorToken == "" {
		return nil, invalidAnchor("trustAnchorToken is required for %s", CredentialX509Certificate)
	}
	if request.CertificatePem == nil || *request.CertificatePem == "" {
		return nil, invalidAnchor("certificatePem is required for %s", CredentialX509Certificate)
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	_, presented, err := x509trust.ParseCertificates(*request.CertificatePem)
	if err != nil {
		return nil, invalidAnchor("certificatePem: %v", err)
	}
	if len(presented) == 0 || len(presented) > x509trust.MaxPresentedCerts {
		return nil, invalidAnchor("certificatePem must hold the device certificate and at most %d intermediates",
			x509trust.MaxPresentedCerts-1)
	}

	anchor, err := api.trustAnchorByToken(ctx, anchorToken)
	if err != nil {
		return nil, err
	}
	if err := requireUsableAnchor(anchor); err != nil {
		return nil, err
	}
	anchorCert, err := anchor.Certificate()
	if err != nil {
		return nil, err
	}
	verified, err := x509trust.VerifyClientChain(anchorCert, presented, time.Now())
	if err != nil {
		return nil, invalidAnchor("the certificate does not chain to trust anchor %q: %s", anchorToken, x509trust.ReasonOf(err))
	}

	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: verified.Leaf.Raw}))
	created := &DeviceCredential{
		TokenReference: rdb.TokenReference{Token: request.Token},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		DeviceId:       device.ID,
		Device:         device,
		CredentialType: string(CredentialX509Certificate),
		CredentialId:   verified.Fingerprint,
		TrustAnchorId:  &anchor.ID,
		CertificatePem: sql.NullString{String: leafPEM, Valid: true},
		Enabled:        request.Enabled,
		ExpiresAt:      sql.NullTime{Time: verified.NotAfter, Valid: true},
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&DeviceCredential{}).
			Where("device_id = ? AND credential_type = ?", device.ID, string(CredentialX509Certificate)).
			Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxX509CredentialsPerDevice {
			return invalidAnchor("device %q already holds %d certificate credentials, the most it may hold; remove one first",
				device.Token, n)
		}
		return tx.Create(created).Error
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// X509CertificateInfo describes the pinned leaf of a certificate credential for display. Every
// field is read back from the stored certificate; none is trusted for authentication.
type X509CertificateInfo struct {
	FingerprintSha256 string
	SubjectCn         string
	SerialHex         string
	NotAfter          time.Time
	TrustAnchorId     uint
}

// X509Info parses the stored leaf of a certificate credential. It is nil for any other type, and
// for a row whose certificate is absent or unreadable (a stored defect is shown as no detail,
// never as a made-up one).
func (c *DeviceCredential) X509Info() *X509CertificateInfo {
	if CredentialType(c.CredentialType) != CredentialX509Certificate || !c.CertificatePem.Valid {
		return nil
	}
	block, _ := pem.Decode([]byte(c.CertificatePem.String))
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	info := &X509CertificateInfo{
		FingerprintSha256: x509trust.Fingerprint(cert),
		SubjectCn:         cert.Subject.CommonName,
		SerialHex:         fmt.Sprintf("%x", cert.SerialNumber),
		NotAfter:          cert.NotAfter.UTC(),
	}
	if c.TrustAnchorId != nil {
		info.TrustAnchorId = *c.TrustAnchorId
	}
	return info
}
