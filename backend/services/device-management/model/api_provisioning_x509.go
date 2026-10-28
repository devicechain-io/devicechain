// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This file is the certificate half of device self-registration: a device whose client
// certificate chains to a tenant trust anchor registers itself the first time it connects, under
// an X.509 provisioning profile. The decision is ProvisionDevice's (the same precedence as
// registration by secret); what differs is what authenticates the caller (the chain, verified by
// the connect path) and what is issued (a pinned X509_CERTIFICATE credential, not an access
// token).

// X509Registration is the certificate a registration is made for. The CALLER has verified
// Leaf's chain to Anchor with x509trust.VerifyClientChain: nothing here repeats that, and
// nothing here would catch a caller that skipped it, so the only caller is the auth callout,
// which has just done it on the very connection the device proved possession of the key on.
type X509Registration struct {
	// Leaf is the device's certificate. Its common name is the device's external id.
	Leaf *x509.Certificate
	// NotAfter is the earliest notAfter over the verified chain: the credential's expiry.
	NotAfter time.Time
	// AnchorId and AnchorFingerprint name the trust anchor the chain was verified against. The
	// registration is refused if the profile's anchor is not that row, unchanged and enabled,
	// when it is checked under the lock.
	AnchorId          uint
	AnchorFingerprint string
}

// X509ProfileAnchor is one X.509 provisioning profile with the trust anchor it authenticates by.
type X509ProfileAnchor struct {
	Profile *ProvisioningProfile
	// Anchor is nil when the row the profile names is gone (which the foreign key forbids; a
	// defect, refused like a disabled anchor).
	Anchor *DeviceTrustAnchor
}

// Usable is whether a registration against this profile can proceed now: the profile enabled
// and unexpired, and its anchor present and enabled.
func (p X509ProfileAnchor) Usable(now time.Time) bool {
	return p.Anchor != nil && p.Anchor.Enabled && certificateProfileCanRegister(p.Profile, now)
}

// X509ProfileAnchors lists the tenant's X.509 provisioning profiles with their anchors, oldest
// first. There are at most MaxX509ProfilesPerTenant. It reads the database on every call (no
// cache is in front of it), and it is how the connect path finds which profile a presented
// chain belongs to: a chain verifies to an anchor, and a profile is named by its anchor.
func (api *Api) X509ProfileAnchors(ctx context.Context) ([]X509ProfileAnchor, error) {
	var profiles []ProvisioningProfile
	if err := api.RDB.DB(ctx).Where("trust_anchor_id IS NOT NULL").Order("id ASC").
		Limit(MaxX509ProfilesPerTenant + 1).Find(&profiles).Error; err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, *p.TrustAnchorId)
	}
	var anchors []DeviceTrustAnchor
	if err := api.RDB.DB(ctx).Where("id IN ?", ids).Find(&anchors).Error; err != nil {
		return nil, err
	}
	byId := make(map[uint]*DeviceTrustAnchor, len(anchors))
	for i := range anchors {
		byId[anchors[i].ID] = &anchors[i]
	}
	out := make([]X509ProfileAnchor, 0, len(profiles))
	for i := range profiles {
		out = append(out, X509ProfileAnchor{Profile: &profiles[i], Anchor: byId[*profiles[i].TrustAnchorId]})
	}
	return out, nil
}

// certificateProfileCanRegister is profileCanRegister for an X.509 profile: enabled and not past
// its expiry. It has no secret to check.
func certificateProfileCanRegister(p *ProvisioningProfile, now time.Time) bool {
	if p == nil || p.TrustAnchorId == nil || !p.Enabled {
		return false
	}
	return !p.ExpiresAt.Valid || p.ExpiresAt.Time.After(now)
}

// ValidateCertificateExternalId reports why a certificate's common name cannot be a device's
// external id, or nil: non-blank, no leading or trailing space (the earmark rule trims it, so
// a padded name would derive a token no earmark could carry), at most the external id length,
// no NUL.
func ValidateCertificateExternalId(commonName string) error {
	if strings.TrimSpace(commonName) == "" || strings.TrimSpace(commonName) != commonName ||
		utf8.RuneCountInString(commonName) > maxDeviceExternalIdLen || strings.ContainsRune(commonName, 0) ||
		!utf8.ValidString(commonName) {
		return invalidProvisioning("the certificate's common name is not a valid device external id")
	}
	return nil
}

// prepareCertificateRegistration checks what a certificate registration owes before any
// transaction opens, and fixes the external id to the certificate's common name.
func (api *Api) prepareCertificateRegistration(request *ProvisionRequest) error {
	reg := request.Certificate
	if reg.Leaf == nil || reg.AnchorId == 0 || reg.AnchorFingerprint == "" || reg.NotAfter.IsZero() {
		return errors.New("a certificate registration needs the verified leaf, its anchor and the chain's expiry")
	}
	cn := reg.Leaf.Subject.CommonName
	if err := ValidateCertificateExternalId(cn); err != nil {
		return err
	}
	if request.ExternalId != "" && request.ExternalId != cn {
		return errors.New("a certificate registration's external id is the certificate's common name")
	}
	request.ExternalId = cn
	return nil
}

// lockCertificateProfile is the gate a certificate registration passes under the profile's lock,
// where registration by secret compares the digest: the profile is still an X.509 profile that
// can register, it still names the anchor the chain was verified against, and that anchor is
// still the one that was verified and is still enabled. The anchor is read FOR SHARE and held to
// the commit, so a disable queues behind the registration and then finds (and kicks) the
// credential it wrote, instead of racing it. LOCK ORDER: profile, anchor, device.
func lockCertificateProfile(tx *gorm.DB, profile *ProvisioningProfile, request *ProvisionRequest) error {
	reg := request.Certificate
	if !certificateProfileCanRegister(profile, time.Now()) || profile.TrustAnchorId == nil ||
		*profile.TrustAnchorId != reg.AnchorId {
		return ErrProvisioningFailed
	}
	// A certificate profile that mints something else is the operator's to fix, and says so
	// (callout_x509_total{result="jit_credential_type_unsupported"}), not a failed authentication.
	if CredentialType(profile.CredentialType) != CredentialX509Certificate {
		log.Error().Str("profile", profile.Token).Str("credentialType", profile.CredentialType).
			Msg("A certificate provisioning profile mints a credential type other than X509_CERTIFICATE; registrations against it are refused.")
		return ErrProvisioningCredentialTypeUnsupported
	}
	var anchor DeviceTrustAnchor
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", reg.AnchorId).First(&anchor).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProvisioningFailed
		}
		return err
	}
	if !anchor.Enabled || anchor.FingerprintSha256 != reg.AnchorFingerprint {
		return ErrProvisioningFailed
	}
	return nil
}

// decideKnownDeviceByCertificate is decideKnownDevice's steps 5 to 8 for a certificate. The
// device has passed steps 1 to 4 (disabled, type, earmark, revoked); live holds this profile's
// credentials on it, none disabled or expired, and foreign is whether it holds any other.
//
//  5. it holds this profile's live credential pinning this very certificate -> REPLAYED
//  6. it holds this profile's live credential for another certificate, or any
//     other credential                                                       -> ALREADY_PROVISIONED
//  7. it holds no credential and the profile is at maxDevices               -> LIMIT_EXCEEDED
//  8. otherwise: ADOPT, which registration by certificate does not do yet     -> PROVISIONING_UNAVAILABLE
//
// Step 6 includes a renewal (the same device, a new certificate): refused for now, since
// renewing a credential is not part of this build. Step 8 fails loudly and writes nothing; it
// is not a refusal of the device but of the operation, hence the code a build without the
// feature answers.
func decideKnownDeviceByCertificate(tx *gorm.DB, profile *ProvisioningProfile, device *Device,
	request *ProvisionRequest, live []DeviceCredential, foreign bool) (*provisionDone, error) {
	pin := x509trust.Fingerprint(request.Certificate.Leaf)
	for _, c := range live {
		if CredentialType(c.CredentialType) == CredentialX509Certificate && c.CredentialId == pin {
			return &provisionDone{outcome: ProvisionReplayed, device: device}, nil
		}
	}
	if len(live) > 0 || foreign {
		return nil, ErrProvisioningAlreadyProvisioned
	}
	if err := checkProfileCapacity(tx, profile); err != nil {
		return nil, err
	}
	return nil, ErrProvisioningUnavailable
}

// insertCertificateCredential writes the credential a certificate registration issues: an
// X509_CERTIFICATE pinning the leaf, bound to the profile's anchor, expiring with the chain,
// stamped with the profile that issued it. As for insertProvisionedCredential, the device row
// is already written and the credential is inserted by its id.
func insertCertificateCredential(tx *gorm.DB, profile *ProvisioningProfile, device *Device, request *ProvisionRequest) error {
	reg := request.Certificate
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: reg.Leaf.Raw}))
	anchorId, profileId := reg.AnchorId, profile.ID
	metadata, err := rdb.JSONInputOf("metadata", nil)
	if err != nil {
		return err
	}
	cred := &DeviceCredential{
		MetadataEntity:        rdb.MetadataEntity{Metadata: metadata},
		DeviceId:              device.ID,
		CredentialType:        string(CredentialX509Certificate),
		CredentialId:          x509trust.Fingerprint(reg.Leaf),
		TrustAnchorId:         &anchorId,
		CertificatePem:        sql.NullString{String: leafPEM, Valid: true},
		Enabled:               true,
		ExpiresAt:             sql.NullTime{Time: reg.NotAfter, Valid: true},
		ProvisioningProfileId: &profileId,
	}
	cred.Token = core.DeriveDeviceToken(request.ExternalId, provisionedCredentialTokenPrefix)
	return tx.Omit("Device").Create(cred).Error
}

// ProvisioningCodes is the closed vocabulary of refusal codes, in a fixed order. A caller that
// counts refusals by code builds its series from this, so a code added here is counted without a
// second list to keep in step.
func ProvisioningCodes() []string {
	return []string{
		ProvisioningCodeFailed, ProvisioningCodeInvalid, ProvisioningCodeNotRegistered, ProvisioningCodeNotEarmarked,
		ProvisioningCodeDeviceDisabled, ProvisioningCodeTypeMismatch, ProvisioningCodeTokenCollision,
		ProvisioningCodeAlreadyProvisioned, ProvisioningCodeLimitExceeded, ProvisioningCodeUnavailable,
		ProvisioningCodeMisconfigured, ProvisioningCodeCredentialTypeUnsup,
	}
}
