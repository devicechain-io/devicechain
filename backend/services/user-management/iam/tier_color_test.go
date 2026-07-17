// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidTierColor(t *testing.T) {
	// Empty is the "no pill" default and must validate.
	require.True(t, ValidTierColor(""))
	// Every listed palette token validates.
	for _, c := range TierColors() {
		require.True(t, ValidTierColor(c), "palette token %q must be accepted", c)
	}
	// A raw hex value is exactly what the registry exists to reject — it carries no
	// contrast guarantee.
	require.False(t, ValidTierColor("#ff0000"))
	require.False(t, ValidTierColor("cornflowerblue"))
	require.False(t, ValidTierColor("Slate")) // case-sensitive: tokens are lower-case
}

// TestTierColorsIsStableAndNonEmpty guards the picker contract: the palette the console
// renders is the exact set the server validates, in a fixed order.
func TestTierColorsIsStableAndNonEmpty(t *testing.T) {
	colors := TierColors()
	require.NotEmpty(t, colors)
	require.Equal(t, colors, TierColors(), "TierColors must be stable across calls")
	seen := map[string]bool{}
	for _, c := range colors {
		require.False(t, seen[c], "palette has a duplicate: %q", c)
		seen[c] = true
	}
}
