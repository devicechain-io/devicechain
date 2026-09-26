// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
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

// IsCredentialRefusal reports whether err is the DEVICE's wrong answer — nothing presented,
// an unknown type, an unknown or revoked credential, an expired one, or a wrong secret —
// as opposed to a failure on the platform's side. A refusal is routine traffic (a
// misconfigured or stale device retries it forever) and callers log it quietly; anything
// else is worth an operator's attention.
//
// 🔴 ErrCredentialMisconfigured IS DELIBERATELY NOT A REFUSAL. It is a defect in STORED
// data — a credential that requires a secret and has none, so it can never authenticate —
// and no device can fix it by answering differently. It must be seen by an operator, not
// filed with the wrong-password noise. This is the ONE place that classification is made;
// a caller that needs it asks here rather than listing the sentinels again.
func IsCredentialRefusal(err error) bool {
	return errors.Is(err, ErrCredentialNotPresented) ||
		errors.Is(err, ErrCredentialTypeInvalid) ||
		errors.Is(err, ErrCredentialNotResolved) ||
		errors.Is(err, ErrCredentialExpired) ||
		errors.Is(err, ErrCredentialSecretMismatch)
}

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
	if err := checkExpiry(cred, now); err != nil {
		return err
	}

	// Secret verification for credential types that carry a comparable secret.
	if credentialRequiresSecret(cred.CredentialType) {
		stored, err := storedSecret(cred)
		if err != nil {
			return err
		}
		if presented.Secret == nil {
			return ErrCredentialSecretMismatch
		}
		// Compare fixed-width SHA-256 digests in constant time, so the check leaks
		// neither the stored secret's content nor its LENGTH: ConstantTimeCompare
		// returns at once when its inputs differ in length. The digests are never
		// stored: they exist only for this length-safe compare.
		//
		// This compare serves the per-event path only (AuthenticateDevice, from the
		// event resolver), whose verdict never reaches the sender. The MQTT auth
		// callout, which DOES answer, does not come here: it resolves the credential
		// with ResolveDeviceCredential and compares through credential.Checker, under
		// a per-credential backoff.
		got := sha256.Sum256([]byte(*presented.Secret))
		want := sha256.Sum256([]byte(stored))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			return ErrCredentialSecretMismatch
		}
	}
	return nil
}

// checkExpiry is the time-bounded half of evaluateCredential: revocation by disable is
// the enabled-only lookup's job.
func checkExpiry(cred *DeviceCredential, now time.Time) error {
	if cred.ExpiresAt.Valid && !now.Before(cred.ExpiresAt.Time) {
		return ErrCredentialExpired
	}
	return nil
}

// storedSecret is the secret a secret-carrying credential stores, or
// ErrCredentialMisconfigured when it stores none. An EMPTY stored secret is
// misconfigured too, not a secret that an empty presented one could match: the
// create path stores NULL for a blank value, so an empty string there is a defect in
// the stored data, and it must reach an operator rather than authenticate anyone.
func storedSecret(cred *DeviceCredential) (string, error) {
	if !cred.CredentialValue.Valid || cred.CredentialValue.String == "" {
		return "", ErrCredentialMisconfigured
	}
	return cred.CredentialValue.String, nil
}

// AuthenticateDevice resolves a presented credential to its owning device and
// verifies it (ADR-014). It is the authentication primitive of the PER-EVENT path —
// the event resolver re-authenticates every event whose body carries a credential —
// where the verdict never reaches the sender: the lookup is enabled-only and tenant
// scoped (the global tenant callback constrains it to the context tenant), so a
// credential from another tenant or a disabled/revoked credential never
// authenticates. Expiry and any required secret are checked on top.
//
// 🔴 THE MQTT AUTH CALLOUT DOES NOT USE IT FOR A PASSWORD, and must not: the callout
// answers the device, so its password compare has to sit behind a per-credential
// backoff. For an MQTT_BASIC connect it calls ResolveDeviceCredential and compares
// through credential.Checker instead; it comes here only for an access-token connect,
// which compares no secret (the token IS the credential id). This path stays
// unthrottled on purpose — a throttle here would cost a KV round trip on every such
// event, and would let a device's own event stream push its connects into backoff.
//
// It returns the owning Device on success, or one of the ErrCredential* sentinels
// on failure. now is supplied by the caller so expiry is deterministic in tests.
func (api *Api) AuthenticateDevice(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, error) {
	cred, err := api.lookupPresentedCredential(ctx, presented)
	if err != nil {
		return nil, err
	}
	if err := evaluateCredential(cred, presented, now); err != nil {
		return nil, err
	}
	return credentialDevice(cred)
}

// ResolveDeviceCredential is AuthenticateDevice WITHOUT the secret compare, for a
// caller that compares through credential.Checker: the MQTT auth callout. It resolves
// a presented MQTT_BASIC credential exactly as AuthenticateDevice does (enabled-only,
// tenant-scoped, expiry) and returns its owning device together with the STORED
// secret, for the Checker to compare against what was presented.
//
// 🔴 A DEVICE RETURNED FROM HERE IS NOT AUTHENTICATED. Nothing has compared the
// presented secret; the caller must not use the device until its Checker has. That is
// why it refuses a credential type that carries no secret (ErrCredentialTypeInvalid):
// for an access token or a certificate there is nothing left for a Checker to compare,
// so a device returned for one would be a grant with no check at all.
//
// It returns ErrCredentialMisconfigured for a stored credential with no secret, and
// the other ErrCredential* sentinels as AuthenticateDevice does, except
// ErrCredentialSecretMismatch, which only a compare can produce.
func (api *Api) ResolveDeviceCredential(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, string, error) {
	if presented != nil && presented.CredentialId != "" && CredentialType(presented.CredentialType).Valid() &&
		!credentialRequiresSecret(presented.CredentialType) {
		return nil, "", fmt.Errorf("%w: %s carries no secret to compare", ErrCredentialTypeInvalid, presented.CredentialType)
	}
	cred, err := api.lookupPresentedCredential(ctx, presented)
	if err != nil {
		return nil, "", err
	}
	if err := checkExpiry(cred, now); err != nil {
		return nil, "", err
	}
	stored, err := storedSecret(cred)
	if err != nil {
		return nil, "", err
	}
	device, err := credentialDevice(cred)
	if err != nil {
		return nil, "", err
	}
	return device, stored, nil
}

// lookupPresentedCredential validates what was presented and finds the enabled
// credential it names.
func (api *Api) lookupPresentedCredential(ctx context.Context, presented *PresentedCredential) (*DeviceCredential, error) {
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
	return cred, nil
}

// credentialDevice is the device a resolved credential belongs to, from the lookup's
// JOIN. The joined row is accepted only if it is the credential's own device (by id) in
// the credential's own tenant.
//
// 🔴 THE TENANT CHECK IS REQUIRED, NOT DEFENSIVE. The join carries the soft-delete
// predicate but NOT the tenant predicate — the scope callback qualifies only the
// statement's own table — so a device_id that points across tenants (corrupt data: a
// normal create resolves the device inside the tenant) would otherwise authenticate as
// the other tenant's device. The tenant-scoped preload this replaced refused it; this
// refuses it the same way. The tenant check also refuses a soft-deleted or missing device
// if the scan ever allocates an empty Device instead of leaving it nil (gorm leaves it nil
// when the joined columns are NULL, but that depends on how each column scans, and a
// future Device field with a serializer could change it): an empty Device's TenantId is "".
//
// The id check is belt-and-braces, and no test reaches it: the join's ON clause already
// pins devices.id to the credential's device_id, and the only other way to get a
// mismatched id — an empty Device — is refused by the tenant check first. It stays because
// it states what the joined row must be at the one place that trusts it.
func credentialDevice(cred *DeviceCredential) (*Device, error) {
	d := cred.Device
	if d == nil || d.ID != cred.DeviceId || d.TenantId != cred.TenantId {
		return nil, ErrCredentialNotResolved
	}
	return d, nil
}
