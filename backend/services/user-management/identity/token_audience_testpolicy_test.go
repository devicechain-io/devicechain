// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

// harnessMcpResources are the MCP resources the shared OAuth harnesses ask tokens for. From
// v1.3 the release default enforces the resource allowlist, so a Manager built without the
// instance's token-audience block (defaultResourcePolicy: enforce over an EMPTY allowlist)
// refuses every one of them as invalid_target. The harnesses are about other properties
// (client_credentials, refresh, consent), so they register these resources the way the chart
// registers mcp's, and keep running under the production default rather than an opt-out.
var harnessMcpResources = []string{
	"https://mcp.example.com",
	"https://mcp.example.invalid",
	"http://127.0.0.1:8080/mcp",
}

// harnessTokenAudience is the block the harnesses install: the release default (enforcement
// unset) with harnessMcpResources registered.
func harnessTokenAudience() config.TokenAudienceConfiguration {
	return config.TokenAudienceConfiguration{McpResources: harnessMcpResources}
}

// harnessResourcePolicy is the authorization server's resource rule over harnessTokenAudience,
// for a test that drives ValidateAuthorizeRequest without a Manager.
var harnessResourcePolicy = func() *resourcePolicy {
	p, err := newResourcePolicy(harnessTokenAudience(), nil)
	if err != nil {
		panic(err)
	}
	return p
}()

// installHarnessTokenAudience registers harnessMcpResources on m under the release default.
func installHarnessTokenAudience(t *testing.T, m *Manager) {
	t.Helper()
	require.NoError(t, m.SetTokenAudience(harnessTokenAudience(), nil))
	require.True(t, m.resourcePolicy().enforcing(), "the harness runs the release default, enforce")
}
