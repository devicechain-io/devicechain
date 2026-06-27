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
	assert.Equal(t, "default", cfg.Auth.BootstrapTenant)
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
			BootstrapTenant:        "default",
			SuperuserEmail:         "superuser@devicechain.local",
			SuperuserPassword:      "devicechain",
		},
	}
	assert.Error(t, cfg.Validate())
}
