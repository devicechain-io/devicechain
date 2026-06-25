// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Errors returned by AuthenticateDevice. They are sentinels so callers (e.g. the
// inbound event resolver) can distinguish "nothing was presented" from an
// outright authentication failure without string matching. Every failure mode
// other than ErrCredentialNotPresented means a credential was offered but did
// not pass verification.
var (
	// ErrCredentialNotPresented means no usable credential was supplied. The
	// caller decides whether that is allowed (see DeviceAuthMode).
	ErrCredentialNotPresented = errors.New("no device credential was presented")
	// ErrCredentialTypeInvalid means the presented credential type is not in the
	// known vocabulary (ADR-014).
	ErrCredentialTypeInvalid = errors.New("presented credential type is not recognized")
	// ErrCredentialNotResolved means the presented (type, id) did not match any
	// enabled credential in the tenant. Disabling or deleting a credential is the
	// revocation path, so a revoked credential surfaces here.
	ErrCredentialNotResolved = errors.New("presented credential did not resolve to an enabled device credential")
	// ErrCredentialExpired means the credential resolved but its ExpiresAt has
	// passed.
	ErrCredentialExpired = errors.New("presented credential has expired")
	// ErrCredentialSecretMismatch means the credential type carries a secret and
	// the presented secret was absent or did not match.
	ErrCredentialSecretMismatch = errors.New("presented credential secret did not match")
	// ErrCredentialMisconfigured means the stored credential requires a secret
	// (e.g. MQTT_BASIC) but none was persisted, so it can never authenticate.
	ErrCredentialMisconfigured = errors.New("stored credential is missing required secret material")
)

// PresentedCredential is the authentication material a connecting device offers,
// carried inbound on the event from the transport (ADR-014). CredentialId is the
// public identifier the device presents (access token, X.509 thumbprint, or MQTT
// username); Secret is the accompanying bearer secret when the credential type
// requires one (e.g. an MQTT password) and is nil otherwise.
type PresentedCredential struct {
	CredentialType string
	CredentialId   string
	Secret         *string
}

// credentialRequiresSecret reports whether a credential type carries a secret
// that the device must present and that is verified by comparison. ACCESS_TOKEN
// and X509_CERTIFICATE prove possession out of band (the token id is itself the
// bearer secret; the certificate's private key is proven at the TLS layer), so
// only MQTT_BASIC compares a stored secret.
func credentialRequiresSecret(ctype string) bool {
	return CredentialType(ctype) == CredentialMqttBasic
}

// evaluateCredential verifies a resolved credential against what was presented:
// it is past the enabled/tenant lookup, so it only enforces expiry and, for
// credential types that carry one, the secret. It is pure (no I/O) so the policy
// is unit-testable in isolation. now is supplied by the caller for the same
// reason.
func evaluateCredential(cred *DeviceCredential, presented *PresentedCredential, now time.Time) error {
	// Expiry. Revocation-by-disable is already handled by the enabled-only
	// lookup; expiry is the time-bounded counterpart.
	if cred.ExpiresAt.Valid && !now.Before(cred.ExpiresAt.Time) {
		return ErrCredentialExpired
	}

	// Secret verification for credential types that carry a comparable secret.
	if credentialRequiresSecret(cred.CredentialType) {
		if !cred.CredentialValue.Valid {
			return ErrCredentialMisconfigured
		}
		if presented.Secret == nil {
			return ErrCredentialSecretMismatch
		}
		// Constant-time compare to avoid leaking the secret via timing.
		if subtle.ConstantTimeCompare([]byte(*presented.Secret), []byte(cred.CredentialValue.String)) != 1 {
			return ErrCredentialSecretMismatch
		}
	}
	return nil
}

// AuthenticateDevice resolves a presented credential to its owning device and
// verifies it (ADR-014). It is the server-side authentication primitive every
// transport-security path builds on: the lookup is enabled-only and tenant
// scoped (the global tenant callback constrains it to the context tenant), so a
// credential from another tenant or a disabled/revoked credential never
// authenticates. Expiry and any required secret are checked on top.
//
// It returns the owning Device on success, or one of the ErrCredential* sentinels
// on failure. now is supplied by the caller so expiry is deterministic in tests.
func (api *Api) AuthenticateDevice(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, error) {
	if presented == nil || presented.CredentialId == "" {
		return nil, ErrCredentialNotPresented
	}
	if !CredentialType(presented.CredentialType).Valid() {
		return nil, ErrCredentialTypeInvalid
	}

	cred, err := api.DeviceCredentialByCredentialId(ctx, presented.CredentialType, presented.CredentialId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCredentialNotResolved
		}
		return nil, err
	}

	if err := evaluateCredential(cred, presented, now); err != nil {
		return nil, err
	}

	// The lookup preloads the owning device; reload defensively if absent so a
	// successful authentication always yields a device.
	if cred.Device != nil {
		return cred.Device, nil
	}
	devices, err := api.DevicesById(ctx, []uint{cred.DeviceId})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, ErrCredentialNotResolved
	}
	return devices[0], nil
}
