// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

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

	// Superuser seeded on first startup when no identity exists (ADR-033): a global
	// email identity holding the `superuser` system role (authority `*`). The
	// default password MUST be changed after first login; startup logs a warning.
	// dcctl bootstrap supplies a generated password.
	SuperuserEmail    string
	SuperuserPassword string

	// BootstrapTenant is the scaffold tenant seeded alongside the superuser so the
	// tenant console is immediately usable (the superuser gets a membership in it).
	// Removed once the admin console can create the first tenant (ADR-033 phase 4,
	// tenant-less bootstrap).
	BootstrapTenant string
}

type UserManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
	Auth             AuthConfiguration
}

// Creates the default user management configuration
func NewUserManagementConfiguration() *UserManagementConfiguration {
	cfg := &UserManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with their defaults so configuration loaded
// from a document that omits them is still well-formed (ADR-022 decision 1). It
// runs on both the constructor and the load path so there is one source of
// defaults. SigningKeyMaxAgeDays is intentionally left at 0 (age-based rotation
// off); SqlDebug is intentionally left at its zero value (SQL query logging off).
func (c *UserManagementConfiguration) ApplyDefaults() {
	if c.Auth.AccessTokenTtlSeconds == 0 {
		c.Auth.AccessTokenTtlSeconds = 900 // 15 minutes
	}
	if c.Auth.RefreshTokenTtlSeconds == 0 {
		c.Auth.RefreshTokenTtlSeconds = 604800 // 7 days
	}
	if c.Auth.SigningKeyRetentionDays == 0 {
		c.Auth.SigningKeyRetentionDays = 8 // > refresh-token lifetime (7 days)
	}
	if c.Auth.SuperuserEmail == "" {
		c.Auth.SuperuserEmail = "superuser@devicechain.local"
	}
	if c.Auth.SuperuserPassword == "" {
		c.Auth.SuperuserPassword = "devicechain"
	}
	if c.Auth.BootstrapTenant == "" {
		c.Auth.BootstrapTenant = "default"
	}
}

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1). It is
// defense in depth: the bootstrap admin must be fully specified, and token TTLs
// must be positive so a key-value store is never created with a zero TTL.
func (c *UserManagementConfiguration) Validate() error {
	if c.Auth.SuperuserEmail == "" {
		return fmt.Errorf("auth.superuserEmail must not be empty")
	}
	if c.Auth.SuperuserPassword == "" {
		return fmt.Errorf("auth.superuserPassword must not be empty")
	}
	if c.Auth.BootstrapTenant == "" {
		return fmt.Errorf("auth.bootstrapTenant must not be empty")
	}
	if c.Auth.AccessTokenTtlSeconds <= 0 {
		return fmt.Errorf("auth.accessTokenTtlSeconds must be positive (got %d)", c.Auth.AccessTokenTtlSeconds)
	}
	if c.Auth.RefreshTokenTtlSeconds <= 0 {
		return fmt.Errorf("auth.refreshTokenTtlSeconds must be positive (got %d)", c.Auth.RefreshTokenTtlSeconds)
	}
	return nil
}
