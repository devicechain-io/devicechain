// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document defaults the superuser (ADR-033) so the documented
// first login works (ADR-022 decision 1 defaulting via core.LoadConfiguration).
// This is platform-breaking if it regresses.
func TestLoadDefaultsSuperuser(t *testing.T) {
	cfg := &UserManagementConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Equal(t, "superuser@devicechain.local", cfg.Auth.SuperuserEmail)
	assert.Equal(t, "devicechain", cfg.Auth.SuperuserPassword)
	assert.Equal(t, 900, cfg.Auth.AccessTokenTtlSeconds)
	assert.Equal(t, 604800, cfg.Auth.RefreshTokenTtlSeconds)
	assert.Equal(t, 8, cfg.Auth.SigningKeyRetentionDays)
	assert.Equal(t, 0, cfg.Auth.SigningKeyMaxAgeDays)
	assert.NoError(t, cfg.Validate())
}

// The constructor and the load path share one source of defaults.
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	assert.Equal(t, "superuser@devicechain.local", cfg.Auth.SuperuserEmail)
	assert.NoError(t, cfg.Validate())
}

// A non-positive refresh-token TTL fails validation closed so a key-value store
// is never created with a zero/negative TTL.
func TestValidateRejectsNonPositiveRefreshTtl(t *testing.T) {
	cfg := &UserManagementConfiguration{
		Auth: AuthConfiguration{
			AccessTokenTtlSeconds:  900,
			RefreshTokenTtlSeconds: -1,
			SuperuserEmail:         "superuser@devicechain.local",
			SuperuserPassword:      "devicechain",
		},
	}
	assert.Error(t, cfg.Validate())
}

// OAuth is off by default (no issuer configured) — the AS surface stays
// fail-closed until an operator sets an issuer URL (ADR-047).
func TestOAuthDisabledByDefault(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	assert.False(t, cfg.OAuthEnabled())
	assert.NoError(t, cfg.Validate())
}

// A configured issuer URL turns OAuth on and passes validation.
func TestOAuthEnabledWithIssuer(t *testing.T) {
	cfg := NewUserManagementConfiguration()
	cfg.Auth.IssuerUrl = "https://devicechain.example.com/user-management"
	assert.True(t, cfg.OAuthEnabled())
	assert.NoError(t, cfg.Validate())
}

// The issuer URL must be a well-formed absolute https origin (http tolerated only
// for localhost), with no query/fragment/trailing slash, so it compares
// byte-for-byte with what clients derive from RFC 8414 discovery.
func TestValidateIssuerUrl(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"https ok", "https://devicechain.example.com", true},
		{"https with path ok", "https://devicechain.example.com/user-management", true},
		{"http localhost ok", "http://localhost:8080", true},
		{"http 127.0.0.1 ok", "http://127.0.0.1:8080", true},
		{"http non-localhost rejected", "http://devicechain.example.com", false},
		{"trailing slash rejected", "https://devicechain.example.com/", false},
		{"query rejected", "https://devicechain.example.com?x=1", false},
		{"fragment rejected", "https://devicechain.example.com#f", false},
		{"bare question mark rejected", "https://devicechain.example.com?", false},
		{"bare hash rejected", "https://devicechain.example.com#", false},
		{"userinfo rejected", "https://user:pass@devicechain.example.com", false},
		{"uppercase scheme rejected", "HTTPS://devicechain.example.com", false},
		{"uppercase host rejected", "https://Devicechain.Example.COM", false},
		{"relative rejected", "/user-management", false},
		{"no host rejected", "https://", false},
		{"garbage rejected", "://nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewUserManagementConfiguration()
			cfg.Auth.IssuerUrl = tc.url
			err := cfg.Validate()
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// A seedClients document decodes (camelCase JSON → struct fields) and validates —
// the injection point dcctl uses to provision a confidential Grafana client.
func TestSeedClientsDecodeAndValidate(t *testing.T) {
	doc := `{
	  "auth": {
	    "seedClients": [
	      {
	        "clientId": "grafana",
	        "redirectUris": ["https://dc.example.com/grafana/login/generic_oauth"],
	        "scopes": ["read-only"],
	        "secretHash": "$2a$10$abcdefghijklmnopqrstuv"
	      }
	    ]
	  }
	}`
	cfg := &UserManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(doc), cfg))
	assert.NoError(t, cfg.Validate())

	if assert.Len(t, cfg.Auth.SeedClients, 1) {
		sc := cfg.Auth.SeedClients[0]
		assert.Equal(t, "grafana", sc.ClientId)
		assert.Equal(t, []string{"https://dc.example.com/grafana/login/generic_oauth"}, sc.RedirectURIs)
		assert.Equal(t, []string{"read-only"}, sc.Scopes)
		assert.NotEmpty(t, sc.SecretHash)
	}
}

// Each seed-client entry is validated fail-closed at startup, mirroring the admin
// API's rules, so a malformed provisioned client cannot boot the service.
func TestValidateSeedClients(t *testing.T) {
	base := func(sc SeedOAuthClientConfig) *UserManagementConfiguration {
		cfg := NewUserManagementConfiguration()
		cfg.Auth.SeedClients = []SeedOAuthClientConfig{sc}
		return cfg
	}
	ok := SeedOAuthClientConfig{
		ClientId: "grafana", RedirectURIs: []string{"https://dc.example.com/grafana/login/generic_oauth"},
		Scopes: []string{"read-only"}, SecretHash: "$2a$10$hash",
	}
	assert.NoError(t, base(ok).Validate(), "a well-formed confidential seed client is valid")

	pub := ok
	pub.SecretHash = ""
	assert.NoError(t, base(pub).Validate(), "an empty secret hash (public client) is allowed")

	bad := map[string]SeedOAuthClientConfig{
		"empty clientId":       {ClientId: "", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes},
		"bad clientId charset": {ClientId: "bad id", RedirectURIs: ok.RedirectURIs, Scopes: ok.Scopes},
		"no redirect":          {ClientId: "grafana", RedirectURIs: nil, Scopes: ok.Scopes},
		"http non-loopback":    {ClientId: "grafana", RedirectURIs: []string{"http://dc.example.com/cb"}, Scopes: ok.Scopes},
		"no scope":             {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: nil},
		"unknown scope":        {ClientId: "grafana", RedirectURIs: ok.RedirectURIs, Scopes: []string{"write"}},
	}
	for name, sc := range bad {
		t.Run(name, func(t *testing.T) { assert.Error(t, base(sc).Validate()) })
	}

	// Duplicate clientIds across entries are rejected.
	dup := NewUserManagementConfiguration()
	dup.Auth.SeedClients = []SeedOAuthClientConfig{ok, ok}
	assert.Error(t, dup.Validate(), "duplicate clientId rejected")
}
