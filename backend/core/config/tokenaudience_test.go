// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const taOrigin = "https://iot.example.com"

// The exact block the chart derives: areaBaseUrl=<o>/api and mcpResources=[<o>/api/mcp].
// mcp's identifier equals AreaResource(base, "mcp"), so this loads only because AreaAudiences
// leaves mcp out. Negative control for M28.
func TestTokenAudienceChartDerivedBlockLoads(t *testing.T) {
	c := TokenAudienceConfiguration{
		AreaBaseUrl:  taOrigin + "/api",
		McpResources: []string{taOrigin + "/api/mcp"},
	}
	require.NoError(t, c.Validate())
	assert.NotContains(t, AreaAudiences(), "mcp")
	assert.Contains(t, AreaAudiences(), "device-management")
	assert.False(t, IsAreaAudience("mcp"))
	assert.True(t, IsAreaAudience("device-management"))
	assert.False(t, IsAreaAudience("log-level-test"))
}

func TestTokenAudienceFullyPopulatedAndAbsentLoad(t *testing.T) {
	require.NoError(t, TokenAudienceConfiguration{}.Validate(), "an absent block must load")
	require.NoError(t, TokenAudienceConfiguration{
		Enforcement:               "enforce",
		AreaBaseUrl:               taOrigin + "/api",
		McpResources:              []string{taOrigin + "/api/mcp"},
		ExternalResources:         []string{"https://reports.example.org", taOrigin + "/apiary"},
		UnscopedClientCredentials: "observe",
	}.Validate())
	// Loopback http is allowed, as it is for the issuer.
	require.NoError(t, TokenAudienceConfiguration{AreaBaseUrl: "http://localhost:8080/api"}.Validate())
	require.NoError(t, TokenAudienceConfiguration{AreaBaseUrl: "http://127.0.0.1/api"}.Validate())
}

func TestTokenAudienceRefusals(t *testing.T) {
	base := taOrigin + "/api"
	for _, tc := range []struct {
		name string
		c    TokenAudienceConfiguration
		want string
	}{
		{"unknown enforcement", TokenAudienceConfiguration{Enforcement: "warn"}, "enforcement"},
		{"retired-looking enforcement spelling", TokenAudienceConfiguration{Enforcement: "Enforce"}, "enforcement"},
		{"unknown unscoped mode", TokenAudienceConfiguration{UnscopedClientCredentials: "off"}, "unscopedClientCredentials"},
		{"trailing slash", TokenAudienceConfiguration{AreaBaseUrl: base + "/"}, "trailing slash"},
		{"upper-case host", TokenAudienceConfiguration{AreaBaseUrl: "https://IoT.Example.com/api"}, "lowercase"},
		{"upper-case scheme", TokenAudienceConfiguration{AreaBaseUrl: "HTTPS://iot.example.com/api"}, "lowercase"},
		{"plain http on a real host", TokenAudienceConfiguration{AreaBaseUrl: "http://iot.example.com/api"}, "https"},
		{"query", TokenAudienceConfiguration{AreaBaseUrl: base + "?x=1"}, "query"},
		{"fragment", TokenAudienceConfiguration{AreaBaseUrl: base + "#f"}, "query"},
		{"userinfo", TokenAudienceConfiguration{AreaBaseUrl: "https://u:p@iot.example.com/api"}, "userinfo"},
		{"relative", TokenAudienceConfiguration{AreaBaseUrl: "/api"}, "absolute"},
		{"mcp entry malformed", TokenAudienceConfiguration{McpResources: []string{"not a url"}}, "mcpResources[0]"},
		{"empty list entry", TokenAudienceConfiguration{ExternalResources: []string{""}}, "externalResources[0]"},
		{"duplicate within a list", TokenAudienceConfiguration{ExternalResources: []string{"https://a.example.org", "https://a.example.org"}}, "already listed"},
		{"duplicate across the lists", TokenAudienceConfiguration{
			McpResources: []string{"https://a.example.org"}, ExternalResources: []string{"https://a.example.org"}}, "already listed"},
		{"external equals an area resource", TokenAudienceConfiguration{
			AreaBaseUrl: base, ExternalResources: []string{base + "/device-management"}}, "platform area"},
		{"mcp entry equals an area resource", TokenAudienceConfiguration{
			AreaBaseUrl: base, McpResources: []string{base + "/device-management"}}, "platform area"},
		{"external equals the base", TokenAudienceConfiguration{
			AreaBaseUrl: base, ExternalResources: []string{base}}, "re-label"},
		{"external under the base", TokenAudienceConfiguration{
			AreaBaseUrl: base, ExternalResources: []string{base + "/x"}}, "re-label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	nine := make([]string, 9)
	for i := range nine {
		nine[i] = fmt.Sprintf("https://r%d.example.org", i)
	}
	assert.ErrorContains(t, TokenAudienceConfiguration{ExternalResources: nine}.Validate(), "at most 8")
	assert.ErrorContains(t, TokenAudienceConfiguration{McpResources: nine}.Validate(), "at most 8")
	require.NoError(t, TokenAudienceConfiguration{ExternalResources: nine[:8]}.Validate())
}

// The path-segment prefix rule: .../apiary is not under .../api, .../api/x is.
func TestTokenAudienceExternalPrefixIsPerPathSegment(t *testing.T) {
	base := taOrigin + "/api"
	require.NoError(t, TokenAudienceConfiguration{AreaBaseUrl: base, ExternalResources: []string{taOrigin + "/apiary"}}.Validate())
	assert.Error(t, TokenAudienceConfiguration{AreaBaseUrl: base, ExternalResources: []string{taOrigin + "/api/x"}}.Validate())
}

// The same-looking list entry is only an area resource when a base is set: with no base there
// are no area resources, so it loads.
func TestTokenAudienceNoBaseMeansNoAreaResources(t *testing.T) {
	require.NoError(t, TokenAudienceConfiguration{ExternalResources: []string{taOrigin + "/api/device-management"}}.Validate())
	assert.Equal(t, "", AreaResource("", "device-management"))
	assert.Equal(t, "", AreaResource(taOrigin+"/api", ""))
	assert.Equal(t, taOrigin+"/api/device-management", AreaResource(taOrigin+"/api", "device-management"))
}

// M21: an explicit value wins over the release default, in both directions. The default is
// asserted against the constant, so the v1.3 flip changes one line and one expectation.
func TestTokenAudienceEffectiveEnforcement(t *testing.T) {
	assert.Equal(t, TokenAudienceReleaseDefault, TokenAudienceConfiguration{}.EffectiveEnforcement())
	assert.Equal(t, TokenAudienceObserve, TokenAudienceReleaseDefault, "v1.2 ships observe")
	assert.Equal(t, TokenAudienceObserve, TokenAudienceConfiguration{Enforcement: "observe"}.EffectiveEnforcement())
	assert.Equal(t, TokenAudienceEnforce, TokenAudienceConfiguration{Enforcement: "enforce"}.EffectiveEnforcement())
	assert.Equal(t, TokenAudienceObserve, TokenAudienceConfiguration{}.EffectiveUnscopedClientCredentials())
	assert.Equal(t, TokenAudienceObserve, TokenAudienceConfiguration{UnscopedClientCredentials: "observe"}.EffectiveUnscopedClientCredentials())
}

// The shared validator keeps mcp's rule when lowercase is off: an upper-case host that loads
// today still loads, and the new fields refuse it. Everything else is common.
func TestValidateResourceURLLowercaseIsOptIn(t *testing.T) {
	const upper = "https://MCP.Example.com/api/mcp"
	assert.NoError(t, ValidateResourceURL("resourceUrl", upper, false))
	assert.Error(t, ValidateResourceURL("resourceUrl", upper, true))
	// The path is case-sensitive and keeps its case.
	assert.NoError(t, ValidateResourceURL("f", "https://mcp.example.com/API/Mcp", true))
	for _, bad := range []string{"https://:443/api", "https://[]:443/api", "https:///api", "", "https://x.example.com/", "http://x.example.com", "https://x.example.com?q=1"} {
		assert.Error(t, ValidateResourceURL("f", bad, false), bad)
	}
}

// Through the document loader, which strict-decodes: a misspelled key is refused, and the
// block is reached from InstanceConfiguration.Validate (a bad mode stops the load).
func TestTokenAudienceIsValidatedThroughTheInstanceDocument(t *testing.T) {
	cfg := &InstanceConfiguration{}
	cfg.Infrastructure.Nats = NatsConfiguration{Hostname: "n", Port: 4222}
	cfg.Infrastructure.UserManagement = UserManagementConfiguration{Hostname: "u", Port: 8080}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	cfg.Infrastructure.TokenAudience.Enforcement = "nope"
	err := cfg.Validate()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "tokenAudience"), err.Error())
}

// An accepted value that does nothing would let an operator believe the rule is on. Rule 3
// is counted and warned in this release, never refused, so "enforce" is refused at load.
func TestUnscopedClientCredentialsEnforceIsRefusedInThisRelease(t *testing.T) {
	err := TokenAudienceConfiguration{UnscopedClientCredentials: "enforce"}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unscopedClientCredentials")
	assert.Contains(t, err.Error(), "next major release")
	// The counterweights: unset and observe load.
	assert.NoError(t, TokenAudienceConfiguration{}.Validate())
	assert.NoError(t, TokenAudienceConfiguration{UnscopedClientCredentials: "observe"}.Validate())
	// And it is independent of the other mode key.
	assert.NoError(t, TokenAudienceConfiguration{Enforcement: "enforce"}.Validate())
}
