// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceAllowlistClassifiesByMembership(t *testing.T) {
	a, err := NewResourceAllowlist(testAudCfg("enforce"))
	require.NoError(t, err)
	assert.Equal(t, config.TokenAudienceEnforce, a.Mode())

	for _, c := range []struct {
		resource string
		want     ResourceClass
	}{
		{"", ResNone},
		{audOwn, ResArea},
		{audOth, ResArea},
		{audBase + "/user-management", ResArea},
		{audMCP, ResMCP},
		{audExternal, ResExternal},
		{"https://elsewhere.example.net", ResUnlisted},
		// Byte-exact: a trailing slash, an upper-case host and a sub-path are other spellings.
		{audOwn + "/", ResUnlisted},
		{"https://IOT.example.com/api/device-management", ResUnlisted},
		{audOwn + "/x", ResUnlisted},
		// The base itself is not an area, and mcp is an MCP resource, not an area.
		{audBase, ResUnlisted},
	} {
		assert.Equal(t, c.want, a.Classify(c.resource), "resource %q", c.resource)
	}
}

// Every platform area except mcp is an area audience; the allowlist carries exactly those.
func TestResourceAllowlistAreaSetIsEveryAreaAudience(t *testing.T) {
	a, err := NewResourceAllowlist(testAudCfg(""))
	require.NoError(t, err)
	for _, area := range streams.PlatformAreas {
		got := a.Classify(config.AreaResource(audBase, area))
		if area == "mcp" {
			// mcp's identifier is an MCP resource: <base>/mcp is listed only when the
			// operator (or the chart) lists it, which testAudCfg does with audMCP.
			assert.Equal(t, ResMCP, got, "area %s", area)
			continue
		}
		assert.Equal(t, ResArea, got, "area %s", area)
	}
}

func TestResourceAllowlistWithNoBaseHasNoAreaEntries(t *testing.T) {
	cfg := testAudCfg("enforce")
	cfg.AreaBaseUrl = ""
	a, err := NewResourceAllowlist(cfg)
	require.NoError(t, err)
	assert.Equal(t, ResUnlisted, a.Classify(audOwn))
	// The counterweight: the lists that do not depend on the base still classify.
	assert.Equal(t, ResMCP, a.Classify(audMCP))
	assert.Equal(t, ResExternal, a.Classify(audExternal))
}

func TestResourceAllowlistRefusesAnInvalidBlock(t *testing.T) {
	cfg := testAudCfg("enforce")
	cfg.McpResources = []string{audMCP + "/"}
	_, err := NewResourceAllowlist(cfg)
	require.Error(t, err)
}

func TestResourceClassIssuable(t *testing.T) {
	for _, c := range []ResourceClass{ResNone, ResArea, ResMCP, ResExternal} {
		assert.True(t, c.Issuable(), string(c))
	}
	for _, c := range []ResourceClass{ResUnlisted, ResMalformed, ResMultiple, ResourceClass("")} {
		assert.False(t, c.Issuable(), string(c))
	}
}

func TestWarnLimiterExported(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := NewWarnLimiter(c.now)
	_, ok := l.Allow("k")
	assert.True(t, ok)
	_, ok = l.Allow("k")
	assert.False(t, ok, "the second line inside the interval is held back")
	_, ok = l.Allow("other")
	assert.True(t, ok, "another key is not suppressed")
}
