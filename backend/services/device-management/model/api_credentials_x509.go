// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/pem"
	"time"

	"github.com/devicechain-io/dc-device-management/model/identity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

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
		return nil, identity.InvalidAnchor("credentialId is computed from the certificate and must be omitted for %s", CredentialX509Certificate)
	}
	if request.CredentialValue != nil && *request.CredentialValue != "" {
		return nil, identity.InvalidAnchor("credentialValue does not apply to %s", CredentialX509Certificate)
	}
	if request.ExpiresAt != nil {
		return nil, identity.InvalidAnchor("expiresAt is the certificate chain's and cannot be set for %s", CredentialX509Certificate)
	}
	anchorToken := identity.TrimmedToken(request.TrustAnchorToken)
	if anchorToken == "" {
		return nil, identity.InvalidAnchor("trustAnchorToken is required for %s", CredentialX509Certificate)
	}
	if request.CertificatePem == nil || *request.CertificatePem == "" {
		return nil, identity.InvalidAnchor("certificatePem is required for %s", CredentialX509Certificate)
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	_, presented, err := x509trust.ParseCertificates(*request.CertificatePem)
	if err != nil {
		return nil, identity.InvalidAnchor("certificatePem: %v", err)
	}
	if len(presented) == 0 || len(presented) > x509trust.MaxPresentedCerts {
		return nil, identity.InvalidAnchor("certificatePem must hold the device certificate and at most %d intermediates",
			x509trust.MaxPresentedCerts-1)
	}

	anchor, err := api.trustAnchorByToken(ctx, anchorToken)
	if err != nil {
		return nil, err
	}
	if err := identity.RequireUsableAnchor(anchor); err != nil {
		return nil, err
	}
	anchorCert, err := anchor.Certificate()
	if err != nil {
		return nil, err
	}
	verified, err := x509trust.VerifyClientChain(anchorCert, presented, time.Now())
	if err != nil {
		return nil, identity.InvalidAnchor("the certificate does not chain to trust anchor %q: %s", anchorToken, x509trust.ReasonOf(err))
	}

	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: verified.Leaf.Raw}))
	created := &DeviceCredential{
		DeviceCredential: identity.DeviceCredential{
			TokenReference: rdb.TokenReference{Token: request.Token},
			MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
			DeviceId:       device.ID,
			CredentialType: string(CredentialX509Certificate),
			CredentialId:   verified.Fingerprint,
			TrustAnchorId:  &anchor.ID,
			CertificatePem: sql.NullString{String: leafPEM, Valid: true},
			Enabled:        request.Enabled,
			ExpiresAt:      sql.NullTime{Time: verified.NotAfter, Valid: true},
		},
		Device: device,
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// The anchor is held FOR SHARE from here to the commit and re-checked: it was read, and
		// the chain verified, before this transaction, and a disable or a delete since then must
		// not be missed. LOCK ORDER: anchor, then device (a move in UpdateDeviceCredential takes
		// the device alone).
		locked, err := lockTrustAnchorShareByToken(tx, anchorToken)
		if err != nil {
			return err
		}
		if locked.ID != anchor.ID || locked.FingerprintSha256 != anchor.FingerprintSha256 {
			return identity.InvalidAnchor("trust anchor %q changed while the credential was being created; try again", anchorToken)
		}
		if err := identity.RequireUsableAnchor(locked); err != nil {
			return err
		}
		// The device is locked so the count below is the count the insert lands on.
		if err := lockDeviceRow(tx, device.ID); err != nil {
			return err
		}
		n, err := countX509Credentials(tx, device.ID)
		if err != nil {
			return err
		}
		if n >= MaxX509CredentialsPerDevice {
			return identity.InvalidAnchor("device %q already holds %d certificate credentials, the most it may hold; remove one first",
				device.Token, n)
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		// The path it was created through, for a later revocation to find its sessions by. A
		// hint, in the credential's own transaction so a credential never exists without it.
		return writeChainLinks(tx, created.ID, anchor.ID, verified.Chain)
	})
	if err != nil {
		return nil, identity.ReferenceToNotFound(err, anchorToken)
	}
	return created, nil
}
