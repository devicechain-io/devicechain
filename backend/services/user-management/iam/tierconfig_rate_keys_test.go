// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A tier declares its ingest ceiling in READINGS and its outbound ceiling in CALLS, and the
// keys say so. The old spellings are not aliases: the registry refuses them like any other
// unknown key, so a tier config written against them fails loudly instead of being stored
// and silently never read.
func TestTierConfigRateKeysNameTheirUnits(t *testing.T) {
	require.NoError(t, ValidateTierConfig(map[string]any{
		"ingestReadingsPerSecond": float64(2000),
		"outboundCallsPerSecond":  float64(200),
	}))

	for _, old := range []string{"ingestMessagesPerSecond", "outboundMessagesPerSecond"} {
		err := ValidateTierConfig(map[string]any{old: float64(2000)})
		require.Errorf(t, err, "%s was accepted; the renamed key must be refused", old)
		require.Contains(t, err.Error(), old, "the refusal must name the key")
	}
}
