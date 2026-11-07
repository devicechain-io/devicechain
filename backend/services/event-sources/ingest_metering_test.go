// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func envOf(v string) func(string) string {
	return func(k string) string {
		if k == IngestMeteringEnv {
			return v
		}
		return ""
	}
}

func TestIngestMeteringDefaultsToLegacy(t *testing.T) {
	for _, v := range []string{"", "  ", "legacy"} {
		mode, err := resolveIngestMetering(envOf(v))
		require.NoError(t, err, "%q", v)
		require.Equal(t, IngestMeteringLegacy, mode)
	}
}

// lagCredit is a mode this build runs: it resolves, and it is never quietly legacy.
func TestIngestMeteringLagCreditResolves(t *testing.T) {
	mode, err := resolveIngestMetering(envOf("lagCredit"))
	require.NoError(t, err)
	require.Equal(t, IngestMeteringLagCredit, mode)
	require.NotEqual(t, IngestMeteringLegacy, mode)
}

func TestIngestMeteringRejectsAnUnknownValue(t *testing.T) {
	for _, v := range []string{"lagcredit", "LEGACY", "on", "true", "lag-credit"} {
		_, err := resolveIngestMetering(envOf(v))
		require.Error(t, err, "%q must not be accepted", v)
		require.Contains(t, err.Error(), "is not a metering mode")
	}
}
