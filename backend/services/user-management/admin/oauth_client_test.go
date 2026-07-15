// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/bcrypt"
)

// generateClientSecret returns a fresh base64url secret whose bcrypt hash verifies
// against it, the hash is never the cleartext, and successive calls differ.
func TestGenerateClientSecret(t *testing.T) {
	secret, hash, err := generateClientSecret()
	assert.NoError(t, err)
	assert.NotEmpty(t, secret)
	assert.NotEqual(t, secret, hash, "hash must not be the cleartext")

	// The secret is URL/config-safe base64url (no padding) so it drops into a config
	// file without escaping.
	_, derr := base64.RawURLEncoding.DecodeString(secret)
	assert.NoError(t, derr, "secret should be raw-base64url")

	// The stored hash verifies the cleartext (and rejects a wrong one).
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)))
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret+"x")))

	// Fresh entropy + a fresh salt every call.
	secret2, hash2, err := generateClientSecret()
	assert.NoError(t, err)
	assert.NotEqual(t, secret, secret2, "each secret is unique")
	assert.NotEqual(t, hash, hash2, "each hash is unique (salt)")
}

func TestValidateClientId(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"simple ok", "devicechain-mcp", false},
		{"dotted ok", "com.devicechain.mcp_1", false},
		{"empty rejected", "", true},
		{"space rejected", "bad id", true},
		{"slash rejected", "a/b", true},
		{"too long rejected", strings.Repeat("a", 129), true},
		{"max length ok", strings.Repeat("a", 128), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClientId(tc.id)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// At least one redirect URI is required, and each must pass the OAuth 2.1 rules.
func TestValidateRedirectURIs(t *testing.T) {
	assert.Error(t, validateRedirectURIs(nil), "empty list rejected")
	assert.NoError(t, validateRedirectURIs([]string{"https://c.example.com/cb", "http://127.0.0.1:5000/cb"}))
	assert.Error(t, validateRedirectURIs([]string{"https://c.example.com/cb", "http://c.example.com/cb"}),
		"a single non-loopback http URI fails the whole set")
}

// At least one scope is required, and every scope must be one the AS grants.
func TestValidateClientScopes(t *testing.T) {
	assert.Error(t, validateClientScopes(nil), "empty list rejected")
	assert.NoError(t, validateClientScopes([]string{auth.ScopeReadOnly}))
	assert.Error(t, validateClientScopes([]string{auth.ScopeReadOnly, "write"}), "unknown scope rejected")
}
