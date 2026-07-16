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

// An empty intent resolves to the DEFAULT profile — the standard system. Declaring no
// intent must never deploy the areas that reach outside the instance (AI inference,
// outbound connectors, MCP); that takes an explicit "full". This is also the chart's
// empty-selection behaviour, and the two must not diverge: the chart renders what is
// deployed, this package is what the operator resolves.
func TestResolveDefaultsToDefaultProfile(t *testing.T) {
	enabled, err := ResolveEnabled("", nil)
	assert.NoError(t, err)
	assert.ElementsMatch(t, profiles[ProfileDefault], enabled)
	assert.NotContains(t, enabled, AiInference, "an unstated intent must not deploy AI inference")
	assert.NotContains(t, enabled, OutboundConn, "an unstated intent must not deploy outbound connectors")
	assert.NotContains(t, enabled, Mcp, "an unstated intent must not deploy MCP")
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

// The MCP area is a known, opt-in area (ADR-047): held back from the standard
// profiles, present in ProfileFull, hard-depends on device-management, and validates
// as an explicit set alongside the core areas.
func TestMcpIsOptInArea(t *testing.T) {
	assert.True(t, Known(Mcp))
	assert.Contains(t, profiles[ProfileFull], Mcp, "full ships everything, including mcp")
	for p, areas := range profiles {
		if p == ProfileFull {
			continue
		}
		assert.NotContains(t, areas, Mcp, "mcp must not be in profile %s (it is enabled deliberately)", p)
	}
	m, ok := ManifestFor(Mcp)
	assert.True(t, ok)
	assert.Contains(t, m.HardDeps, DeviceManagement)
	// Explicitly enabling mcp with its hard dep (+ core) is valid.
	assert.NoError(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, Mcp}))
	// Enabling mcp WITHOUT device-management is rejected.
	assert.ErrorContains(t, Validate([]FunctionalArea{UserManagement, Mcp}), string(DeviceManagement))
}

// The outbound-connectors area is a known, opt-in area (ADR-060): held back from the
// standard profiles, present in ProfileFull, hard-depends on event-processing (its
// dispatch producer), and validates only when that chain is enabled too.
func TestOutboundConnectorsIsOptInArea(t *testing.T) {
	assert.True(t, Known(OutboundConn))
	assert.Contains(t, profiles[ProfileFull], OutboundConn, "full ships everything, including outbound-connectors")
	for p, areas := range profiles {
		if p == ProfileFull {
			continue
		}
		assert.NotContains(t, areas, OutboundConn, "outbound-connectors must not be in profile %s (it is enabled deliberately)", p)
	}
	m, ok := ManifestFor(OutboundConn)
	assert.True(t, ok)
	assert.Contains(t, m.HardDeps, EventProcessing)
	// Enabling it with its hard-dep chain (event-processing → device-management) + core is valid.
	assert.NoError(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, EventProcessing, OutboundConn}))
	// Enabling it WITHOUT event-processing is rejected.
	assert.ErrorContains(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, OutboundConn}), string(EventProcessing))
}

// The ai-inference area is a known, opt-in area (ADR-056): held back from the standard
// profiles, present in ProfileFull, and — unlike a stream consumer — it has NO hard
// deps, since event-processing calls IT, so it can be enabled with just the core areas.
// It only soft-deps user-management (the consent flag read degrades fail-closed).
func TestAiInferenceIsOptInArea(t *testing.T) {
	assert.True(t, Known(AiInference))
	assert.Contains(t, profiles[ProfileFull], AiInference, "full ships everything, including ai-inference")
	for p, areas := range profiles {
		if p == ProfileFull {
			continue
		}
		assert.NotContains(t, areas, AiInference, "ai-inference must not be in profile %s (it is enabled deliberately)", p)
	}
	m, ok := ManifestFor(AiInference)
	assert.True(t, ok)
	assert.Empty(t, m.HardDeps)
	assert.Contains(t, m.SoftDeps, UserManagement)
	// Enabling it alongside the core areas is valid (no hard dep to satisfy).
	assert.NoError(t, Validate([]FunctionalArea{UserManagement, DeviceManagement, AiInference}))
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

// ProfileFull means EVERYTHING this build ships. This is the guard against the drift
// that made "full" a misnomer: three areas (mcp, outbound-connectors, ai-inference)
// were added to the catalog over time and each was quietly left out of every profile,
// so the profile named "full" silently came to mean "most of it" — and the areas
// behind two GA marquee features became unreachable from the installer, which only
// ever passes a profile. A new area now fails this test until it is placed
// deliberately: in full, or excluded with a stated reason.
func TestProfileFullShipsEveryKnownArea(t *testing.T) {
	for a := range catalog {
		assert.Contains(t, profiles[ProfileFull], a,
			"area %q is missing from the full profile — full must ship everything", a)
	}
	assert.Len(t, profiles[ProfileFull], len(catalog), "full must contain every known area exactly once")
	assert.NoError(t, Validate(profiles[ProfileFull]), "the full profile must itself be a valid selection")
}

// ProfileDefault is the standard system and must stay a valid, self-consistent
// selection — and a strict subset of full.
func TestProfileDefaultIsTheStandardSystem(t *testing.T) {
	assert.NoError(t, Validate(profiles[ProfileDefault]))
	for _, a := range profiles[ProfileDefault] {
		assert.Contains(t, profiles[ProfileFull], a, "default must be a subset of full")
	}
	assert.Less(t, len(profiles[ProfileDefault]), len(profiles[ProfileFull]),
		"default holds back the areas that reach outside the instance")
}
