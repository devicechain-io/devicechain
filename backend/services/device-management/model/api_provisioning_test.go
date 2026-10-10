// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
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
