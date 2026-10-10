// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// strptr is a small helper for building optional presented secrets.
func strptr(s string) *string { return &s }

// Build a credential of the given type with an optional stored secret (stored as its
// digest under testSecretKey) and
// optional expiry, for exercising evaluateCredential in isolation.
func credentialRow(ctype CredentialType, value *string, expires *time.Time) *DeviceCredential {
	cred := &DeviceCredential{
		CredentialType: string(ctype),
		CredentialId:   "cred-1",
		Enabled:        true,
	}
	if value != nil && *value == "" {
		// An empty stored value is a defect no write path produces; stored as is.
		cred.SecretDigest = sql.NullString{String: "", Valid: true}
	} else if value != nil {
		cred.SecretDigest = testDigest(*value)
	}
	if expires != nil {
		cred.ExpiresAt = sql.NullTime{Time: *expires, Valid: true}
	}
	return cred
}

// A credential type that carries no comparable secret (ACCESS_TOKEN) passes on
// possession alone, even when no secret is presented.
func TestEvaluateCredential_NoSecretType(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialAccessToken, nil, nil)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.NoError(t, err)
}

// A stored X.509 row can never authenticate: nothing verifies a certificate, so the
// type is refused even when the row is present and enabled.
func TestEvaluateCredential_X509IsRefused(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialX509Certificate, strptr("-----BEGIN CERTIFICATE-----"), nil)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: "cred-1"}, now)

	assert.ErrorIs(t, err, ErrCredentialTypeInvalid)
}

// MQTT_BASIC carries a secret that must be presented and must match.
func TestEvaluateCredential_BasicSecretMatch(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialMqttBasic, strptr("s3cret"), nil)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
		Secret:         strptr("s3cret"),
	}, now)

	assert.NoError(t, err)
}

func TestEvaluateCredential_BasicSecretWrong(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialMqttBasic, strptr("s3cret"), nil)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
		Secret:         strptr("wrong"),
	}, now)

	assert.ErrorIs(t, err, ErrCredentialSecretMismatch)
}

// The compare is over digests, so every near miss must still be refused: a secret of
// the same length, one extending the stored secret, a prefix of it, and an empty one.
func TestEvaluateCredential_BasicSecretNearMisses(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialMqttBasic, strptr("s3cret"), nil)

	for _, presented := range []string{"s3creT", "s3cret-extra", "s3cre", ""} {
		err := evaluateCredential(testSecretKey, cred, &PresentedCredential{
			CredentialType: string(CredentialMqttBasic),
			CredentialId:   "cred-1",
			Secret:         strptr(presented),
		}, now)
		assert.ErrorIs(t, err, ErrCredentialSecretMismatch, "presented %q", presented)
	}
}

func TestEvaluateCredential_BasicSecretMissing(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialMqttBasic, strptr("s3cret"), nil)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
	}, now)

	assert.ErrorIs(t, err, ErrCredentialSecretMismatch)
}

// A basic credential with no stored secret can never authenticate.
func TestEvaluateCredential_BasicMisconfigured(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialMqttBasic, nil, nil)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
		Secret:         strptr("anything"),
	}, now)

	assert.ErrorIs(t, err, ErrCredentialMisconfigured)
}

// Expiry is enforced; a credential expiring exactly at now is already expired.
func TestEvaluateCredential_Expired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	cred := credentialRow(CredentialAccessToken, nil, &past)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.ErrorIs(t, err, ErrCredentialExpired)
}

func TestEvaluateCredential_ExpiresAtExactlyNow(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credentialRow(CredentialAccessToken, nil, &now)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.ErrorIs(t, err, ErrCredentialExpired)
}

func TestEvaluateCredential_NotYetExpired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	cred := credentialRow(CredentialAccessToken, nil, &future)

	err := evaluateCredential(testSecretKey, cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.NoError(t, err)
}

// AuthenticateDevice rejects empty/absent input before any datastore access, so
// these guards are exercisable without an RDB.
func TestAuthenticateDevice_NotPresented(t *testing.T) {
	api := &Api{}
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	_, err := api.AuthenticateDevice(context.Background(), nil, now)
	assert.ErrorIs(t, err, ErrCredentialNotPresented)

	_, err = api.AuthenticateDevice(context.Background(), &PresentedCredential{
		CredentialType: string(CredentialAccessToken),
		CredentialId:   "",
	}, now)
	assert.ErrorIs(t, err, ErrCredentialNotPresented)
}

func TestAuthenticateDevice_InvalidType(t *testing.T) {
	api := &Api{}
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	_, err := api.AuthenticateDevice(context.Background(), &PresentedCredential{
		CredentialType: "NOPE",
		CredentialId:   "cred-1",
	}, now)

	assert.ErrorIs(t, err, ErrCredentialTypeInvalid)
}
