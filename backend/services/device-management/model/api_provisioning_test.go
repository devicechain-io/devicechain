// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ProvisioningStrategy.Valid accepts the known strategies and rejects others
// (ADR-012).
func TestProvisioningStrategyValid(t *testing.T) {
	for _, valid := range []ProvisioningStrategy{
		ProvisionAllowNew,
		ProvisionCheckPreProvisioned,
	} {
		if !valid.Valid() {
			t.Errorf("known strategy %q rejected", valid)
		}
	}
	for _, invalid := range []ProvisioningStrategy{"", "BOGUS", "allow_new"} {
		if invalid.Valid() {
			t.Errorf("unknown strategy %q accepted", invalid)
		}
	}
}

// provisionableCredentialType allows only ACCESS_TOKEN today; the other
// credential types are not yet mintable by provisioning.
func TestProvisionableCredentialType(t *testing.T) {
	assert.True(t, provisionableCredentialType(string(CredentialAccessToken)))
	assert.False(t, provisionableCredentialType(string(CredentialMqttBasic)))
	assert.False(t, provisionableCredentialType(string(CredentialX509Certificate)))
	assert.False(t, provisionableCredentialType(""))
}

// profile builds a provisioning profile with the given enabled/secret/expiry for
// exercising evaluateProvisioningProfile in isolation.
func profile(enabled bool, secret string, expires *time.Time) *ProvisioningProfile {
	p := &ProvisioningProfile{
		ProvisionKey:    "key-1",
		ProvisionSecret: secret,
		Strategy:        string(ProvisionAllowNew),
		Enabled:         enabled,
	}
	if expires != nil {
		p.ExpiresAt = sql.NullTime{Time: *expires, Valid: true}
	}
	return p
}

// A profile that is enabled, unexpired, and whose secret matches passes.
func TestEvaluateProvisioningProfile_OK(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateProvisioningProfile(profile(true, "s3cret", nil), "s3cret", now)
	assert.NoError(t, err)
}

// A disabled profile is rejected before any secret comparison.
func TestEvaluateProvisioningProfile_Disabled(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateProvisioningProfile(profile(false, "s3cret", nil), "s3cret", now)
	assert.ErrorIs(t, err, ErrProvisioningDisabled)
}

// A wrong secret is rejected.
func TestEvaluateProvisioningProfile_SecretMismatch(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateProvisioningProfile(profile(true, "s3cret", nil), "wrong", now)
	assert.ErrorIs(t, err, ErrProvisioningSecretMismatch)
}

// Expiry is enforced; a profile expiring exactly at now is already expired.
func TestEvaluateProvisioningProfile_Expired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	err := evaluateProvisioningProfile(profile(true, "s3cret", &past), "s3cret", now)
	assert.ErrorIs(t, err, ErrProvisioningExpired)
}

func TestEvaluateProvisioningProfile_ExpiresAtExactlyNow(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateProvisioningProfile(profile(true, "s3cret", &now), "s3cret", now)
	assert.ErrorIs(t, err, ErrProvisioningExpired)
}

func TestEvaluateProvisioningProfile_NotYetExpired(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	err := evaluateProvisioningProfile(profile(true, "s3cret", &future), "s3cret", now)
	assert.NoError(t, err)
}

// Disabled takes precedence over an otherwise-bad secret: the gate order is
// enabled, then expiry, then secret.
func TestEvaluateProvisioningProfile_DisabledBeatsSecret(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	err := evaluateProvisioningProfile(profile(false, "s3cret", nil), "wrong", now)
	assert.ErrorIs(t, err, ErrProvisioningDisabled)
}

// Only CHECK_PRE_PROVISIONED forbids creating an unknown device; ALLOW_NEW
// permits it.
func TestProvisioningRejectsUnknownDevice(t *testing.T) {
	assert.True(t, provisioningRejectsUnknownDevice(ProvisionCheckPreProvisioned))
	assert.False(t, provisioningRejectsUnknownDevice(ProvisionAllowNew))
}

// parseOptionalTime returns the zero invalid value for nil input and a valid
// time for a well-formed RFC3339 string; a malformed string errors.
func TestParseOptionalTime(t *testing.T) {
	zero, err := parseOptionalTime(nil)
	assert.NoError(t, err)
	assert.False(t, zero.Valid)

	ts := "2026-06-25T12:00:00Z"
	parsed, err := parseOptionalTime(&ts)
	assert.NoError(t, err)
	assert.True(t, parsed.Valid)
	assert.Equal(t, time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC), parsed.Time.UTC())

	bad := "not-a-time"
	_, err = parseOptionalTime(&bad)
	assert.Error(t, err)
}
