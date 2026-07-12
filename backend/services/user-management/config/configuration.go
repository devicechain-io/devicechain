// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-microservice/config"
)

// AuthConfiguration controls JWT issuance, signing-key rotation, and the
// one-time bootstrap admin.
type AuthConfiguration struct {
	// IssuerUrl is the external https origin of this Authorization Server (ADR-047).
	// When set it becomes the "iss" claim of *every* minted token (a decisive
	// platform-wide cutover from the legacy internal identifier — the validator
	// selects keys by "kid", never pins "iss", so cross-service verification is
	// unaffected) AND turns on the OAuth 2.1 AS surface: the token's issuer must
	// equal the base URL where the RFC 8414 metadata is served. Empty (the default)
	// keeps the legacy derived issuer and leaves the entire OAuth surface OFF,
	// fail-closed — mirroring how an empty service-auth secret disables service-token
	// minting. Must be an absolute http/https URL with no query or fragment (http is
	// tolerated only for a localhost issuer, for local development).
	IssuerUrl string

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
	if c.Auth.AccessTokenTtlSeconds <= 0 {
		return fmt.Errorf("auth.accessTokenTtlSeconds must be positive (got %d)", c.Auth.AccessTokenTtlSeconds)
	}
	if c.Auth.RefreshTokenTtlSeconds <= 0 {
		return fmt.Errorf("auth.refreshTokenTtlSeconds must be positive (got %d)", c.Auth.RefreshTokenTtlSeconds)
	}
	if c.Auth.IssuerUrl != "" {
		if err := validateIssuerUrl(c.Auth.IssuerUrl); err != nil {
			return fmt.Errorf("auth.issuerUrl: %w", err)
		}
	}
	return nil
}

// OAuthEnabled reports whether the OAuth 2.1 Authorization-Server surface is
// turned on — it is exactly when an issuer URL is configured (fail-closed: no
// issuer, no OAuth). The metadata/authorize/token handlers register only when
// this is true, and the same URL is stamped as every token's "iss".
func (c *UserManagementConfiguration) OAuthEnabled() bool {
	return c.Auth.IssuerUrl != ""
}

// validateIssuerUrl enforces the RFC 8414 issuer-identifier shape: an absolute
// URL, https (http tolerated only for a localhost issuer during local dev), with
// no query or fragment. A trailing slash is rejected so the stored issuer and the
// "iss" claim compare byte-for-byte against what clients derive from discovery.
func validateIssuerUrl(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("must be an absolute URL with a host (got %q)", raw)
	}
	// The raw string is what gets stamped as "iss" and concatenated into endpoint
	// URLs, so reject anything the parsed-field checks below would miss on a
	// technicality: a bare "?"/"#" (url.Parse records these as ForceQuery / empty
	// fragment, slipping past the RawQuery/Fragment checks) and any query/fragment
	// delimiter at all. An issuer with either can never compare byte-for-byte
	// against what a client derives from discovery.
	if u.ForceQuery || strings.ContainsAny(raw, "?#") {
		return fmt.Errorf("must have no query or fragment (got %q)", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("must have no query or fragment (got %q)", raw)
	}
	if u.User != nil {
		return fmt.Errorf("must not contain userinfo/credentials (got %q)", raw)
	}
	if strings.HasSuffix(u.Path, "/") {
		return fmt.Errorf("must not end with a trailing slash (got %q)", raw)
	}
	// The scheme and host must already be lowercase in the RAW string (it is what
	// is stored/emitted as "iss"), so an uppercase "HTTPS://Host" fails a
	// normalizing client's issuer match. url.Parse lowercases u.Scheme (so check the
	// raw prefix) but preserves u.Host's case.
	if i := strings.Index(raw, "://"); i >= 0 {
		if s := raw[:i]; s != strings.ToLower(s) {
			return fmt.Errorf("scheme must be lowercase (got %q)", raw)
		}
	}
	if host := u.Host; host != strings.ToLower(host) {
		return fmt.Errorf("host must be lowercase (got %q)", raw)
	}
	host := u.Hostname()
	isLocalhost := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocalhost) {
		return fmt.Errorf("must use https (http allowed only for a localhost issuer; got scheme %q host %q)", u.Scheme, host)
	}
	return nil
}
