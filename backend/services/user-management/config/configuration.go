// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

// AuthConfiguration controls JWT issuance and the one-time bootstrap admin.
type AuthConfiguration struct {
	// Token lifetimes in seconds (0 falls back to the auth package defaults).
	AccessTokenTtlSeconds  int
	RefreshTokenTtlSeconds int

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
			AccessTokenTtlSeconds:  900,    // 15 minutes
			RefreshTokenTtlSeconds: 604800, // 7 days
			BootstrapTenant:        "default",
			BootstrapUsername:      "admin",
			BootstrapPassword:      "devicechain",
		},
	}
}
