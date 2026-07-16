// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// num builds a decoded json.Number field as the GraphQL response would carry it.
func num(s string) *json.Number {
	n := json.Number(s)
	return &n
}

// The query names exactly the dimension's two fields — reading the wrong pair
// would silently govern a different resource.
func TestGovernanceQuery_NamesDimensionFields(t *testing.T) {
	assert.Equal(t, `query { tenantGovernance { ingestMessagesPerSecond ingestBurst } }`, governanceQuery(Ingest))
	assert.Equal(t, `query { tenantGovernance { outboundMessagesPerSecond outboundBurst } }`, governanceQuery(Outbound))
}

// A tenant that declared overrides is metered at them.
func TestResolveLimits_AppliesOverrides(t *testing.T) {
	got := resolveLimits(map[string]*json.Number{
		"ingestMessagesPerSecond": num("5"),
		"ingestBurst":             num("10"),
	}, platformDefault, Ingest)
	assert.Equal(t, Limits{MessagesPerSecond: 5, Burst: 10}, got)
}

// A null override (the common case — the tenant declared none) inherits the
// platform default, which is itself a limit, never unlimited.
func TestResolveLimits_NullInheritsDefault(t *testing.T) {
	got := resolveLimits(map[string]*json.Number{
		"ingestMessagesPerSecond": nil,
		"ingestBurst":             nil,
	}, platformDefault, Ingest)
	assert.Equal(t, platformDefault, got)

	// An absent key (field never returned) is the same story.
	assert.Equal(t, platformDefault, resolveLimits(map[string]*json.Number{}, platformDefault, Ingest))
	assert.Equal(t, platformDefault, resolveLimits(nil, platformDefault, Ingest))
}

// Each field falls back independently: overriding the rate alone must not reset
// the burst to zero.
func TestResolveLimits_PartialOverride(t *testing.T) {
	got := resolveLimits(map[string]*json.Number{
		"ingestMessagesPerSecond": num("5"),
	}, platformDefault, Ingest)
	assert.Equal(t, Limits{MessagesPerSecond: 5, Burst: platformDefault.Burst}, got)
}

// THE DRIFT THIS CONSOLIDATION FIXES. A non-positive override can only reach the
// column via an out-of-band DB write (the GraphQL write path rejects it), and
// core.TenantRateLimiter documents that a non-positive ceiling admits NOTHING — so
// passing one through would black-hole that tenant. It must floor to the default.
// Two of the three pre-consolidation copies had this guard; event-sources did not.
func TestResolveLimits_NonPositiveFloorsToDefault(t *testing.T) {
	for _, tc := range []struct{ name, rate, burst string }{
		{"zero", "0", "0"},
		{"negative", "-1", "-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLimits(map[string]*json.Number{
				"ingestMessagesPerSecond": num(tc.rate),
				"ingestBurst":             num(tc.burst),
			}, platformDefault, Ingest)
			assert.Equal(t, platformDefault, got, "a non-positive override must inherit the default, never meter at zero")
		})
	}
}

// A malformed number is ignored rather than trusted as a zero ceiling.
func TestResolveLimits_UnparseableFloorsToDefault(t *testing.T) {
	got := resolveLimits(map[string]*json.Number{
		"ingestMessagesPerSecond": num("not-a-number"),
		"ingestBurst":             num("not-a-number"),
	}, platformDefault, Ingest)
	assert.Equal(t, platformDefault, got)
}

// A fractional rate is legal (0.5/s = one call every two seconds); a fractional
// burst is not an integer and must not silently truncate to a live ceiling.
func TestResolveLimits_FractionalRate(t *testing.T) {
	got := resolveLimits(map[string]*json.Number{
		"ingestMessagesPerSecond": num("0.5"),
		"ingestBurst":             num("2.5"),
	}, platformDefault, Ingest)
	assert.Equal(t, 0.5, got.MessagesPerSecond, "a sub-1/s rate is a legal ceiling")
	assert.Equal(t, platformDefault.Burst, got.Burst, "a non-integer burst is unparseable as Int64 → inherit")
}

// The fetcher reads only its own dimension: an outbound override must not leak
// into an ingest resolver.
func TestResolveLimits_IgnoresOtherDimensions(t *testing.T) {
	got := resolveLimits(map[string]*json.Number{
		"outboundMessagesPerSecond": num("5"),
		"outboundBurst":             num("10"),
	}, platformDefault, Ingest)
	assert.Equal(t, platformDefault, got, "another dimension's override must not govern ingest")
}
