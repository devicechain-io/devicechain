// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ErrCredentialRevoked means the certificate pin names a credential that exists and has been
// disabled. It is its own answer, apart from ErrCredentialNotResolved (no such pin), because the
// two mean different things to an operator and a revocation must be countable as one.
var ErrCredentialRevoked = errors.New("presented certificate belongs to a disabled device credential")

// X509Connect is what ResolveX509Connect found for a certificate pin: the device the credential
// belongs to and the trust anchor its chain must reach. Nothing in it is authenticated yet: the
// caller still owes the chain verification against Anchor, and the checks on Device.
type X509Connect struct {
	// Device carries only its id, tenant, token and when it was disabled, and
	// ConnectCredentialExpiresAt set to the credential's expiry.
	Device *Device
	// Anchor is the credential's trust anchor, or nil when the row it names is gone. A nil or
	// disabled anchor must refuse the connect.
	Anchor *DeviceTrustAnchor
}

// x509ConnectColumns is everything an X.509 connect reads from the credential row: an
// allowlist, as for the other connect lookups, so a column added later is not read until it is
// added here. Table-qualified because the device join shares column names with it.
var x509ConnectColumns = []string{
	"device_credentials.id",
	"device_credentials.tenant_id",
	"device_credentials.device_id",
	"device_credentials.credential_type",
	"device_credentials.credential_id",
	"device_credentials.trust_anchor_id",
	"device_credentials.enabled",
	"device_credentials.expires_at",
}

// ResolveX509Connect finds the X509_CERTIFICATE credential that pins fingerprint (the hex
// SHA-256 of the presented leaf's DER) in the context's tenant, with its device and its trust
// anchor: the auth callout's one call into this path for a certificate connect.
//
// 🔴 IT IS A METHOD OF ITS OWN SO THAT NO CACHE CAN SIT IN FRONT OF IT, for the reason
// AuthenticateDeviceConnect is: a connect that succeeds is granted a session the broker
// honours for hours, so one answered from a copy a revocation had not reached would outlive the
// revocation. CachedApi must never override this; every call reads the database, which is what
// makes a disable refuse the next connect on every replica at once. The same holds for the
// anchor, which is read here and not through any cache.
//
// 🔴 THE PIN IS A PUBLIC VALUE. Finding a credential by it authenticates nobody: the caller
// must verify the presented chain to Anchor, and the device proves possession of the
// certificate's key in the TLS handshake. That is why this is not reachable through
// AuthenticateDevice, whose type gate (PresentedById) refuses this type.
//
// Errors, in the order they are decided:
//   - ErrCredentialNotResolved: no live credential pins it (none, deleted, or the device is);
//   - ErrCredentialRevoked: the credential is disabled;
//   - ErrCredentialExpired: its expiry (the chain's earliest notAfter) has passed;
//   - ErrCredentialMisconfigured: a certificate credential with no anchor (stored before
//     anchors existed), which can never authenticate.
func (api *Api) ResolveX509Connect(ctx context.Context, fingerprint string, now time.Time) (*X509Connect, error) {
	if fingerprint == "" {
		return nil, ErrCredentialNotPresented
	}
	device := api.RDB.Database.Session(&gorm.Session{NewDB: true}).Select(connectDeviceFields)
	found, result := api.credentialsWithDevice(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("device_credentials.credential_type = ? AND device_credentials.credential_id = ?",
			string(CredentialX509Certificate), fingerprint)
	}, x509ConnectColumns, device)
	markMetadataUnread(found)
	cred, err := oneLiveCredential(result, found, string(CredentialX509Certificate), fingerprint)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCredentialNotResolved
		}
		return nil, err
	}
	// Belt over the callback's predicate and credentialsWithDevice's own filter: a pin resolves in
	// the calling tenant only.
	if tenant, terr := ctxTenant(ctx); terr != nil || cred.TenantId != tenant {
		return nil, ErrCredentialNotResolved
	}
	if !cred.Enabled {
		return nil, ErrCredentialRevoked
	}
	if err := checkExpiry(cred, now); err != nil {
		return nil, err
	}
	if cred.TrustAnchorId == nil {
		return nil, ErrCredentialMisconfigured
	}
	dev, err := credentialDevice(cred)
	if err != nil {
		return nil, err
	}
	out := &X509Connect{Device: withConnectExpiry(dev, cred)}
	var anchor DeviceTrustAnchor
	switch err := api.RDB.DB(ctx).Where("id = ?", *cred.TrustAnchorId).Take(&anchor).Error; {
	case err == nil:
		out.Anchor = &anchor
	case errors.Is(err, gorm.ErrRecordNotFound):
		// The anchor is gone: left nil, which the caller refuses like a disabled one.
	default:
		return nil, err
	}
	return out, nil
}
