// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// raw builds a decoded field as the GraphQL response would carry it.
func raw(s string) json.RawMessage { return json.RawMessage(s) }

// resolve is a spelling shortcut: most cases assert only the limits.
func resolve(t *testing.T, fields map[string]json.RawMessage, dim Dimension) Limits {
	t.Helper()
	limits, _ := resolveLimits(fields, platformDefault, dim)
	return limits
}

// The query names exactly the dimension's two fields — reading the wrong pair
// would silently govern a different resource.
func TestGovernanceQuery_NamesDimensionFields(t *testing.T) {
	assert.Equal(t, `query { tenantGovernance { ingestMessagesPerSecond ingestBurst } }`, governanceQuery(Ingest))
	assert.Equal(t, `query { tenantGovernance { outboundMessagesPerSecond outboundBurst } }`, governanceQuery(Outbound))
}

// A tenant that declared overrides is metered at them.
func TestResolveLimits_AppliesOverrides(t *testing.T) {
	got := resolve(t, map[string]json.RawMessage{
		"ingestMessagesPerSecond": raw("5"),
		"ingestBurst":             raw("10"),
	}, Ingest)
	assert.Equal(t, Limits{MessagesPerSecond: 5, Burst: 10}, got)
}

// A null override (the common case — the tenant declared none) inherits the
// platform default, which is itself a limit, never unlimited. Inheriting is normal,
// so it is NOT reported as a floored field.
func TestResolveLimits_NullInheritsDefaultSilently(t *testing.T) {
	limits, floored := resolveLimits(map[string]json.RawMessage{
		"ingestMessagesPerSecond": raw("null"),
		"ingestBurst":             raw("null"),
	}, platformDefault, Ingest)
	assert.Equal(t, platformDefault, limits)
	assert.Empty(t, floored, "declaring no override is the normal case, not a fail-safe worth reporting")

	// An absent key (field never returned) and a nil map are the same story.
	assert.Equal(t, platformDefault, resolve(t, map[string]json.RawMessage{}, Ingest))
	assert.Equal(t, platformDefault, resolve(t, nil, Ingest))
}

// Each field falls back independently: overriding the rate alone must not reset
// the burst to zero.
func TestResolveLimits_PartialOverride(t *testing.T) {
	got := resolve(t, map[string]json.RawMessage{
		"ingestMessagesPerSecond": raw("5"),
	}, Ingest)
	assert.Equal(t, Limits{MessagesPerSecond: 5, Burst: platformDefault.Burst}, got)
}

// THE DRIFT THIS CONSOLIDATION FIXES. A non-positive override can only reach the
// column via an out-of-band DB write (the GraphQL write path rejects it), and
// core.TenantRateLimiter documents that a non-positive ceiling admits NOTHING — so
// passing one through would black-hole that tenant. It must floor to the default,
// and say so.
func TestResolveLimits_NonPositiveFloorsToDefault(t *testing.T) {
	for _, tc := range []struct{ name, rate, burst string }{
		{"zero", "0", "0"},
		{"negative", "-1", "-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits, floored := resolveLimits(map[string]json.RawMessage{
				"ingestMessagesPerSecond": raw(tc.rate),
				"ingestBurst":             raw(tc.burst),
			}, platformDefault, Ingest)
			assert.Equal(t, platformDefault, limits, "a non-positive override must inherit the default, never meter at zero")
			assert.ElementsMatch(t, []string{"ingestMessagesPerSecond", "ingestBurst"}, floored,
				"an unusable override must be reported, not silently ignored")
		})
	}
}

// A value that is not a usable number is ignored rather than trusted as a ceiling.
// A JSON string is included deliberately: an earlier decode accepted "5" as 5.
func TestResolveLimits_UnusableValuesFloorToDefault(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"string", `"5"`},
		{"bool", "true"},
		{"object", "{}"},
		{"garbage", "not-json"},
		{"rate_overflow", "1e400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits, floored := resolveLimits(map[string]json.RawMessage{
				"ingestMessagesPerSecond": raw(tc.value),
			}, platformDefault, Ingest)
			assert.Equal(t, platformDefault, limits)
			assert.Equal(t, []string{"ingestMessagesPerSecond"}, floored)
		})
	}
}

// A fractional rate is legal (0.5/s = one call every two seconds); a fractional
// burst is not an integer count and must not silently truncate into a live ceiling.
func TestResolveLimits_FractionalRateLegalBurstNot(t *testing.T) {
	limits, floored := resolveLimits(map[string]json.RawMessage{
		"ingestMessagesPerSecond": raw("0.5"),
		"ingestBurst":             raw("2.5"),
	}, platformDefault, Ingest)
	assert.Equal(t, 0.5, limits.MessagesPerSecond, "a sub-1/s rate is a legal ceiling")
	assert.Equal(t, platformDefault.Burst, limits.Burst, "a non-integer burst must not truncate")
	assert.Equal(t, []string{"ingestBurst"}, floored)
}

// The fetcher reads only its own dimension: an outbound override must not leak
// into an ingest resolver.
func TestResolveLimits_IgnoresOtherDimensions(t *testing.T) {
	got := resolve(t, map[string]json.RawMessage{
		"outboundMessagesPerSecond": raw("5"),
		"outboundBurst":             raw("10"),
	}, Ingest)
	assert.Equal(t, platformDefault, got, "another dimension's override must not govern ingest")
}

// THE WIRE-DECODE PATH, end to end from a real response body — the shape
// svcclient.Query unmarshals into. Decoding each field raw (rather than into a
// numeric map) is what keeps a NON-NUMERIC sibling on the TenantGovernance type
// from failing the whole decode: aiExternalEnabled is a Boolean! on that type, so a
// numeric-map decode would error on every call and silently pin every tenant to the
// platform default forever.
func TestFetchDecode_ToleratesNonNumericSiblings(t *testing.T) {
	body := []byte(`{"tenantGovernance":{
		"ingestMessagesPerSecond": 5,
		"ingestBurst": 10,
		"aiExternalEnabled": true
	}}`)
	var out struct {
		TenantGovernance map[string]json.RawMessage `json:"tenantGovernance"`
	}
	require.NoError(t, json.Unmarshal(body, &out), "a boolean sibling must not fail the decode")

	limits, floored := resolveLimits(out.TenantGovernance, platformDefault, Ingest)
	assert.Equal(t, Limits{MessagesPerSecond: 5, Burst: 10}, limits)
	assert.Empty(t, floored)
}

// The real null-override response decodes and inherits the default.
func TestFetchDecode_NullOverrides(t *testing.T) {
	body := []byte(`{"tenantGovernance":{"ingestMessagesPerSecond":null,"ingestBurst":null}}`)
	var out struct {
		TenantGovernance map[string]json.RawMessage `json:"tenantGovernance"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, platformDefault, resolve(t, out.TenantGovernance, Ingest))
}

// A null tenantGovernance object decodes to a nil map; reading it must not panic.
func TestFetchDecode_NullGovernanceObject(t *testing.T) {
	body := []byte(`{"tenantGovernance":null}`)
	var out struct {
		TenantGovernance map[string]json.RawMessage `json:"tenantGovernance"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Nil(t, out.TenantGovernance)
	assert.Equal(t, platformDefault, resolve(t, out.TenantGovernance, Ingest))
}
