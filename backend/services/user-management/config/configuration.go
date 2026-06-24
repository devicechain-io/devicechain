// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

// AuthConfiguration controls JWT issuance, signing-key rotation, and the
// one-time bootstrap admin.
type AuthConfiguration struct {
	// Token lifetimes in seconds (0 falls back to the auth package defaults).
	AccessTokenTtlSeconds  int
	RefreshTokenTtlSeconds int

	// SigningKeyMaxAgeDays triggers an age-based signing-key rotation at startup
	// when the active key is older than this. 0 disables age-based rotation
	// (rotation can still be invoked explicitly). SigningKeyRetentionDays is how
	// long a rotated-out key is kept (its public half stays in the JWKS) so
	// tokens it signed keep verifying; it must exceed the refresh-token lifetime.
	SigningKeyMaxAgeDays    int
	SigningKeyRetentionDays int

	// Bootstrap admin seeded on first startup when no users exist. The default
	// password MUST be changed after first login; startup logs a warning.
	BootstrapTenant   string
	BootstrapUsername string
	BootstrapPassword string
}

type UserManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
	Auth             AuthConfiguration
}

// Creates the default user management configuration
func NewUserManagementConfiguration() *UserManagementConfiguration {
	return &UserManagementConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
		Auth: AuthConfiguration{
			AccessTokenTtlSeconds:   900,    // 15 minutes
			RefreshTokenTtlSeconds:  604800, // 7 days
			SigningKeyMaxAgeDays:    0,      // age-based rotation off by default
			SigningKeyRetentionDays: 8,      // > refresh-token lifetime (7 days)
			BootstrapTenant:         "default",
			BootstrapUsername:       "admin",
			BootstrapPassword:       "devicechain",
		},
	}
}
