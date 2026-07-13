// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package functionalarea

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The two core areas are exactly user-management and device-management.
func TestCoreAreas(t *testing.T) {
	assert.Equal(t, []FunctionalArea{DeviceManagement, UserManagement}, CoreAreas())
}

// Every hard dependency and every profile member names a known area, so the
// catalog has no dangling references.
func TestCatalogReferentialIntegrity(t *testing.T) {
	for area, m := range catalog {
		assert.Equal(t, area, m.Area, "manifest key must match its Area")
		for _, dep := range m.HardDeps {
			assert.True(t, Known(dep), "%s hard-dep %s must be known", area, dep)
		}
		for _, dep := range m.SoftDeps {
			assert.True(t, Known(dep), "%s soft-dep %s must be known", area, dep)
		}
	}
	for p, areas := range profiles {
		for _, area := range areas {
			assert.True(t, Known(area), "profile %s member %s must be known", p, area)
		}
	}
}

// Each named profile resolves and validates (profiles are valid by construction).
func TestProfilesAreValid(t *testing.T) {
	for p := range profiles {
		enabled, err := ResolveAndValidate(string(p), nil)
		assert.NoError(t, err, "profile %s must be valid", p)
		assert.NotEmpty(t, enabled)
	}
}

// An empty intent defaults to the full profile.
func TestResolveDefaultsToFull(t *testing.T) {
	enabled, err := ResolveEnabled("", nil)
	assert.NoError(t, err)
	assert.ElementsMatch(t, profiles[ProfileFull], enabled)
}

// Profile and explicit set are mutually exclusive.
func TestResolveRejectsBothProfileAndExplicit(t *testing.T) {
	_, err := ResolveEnabled("full", []FunctionalArea{UserManagement})
	assert.Error(t, err)
}

func TestResolveRejectsUnknownProfile(t *testing.T) {
	_, err := ResolveEnabled("bogus", nil)
	assert.Error(t, err)
}

// telemetry omits command-delivery.
func TestTelemetryProfileOmitsCommandDelivery(t *testing.T) {
	enabled, err := ResolveEnabled("telemetry", nil)
	assert.NoError(t, err)
	assert.NotContains(t, enabled, CommandDelivery)
	assert.Contains(t, enabled, EventManagement)
}

// An explicit set enabling a consumer without its hard-dep resolver is rejected.
func TestValidateRejectsMissingHardDep(t *testing.T) {
	// event-management consumes resolved-events (device-management produces them).
	err := Validate([]FunctionalArea{UserManagement, EventManagement})
	assert.ErrorContains(t, err, string(DeviceManagement))
}

// A set missing a core area is rejected even if hard deps are otherwise satisfied.
func TestValidateRejectsMissingCore(t *testing.T) {
	// device-management present, but user-management (core) absent.
	err := Validate([]FunctionalArea{DeviceManagement, EventManagement})
	assert.ErrorContains(t, err, string(UserManagement))
}

// An unknown area in an explicit set is rejected.
func TestValidateRejectsUnknownArea(t *testing.T) {
	err := Validate([]FunctionalArea{UserManagement, DeviceManagement, "made-up"})
	assert.ErrorContains(t, err, "made-up")
}

// The MCP area is a known, opt-in area (ADR-047): not in any profile, hard-depends
// on device-management, and validates as an explicit set alongside the core areas.
func TestMcpIsOptInArea(t *testing.T) {
	assert.True(t, Known(Mcp))
	for p, areas := range profiles {
		assert.NotContains(t, areas, Mcp, "mcp must not be in profile %s (it requires explicit config)", p)
	}
	m, ok := ManifestFor(Mcp)
	assert.True(t, ok)
	assert.Contains(t, m.HardDeps, DeviceManagement)
	// Explicitly enabling mcp with its hard dep (+ core) is valid.
	assert.NoError(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, Mcp}))
	// Enabling mcp WITHOUT device-management is rejected.
	assert.ErrorContains(t, Validate([]FunctionalArea{UserManagement, Mcp}), string(DeviceManagement))
}

// The outbound-connectors area is a known, opt-in area (ADR-060): not in any profile, hard-depends
// on event-processing (its dispatch producer), and validates only when that chain is enabled too.
func TestOutboundConnectorsIsOptInArea(t *testing.T) {
	assert.True(t, Known(OutboundConn))
	for p, areas := range profiles {
		assert.NotContains(t, areas, OutboundConn, "outbound-connectors must not be in profile %s (it is enabled on demand)", p)
	}
	m, ok := ManifestFor(OutboundConn)
	assert.True(t, ok)
	assert.Contains(t, m.HardDeps, EventProcessing)
	// Enabling it with its hard-dep chain (event-processing → device-management) + core is valid.
	assert.NoError(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, EventProcessing, OutboundConn}))
	// Enabling it WITHOUT event-processing is rejected.
	assert.ErrorContains(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, OutboundConn}), string(EventProcessing))
}

// The minimal valid explicit set is just the two core areas.
func TestValidateAcceptsCoreOnly(t *testing.T) {
	err := Validate([]FunctionalArea{UserManagement, DeviceManagement})
	assert.NoError(t, err)
}

// Soft deps are never enforced: device-management validates without event-sources.
func TestSoftDepsNotEnforced(t *testing.T) {
	err := Validate([]FunctionalArea{UserManagement, DeviceManagement})
	assert.NoError(t, err)
}
