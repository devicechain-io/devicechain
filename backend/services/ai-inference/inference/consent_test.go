// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFactsQuerySelectsTheContractFields is the ai-inference half of a CROSS-SERVICE
// wire contract; user-management pins the other half
// (graphql.TestTenantGovernanceExposesTheTierToken).
//
// Both facts are read from ONE tenantGovernance query by field name. If either name
// drifts from what user-management serves, the JSON simply decodes to the zero value —
// and both zero values are indistinguishable from a legitimate answer: false reads as
// "this tenant never consented", "" reads as "unknown tier, empty menu". Either way the
// NL door reports unavailable for every tenant, fail-closed and silent, which is the
// worst kind of correct-looking failure. Nothing else in the build would notice.
func TestFactsQuerySelectsTheContractFields(t *testing.T) {
	assert.Contains(t, tenantFactsQuery, "tenantGovernance", "the facts come from user-management's tenantGovernance query")
	assert.Contains(t, tenantFactsQuery, "aiExternalEnabled", "ADR-056 §6 consent, by the name user-management serves")
	assert.Contains(t, tenantFactsQuery, "tierToken", "ADR-065 tier, by the name user-management serves")
}

// TestFactsDecodesTheWireShape decodes an ACTUAL user-management response body rather
// than asserting against a hand-built Go map. That distinction has bitten this platform
// before: a struct-tag typo or a nesting mistake is invisible to a test that never
// serializes, and the failure it produces (a zero value) looks exactly like a valid
// fail-closed answer.
func TestFactsDecodesTheWireShape(t *testing.T) {
	// The struct under test is the one production decodes into, not a copy of it.
	var out tenantFactsResponse
	body := []byte(`{"tenantGovernance":{"aiExternalEnabled":true,"tierToken":"gold"}}`)
	require.NoError(t, json.Unmarshal(body, &out))

	assert.True(t, out.TenantGovernance.AiExternalEnabled, "consent must survive the round trip")
	assert.Equal(t, "gold", out.TenantGovernance.TierToken, "the tier must survive the round trip")
}

// TestDeniedFactsReaderFailsClosed. When service-to-service auth is unconfigured neither
// consent nor the tier can be established, so every tenant resolution must be refused —
// never served with a zero-value tier, which would look like "unknown tier" rather than
// "we could not ask".
func TestDeniedFactsReaderFailsClosed(t *testing.T) {
	reader := NewDeniedTenantFactsReader("service auth not configured")
	_, err := reader.Facts(context.Background(), "acme")
	require.Error(t, err, "an unconfigurable facts read must be an error, not an empty answer")
	assert.True(t, strings.Contains(err.Error(), "service auth not configured"),
		"the reason must reach the operator's logs")
}
