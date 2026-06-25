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

// Build a credential of the given type with an optional stored secret value and
// optional expiry, for exercising evaluateCredential in isolation.
func credential(ctype CredentialType, value *string, expires *time.Time) *DeviceCredential {
	cred := &DeviceCredential{
		CredentialType: string(ctype),
		CredentialId:   "cred-1",
		Enabled:        true,
	}
	if value != nil {
		cred.CredentialValue = sql.NullString{String: *value, Valid: true}
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
	cred := credential(CredentialAccessToken, nil, nil)

	err := evaluateCredential(cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.NoError(t, err)
}

// An X.509 credential may store the (non-secret) PEM in CredentialValue; that
// must not be treated as a secret the device has to present.
func TestEvaluateCredential_X509IgnoresStoredValue(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credential(CredentialX509Certificate, strptr("-----BEGIN CERTIFICATE-----"), nil)

	err := evaluateCredential(cred, &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: "cred-1"}, now)

	assert.NoError(t, err)
}

// MQTT_BASIC carries a secret that must be presented and must match.
func TestEvaluateCredential_BasicSecretMatch(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credential(CredentialMqttBasic, strptr("s3cret"), nil)

	err := evaluateCredential(cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
		Secret:         strptr("s3cret"),
	}, now)

	assert.NoError(t, err)
}

func TestEvaluateCredential_BasicSecretWrong(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credential(CredentialMqttBasic, strptr("s3cret"), nil)

	err := evaluateCredential(cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
		Secret:         strptr("wrong"),
	}, now)

	assert.ErrorIs(t, err, ErrCredentialSecretMismatch)
}

func TestEvaluateCredential_BasicSecretMissing(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credential(CredentialMqttBasic, strptr("s3cret"), nil)

	err := evaluateCredential(cred, &PresentedCredential{
		CredentialType: string(CredentialMqttBasic),
		CredentialId:   "cred-1",
	}, now)

	assert.ErrorIs(t, err, ErrCredentialSecretMismatch)
}

// A basic credential with no stored secret can never authenticate.
func TestEvaluateCredential_BasicMisconfigured(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credential(CredentialMqttBasic, nil, nil)

	err := evaluateCredential(cred, &PresentedCredential{
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
	cred := credential(CredentialAccessToken, nil, &past)

	err := evaluateCredential(cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.ErrorIs(t, err, ErrCredentialExpired)
}

func TestEvaluateCredential_ExpiresAtExactlyNow(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cred := credential(CredentialAccessToken, nil, &now)

	err := evaluateCredential(cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

	assert.ErrorIs(t, err, ErrCredentialExpired)
}

func TestEvaluateCredential_NotYetExpired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	cred := credential(CredentialAccessToken, nil, &future)

	err := evaluateCredential(cred, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "cred-1"}, now)

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
