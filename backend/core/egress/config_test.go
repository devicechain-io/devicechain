// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"net/netip"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnAllowedDestinationIsActuallyReachable(t *testing.T) {
	g, err := FromConfig(config.EgressConfiguration{AllowedDestinations: []string{"10.42.0.17/32"}})
	require.NoError(t, err)

	assert.NoError(t, g.CheckAddr(netip.MustParseAddr("10.42.0.17")))
	assert.Error(t, g.CheckAddr(netip.MustParseAddr("10.42.0.18")),
		"a /32 allowance must not widen to its neighbour")
	assert.Error(t, g.CheckAddr(netip.MustParseAddr("169.254.169.254")),
		"one allowance must not open the rest of the deny list")
}

func TestNoConfigurationMeansNoAllowances(t *testing.T) {
	g, err := FromConfig(config.EgressConfiguration{})
	require.NoError(t, err)
	assert.Error(t, g.CheckAddr(netip.MustParseAddr("10.42.0.17")),
		"the default must be fail-closed")
}

// 🔴 The check that exists because getting it wrong is silent and widens the boundary.
// netip.ParsePrefix accepts 10.1.2.3/24 and quietly ignores the host bits, so an operator
// who meant one address and mistyped the length would grant a whole /24 — and the line in
// their values file would still read like the single address they intended.
func TestAnUnmaskedPrefixIsRejectedRatherThanSilentlyWidened(t *testing.T) {
	p, err := netip.ParsePrefix("10.1.2.3/24")
	require.NoError(t, err, "precondition: Go accepts this, which is the whole problem")
	require.Equal(t, "10.1.2.0/24", p.Masked().String(),
		"precondition: it silently means a /24, not the address written")

	_, err = ParsePrefixes([]string{"10.1.2.3/24"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.1.2.0/24",
		"the error must show the range it would actually have granted")
	assert.Contains(t, err.Error(), "/32",
		"and name the length that expresses what was probably meant")
}

// A malformed entry fails startup rather than being skipped. A dropped allowance would
// look configured and behave as though it were not, and the only symptom would be
// deliveries failing for a reason the configuration appears to have already handled.
func TestAMalformedPrefixIsAnError(t *testing.T) {
	for _, bad := range []string{"not-an-address", "10.1.2.3", "10.1.2.0/33", "fd00::/999"} {
		t.Run(bad, func(t *testing.T) {
			_, err := ParsePrefixes([]string{bad})
			require.Error(t, err)
			assert.Contains(t, err.Error(), bad, "the error must name the entry to fix")
		})
	}
}

func TestBlankEntriesAreIgnored(t *testing.T) {
	got, err := ParsePrefixes([]string{"", "  ", "10.1.2.0/24"})
	require.NoError(t, err)
	assert.Len(t, got, 1, "whitespace from a YAML list should not become a failed parse")
}

func TestIPv6AllowancesWork(t *testing.T) {
	g, err := FromConfig(config.EgressConfiguration{AllowedDestinations: []string{"fd00:abcd::1/128"}})
	require.NoError(t, err)
	assert.NoError(t, g.CheckAddr(netip.MustParseAddr("fd00:abcd::1")))
	assert.Error(t, g.CheckAddr(netip.MustParseAddr("fd00:abcd::2")))
}
