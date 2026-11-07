// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-mcp/arena"
	"github.com/devicechain-io/dc-mcp/server"
	coreconfig "github.com/devicechain-io/dc-microservice/config"
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

	// MaxBatchEntries is the most messages one JSON-RPC batch POST may carry; a larger
	// batch is refused whole with a JSON-RPC error. Protocol versions before 2025-06-18
	// allow batches, and without a cap one 1 MiB body can carry ~15,000 tool calls.
	// Defaults to 16.
	MaxBatchEntries int

	// CallsPerSecond and CallsBurst are the per-caller (token subject) rate limit, counted
	// in JSON-RPC messages: a batch of N is charged N. Past it a request is refused with
	// 429 and a Retry-After. Defaults 20 per second, burst 40. CallsBurst must be at least
	// MaxBatchEntries.
	CallsPerSecond float64
	CallsBurst     int

	// ExperimentalArena configures the experimental Proving Ground tools. Absent, or with
	// enabled false, none of them is served. It may change or be removed in any release.
	ExperimentalArena *ExperimentalArena
}

// ExperimentalArena is the switch and the inputs of the Proving Ground, an experimental
// graded-test environment built from recordings (experimental_arena_* tools).
type ExperimentalArena struct {
	// Enabled turns the tools on. It is false unless set.
	Enabled bool
	// ScenarioDir holds the *.scenario.json files and the recordings they name. Required
	// when enabled; the service refuses to start on a directory it cannot load.
	ScenarioDir string
	// Policy is the tenant's graduated-autonomy policy the scorecard reports against.
	Policy arena.Policy
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
func (c *McpConfiguration) ApplyDefaults() {
	d := server.DefaultLimits()
	if c.MaxBatchEntries == 0 {
		c.MaxBatchEntries = d.MaxBatchEntries
	}
	if c.CallsPerSecond == 0 {
		c.CallsPerSecond = d.CallsPerSecond
	}
	if c.CallsBurst == 0 {
		c.CallsBurst = d.CallsBurst
	}
}

// Limits is the configured request bounds, in the form the server takes.
func (c *McpConfiguration) Limits() server.Limits {
	return server.Limits{MaxBatchEntries: c.MaxBatchEntries, CallsPerSecond: c.CallsPerSecond, CallsBurst: c.CallsBurst}
}

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
	if a := c.ExperimentalArena; a != nil && a.Enabled {
		if a.ScenarioDir == "" {
			return fmt.Errorf("experimentalArena.scenarioDir: required when the arena is enabled")
		}
		for i, r := range a.Policy.Rules {
			if r.Suite == "" || len(r.Commands) == 0 || r.MinScore < 0 || r.MinScore > 1 || r.MaxUnsafe < 0 {
				return fmt.Errorf("experimentalArena.policy.rules[%d]: needs a suite, at least one command, minScore in [0, 1] and a non-negative maxUnsafe", i)
			}
		}
	}
	if c.MaxBatchEntries < 1 {
		return fmt.Errorf("maxBatchEntries: must be at least 1 (got %d)", c.MaxBatchEntries)
	}
	if c.CallsPerSecond <= 0 {
		return fmt.Errorf("callsPerSecond: must be positive (got %v)", c.CallsPerSecond)
	}
	if c.CallsBurst < c.MaxBatchEntries {
		return fmt.Errorf("callsBurst: must be at least maxBatchEntries (%d) or a full batch could never be admitted (got %d)",
			c.MaxBatchEntries, c.CallsBurst)
	}
	return nil
}

// validateHTTPSUrl requires an absolute https URL (http only for a loopback host), with no
// query, fragment, userinfo or trailing slash. It is the shared rule in core/config, which the
// instance's tokenAudience block and the authorization server use too, so the identifier mcp
// is configured with and the one it must be registered under are judged by one implementation.
// The lowercase scheme/host rule is NOT applied: it is for the new tokenAudience fields only,
// and adding it here would refuse, in a minor release, a resourceUrl that starts today.
func validateHTTPSUrl(field, raw string) error {
	return coreconfig.ValidateResourceURL(field, raw, false)
}

// ArenaOptions builds the experimental server options from the configuration: the zero
// Options (nothing experimental served) unless the arena is enabled. A scenario directory
// that cannot be loaded, or a policy naming a suite or command the scenarios do not have,
// is an error the service refuses to start on.
func (c *McpConfiguration) ArenaOptions() (server.Options, error) {
	a := c.ExperimentalArena
	if a == nil || !a.Enabled {
		return server.Options{}, nil
	}
	cat, err := arena.LoadCatalog(a.ScenarioDir)
	if err != nil {
		return server.Options{}, fmt.Errorf("experimentalArena: %w", err)
	}
	for i, r := range a.Policy.Rules {
		if len(cat.Suite(r.Suite)) == 0 {
			return server.Options{}, fmt.Errorf("experimentalArena.policy.rules[%d]: no scenario belongs to suite %q", i, r.Suite)
		}
	}
	return server.Options{Arena: arena.NewRegistry(cat), ArenaPolicy: a.Policy}, nil
}
