// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"net/url"
	"strings"
)

// McpConfiguration configures the MCP server (ADR-047): an OAuth 2.1 Resource
// Server that fronts the per-area GraphQL over the Model Context Protocol.
type McpConfiguration struct {
	// ResourceUrl is this Resource Server's public identifier (RFC 8707 / RFC 9728):
	// the audience OAuth access tokens must be bound to, and the `resource` field of
	// the protected-resource metadata. A token whose `aud` does not include this
	// value is rejected — the anti-confused-deputy binding. Must be an absolute
	// https URL (http tolerated only for a localhost value, for local dev).
	ResourceUrl string

	// IssuerUrl is the OAuth 2.1 Authorization Server that issues tokens for this
	// resource (the DeviceChain user-management AS). Advertised to clients in the
	// protected-resource metadata (`authorization_servers`) so they know where to
	// obtain a token. Must be an absolute https URL (http only for localhost).
	IssuerUrl string
}

// NewMcpConfiguration builds a defaulted configuration.
func NewMcpConfiguration() *McpConfiguration {
	cfg := &McpConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields (ADR-022 decision 1). Both URLs are
// deployment-specific with no universal default, so none is applied here; Validate
// fails the load closed if they are missing.
func (c *McpConfiguration) ApplyDefaults() {}

// Validate fails the load closed on an unusable configuration (ADR-022 decision 1):
// without a resource identifier the RS cannot enforce the audience binding, and
// without an issuer it cannot advertise where to authenticate — either gap would
// make the server insecure or non-functional, so both are required.
func (c *McpConfiguration) Validate() error {
	if err := validateHTTPSUrl("resourceUrl", c.ResourceUrl); err != nil {
		return err
	}
	if err := validateHTTPSUrl("issuerUrl", c.IssuerUrl); err != nil {
		return err
	}
	return nil
}

// validateHTTPSUrl requires an absolute https URL (http only for a localhost host),
// with no query or fragment, mirroring the AS issuer-URL rule so identifiers
// compare byte-for-byte with what clients derive from discovery.
func validateHTTPSUrl(field, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: not a valid URL: %w", field, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("%s: must be an absolute URL with a host (got %q)", field, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.ContainsAny(raw, "?#") {
		return fmt.Errorf("%s: must have no query, fragment, or userinfo (got %q)", field, raw)
	}
	host := u.Hostname()
	isLocalhost := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocalhost) {
		return fmt.Errorf("%s: must use https (http allowed only for localhost; got %q)", field, raw)
	}
	return nil
}
